package inlay

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/acycl/git-inlay/internal/gitx"
)

// isolatedEnv builds a git environment that ignores the developer's own
// configuration and any inherited repository variables.
func isolatedEnv(t *testing.T, base string) []string {
	t.Helper()
	home := filepath.Join(base, "home")
	cfg := filepath.Join(home, "gitconfig")
	writeFile(t, cfg, "[user]\n\tname = test\n\temail = test@example.com\n[init]\n\tdefaultBranch = main\n")
	return []string{
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=" + cfg,
		"GIT_CONFIG_NOSYSTEM=1",
		"LC_ALL=C",
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + os.TempDir(),
	}
}

// writeFile writes content to path, creating parent directories.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ManifestAt answers an empty manifest for an unborn HEAD, a commit without
// the manifest file and an unknown revision, the parsed manifest for a
// commit that has it, and an error for one that does not parse, so a
// broken inlay never silently loses its claims.
func TestManifestAt(t *testing.T) {
	base := t.TempDir()
	env := isolatedEnv(t, base)
	gitDir := filepath.Join(base, "x.git")
	tree := filepath.Join(base, "tree")
	if err := os.Mkdir(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (gitx.Runner{Dir: base, Env: env}).Run("init", "-q", "--bare", "-b", "main", gitDir); err != nil {
		t.Fatal(err)
	}
	// Driven as an inlay is: a bare-initialized git dir with an explicit
	// work tree.
	run := gitx.Runner{GitDir: gitDir, WorkTree: tree, Dir: tree, Env: env}
	commit := func(rel, content, msg string) {
		t.Helper()
		writeFile(t, filepath.Join(tree, filepath.FromSlash(rel)), content)
		if err := run.Run("add", "--", rel); err != nil {
			t.Fatal(err)
		}
		if err := run.Run("commit", "-q", "-m", msg); err != nil {
			t.Fatal(err)
		}
	}
	claims := func(rev string) []string {
		t.Helper()
		m, err := ManifestAt(run, "x", rev)
		if err != nil {
			t.Fatalf("ManifestAt(%q): %v", rev, err)
		}
		if m.Name != "x" {
			t.Fatalf("ManifestAt(%q).Name = %q, want x", rev, m.Name)
		}
		var out []string
		for _, c := range m.Claims {
			out = append(out, c.String())
		}
		return out
	}

	if got := claims("HEAD"); len(got) != 0 {
		t.Errorf("claims at an unborn HEAD = %q, want none", got)
	}
	commit("README.md", "x\n", "no manifest")
	if got := claims("HEAD"); len(got) != 0 {
		t.Errorf("claims at a commit without the manifest = %q, want none", got)
	}
	commit(".git-inlay/x.paths", "# header\n\nnotes/\n.claude/settings.json\nnotes/a.md\n", "manifest")
	if got, want := claims("HEAD"), []string{".claude/settings.json", "notes/"}; !slices.Equal(got, want) {
		t.Errorf("claims at a commit with the manifest = %q, want %q", got, want)
	}
	for _, rev := range []string{"HEAD~1", "nope", "refs/heads/nope", strings.Repeat("1", 40)} {
		if got := claims(rev); len(got) != 0 {
			t.Errorf("claims at %q = %q, want none", rev, got)
		}
	}

	commit(".git-inlay/x.paths", "/abs\n", "broken manifest")
	if _, err := ManifestAt(run, "x", "HEAD"); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("ManifestAt of a manifest that does not parse: err = %v, want a parse error", err)
	}
}
