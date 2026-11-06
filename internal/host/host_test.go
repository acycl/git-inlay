package host

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/acycl/git-inlay/internal/gitx"
)

// Fold gives every case variant of a path one key, beyond ASCII too.
func TestFold(t *testing.T) {
	for _, c := range []struct{ a, b string }{
		{".Claude/Settings.json", ".claude/settings.json"},
		{"CAFÉ/x", "café/x"},
		{"ΛΌΓΟΣ", "λόγος"}, // final sigma
		{"λόγοσ", "λόγος"},
		{"ſtore", "STORE"},   // long s
		{"Kelvin", "kelvin"}, // Kelvin sign
	} {
		if Fold(c.a) != Fold(c.b) {
			t.Errorf("Fold(%q) = %q, Fold(%q) = %q; want equal", c.a, Fold(c.a), c.b, Fold(c.b))
		}
	}
	if Fold("a/b") == Fold("a-b") {
		t.Error("Fold merged different characters")
	}
	// Bytes that are not valid UTF-8 keep their identity.
	if Fold("caf\xe9") == Fold("caf\xe8") || Fold("CAF\xe9") != "caf\xe9" {
		t.Errorf("Fold of Latin-1 names: %q, %q", Fold("caf\xe9"), Fold("CAF\xe9"))
	}
}

func TestASCII(t *testing.T) {
	if !ascii(":(literal).claude/docs/a.md") {
		t.Error("ascii rejected an ASCII pathspec")
	}
	if ascii(":(literal)Café") {
		t.Error("ascii accepted a non-ASCII pathspec")
	}
}

// Worktree finds a linked worktree's root from its gitdir file, absolute
// or relative, and calls it gone only on an absence, never when locked or
// when its path cannot be read.
func TestWorktree(t *testing.T) {
	base := t.TempDir()
	common := filepath.Join(base, "main", ".git")
	h := &Host{CommonDir: common, Key: "main", Root: filepath.Join(base, "main")}
	add := func(id, gitdir string) string {
		t.Helper()
		admin := filepath.Join(common, "worktrees", id)
		if err := os.MkdirAll(admin, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(admin, "gitdir"), []byte(gitdir+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return admin
	}
	tree := func(rel string) string {
		t.Helper()
		root := filepath.Join(base, rel)
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return root
	}
	absRoot := tree("abs")
	add("abs", filepath.ToSlash(filepath.Join(absRoot, ".git")))
	relRoot := tree("rel")
	add("rel", "../../../../rel/.git")
	add("deleted", filepath.ToSlash(filepath.Join(base, "deleted", ".git")))
	locked := add("locked", filepath.ToSlash(filepath.Join(base, "unmounted", ".git")))
	if err := os.WriteFile(filepath.Join(locked, "locked"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	add("notdir", filepath.ToSlash(filepath.Join(base, "file", "wt", ".git")))
	add("empty", "")

	for _, c := range []struct {
		key  string
		root string
		gone bool
	}{
		{"main", h.Root, false},
		{"wt/abs", absRoot, false},
		{"wt/rel", relRoot, false},
		{"wt/deleted", "", true},
		{"wt/locked", "", false},
		{"wt/notdir", "", true},
		{"wt/empty", "", true},
		{"wt/never", "", true},
	} {
		root, gone := h.Worktree(c.key)
		if root != c.root || gone != c.gone {
			t.Errorf("Worktree(%q) = %q, %v; want %q, %v", c.key, root, gone, c.root, c.gone)
		}
	}

	// Seen from a linked worktree, main's root is the parent of $C.
	linked := &Host{CommonDir: common, Key: "wt/abs", Root: absRoot}
	if root, gone := linked.Worktree("main"); root != h.Root || gone {
		t.Errorf("Worktree(main) from a linked worktree = %q, %v", root, gone)
	}

	// A path that cannot be read is not evidence the worktree is gone.
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		sealed := tree("sealed/wt")
		add("sealed", filepath.ToSlash(filepath.Join(sealed, ".git")))
		if err := os.Chmod(filepath.Join(base, "sealed"), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(filepath.Join(base, "sealed"), 0o755) })
		if root, gone := h.Worktree("wt/sealed"); root != "" || gone {
			t.Errorf("Worktree of an unreadable path = %q, %v; want not gone", root, gone)
		}
	}
}

// isolatedEnv builds a git environment that ignores the developer's own
// configuration and any inherited repository variables.
func isolatedEnv(t *testing.T, base string) []string {
	t.Helper()
	home := filepath.Join(base, "home")
	cfg := filepath.Join(home, "gitconfig")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[user]\n\tname = test\n\temail = test@example.com\n[init]\n\tdefaultBranch = main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return []string{
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=" + cfg,
		"GIT_CONFIG_NOSYSTEM=1",
		"LC_ALL=C",
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + os.TempDir(),
	}
}

// realTempDir returns a fresh temp dir with symlinks resolved, as git
// reports paths.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// Rel maps a path given from a cwd reached through a symlink to the root
// into the real root, created parts or not, while the last component of
// the path is kept as named, since git tracks a symlink as itself.
func TestRelThroughSymlink(t *testing.T) {
	base := realTempDir(t)
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("cannot make a symlink: %v", err)
	}
	// A symlink inside the root that points outside it.
	if err := os.Symlink(base, filepath.Join(root, "sub", "out")); err != nil {
		t.Fatal(err)
	}
	// One inside the root that points at the root: tracked as itself.
	if err := os.Symlink(root, filepath.Join(root, "sub", "up")); err != nil {
		t.Fatal(err)
	}
	// Spelled as git spells it, with forward slashes on Windows too.
	h := &Host{Root: filepath.ToSlash(root)}
	sub := filepath.Join(link, "sub")
	for _, c := range []struct{ cwd, p, want string }{
		{link, ".", "."},
		{base, "link", "."},
		{sub, "..", "."},
		{sub, "up", "sub/up"},
		{link, "a.txt", "a.txt"},
		{sub, "b.txt", "sub/b.txt"},
		{sub, filepath.Join("..", "c.txt"), "c.txt"},
		{sub, filepath.Join("new", "deeper", "d.txt"), "sub/new/deeper/d.txt"},
		{base, filepath.Join("link", "sub", "e.txt"), "sub/e.txt"},
		{base, filepath.Join(sub, "f.txt"), "sub/f.txt"},
		{sub, "out", "sub/out"},
	} {
		got, err := h.Rel(c.cwd, c.p)
		if err != nil || got != c.want {
			t.Errorf("Rel(%q, %q) = %q, %v; want %q", c.cwd, c.p, got, err, c.want)
		}
	}
	for _, c := range []struct{ cwd, p string }{
		{link, filepath.Join("..", "outside")},
		{sub, filepath.Join("out", "x")},
	} {
		if got, err := h.Rel(c.cwd, c.p); err == nil {
			t.Errorf("Rel(%q, %q) = %q, want an outside-the-repository error", c.cwd, c.p, got)
		}
	}
}

// Discover keys the main worktree "main" and a linked worktree
// "wt/<id>", where id names its administrative directory under the common
// dir, which differs from the path's base name when two collide.
func TestDiscoverKey(t *testing.T) {
	base := realTempDir(t)
	env := isolatedEnv(t, base)
	git := func(dir string, args ...string) {
		t.Helper()
		if err := (gitx.Runner{Dir: dir, Env: env}).Run(args...); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(base, "main")
	git(base, "init", "-q", "-b", "main", root)
	git(root, "commit", "-q", "--allow-empty", "-m", "init")
	one := filepath.Join(base, "a", "wt")
	two := filepath.Join(base, "b", "wt")
	git(root, "worktree", "add", "-q", "-b", "one", one)
	git(root, "worktree", "add", "-q", "-b", "two", two)
	for _, dir := range []string{root, one} {
		if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	common := filepath.Join(root, ".git")
	for _, c := range []struct{ dir, root, gitDir, key string }{
		{root, root, common, "main"},
		{filepath.Join(root, "sub"), root, common, "main"},
		{one, one, filepath.Join(common, "worktrees", "wt"), "wt/wt"},
		{filepath.Join(one, "sub"), one, filepath.Join(common, "worktrees", "wt"), "wt/wt"},
		{two, two, filepath.Join(common, "worktrees", "wt1"), "wt/wt1"},
	} {
		h, err := Discover(c.dir, env)
		if err != nil {
			t.Fatalf("Discover(%q): %v", c.dir, err)
		}
		if h.Key != c.key {
			t.Errorf("Discover(%q).Key = %q, want %q", c.dir, h.Key, c.key)
		}
		// Git prints forward slashes; Clean gives native separators.
		got := []string{filepath.Clean(h.Root), filepath.Clean(h.GitDir), filepath.Clean(h.CommonDir)}
		if want := []string{c.root, c.gitDir, common}; !slices.Equal(got, want) {
			t.Errorf("Discover(%q) root, git dir, common dir = %q, want %q", c.dir, got, want)
		}
	}

	// Each worktree finds the others from the files under the common dir.
	for _, c := range []struct{ from, key, want string }{
		{root, "wt/wt", one},
		{root, "wt/wt1", two},
		{one, "main", root},
		{one, "wt/wt1", two},
	} {
		h, err := Discover(c.from, env)
		if err != nil {
			t.Fatal(err)
		}
		if got, gone := h.Worktree(c.key); filepath.Clean(got) != c.want || gone {
			t.Errorf("Worktree(%q) from %s = %q, %v; want %q", c.key, c.from, got, gone, c.want)
		}
	}
}
