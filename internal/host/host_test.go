package host

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
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
