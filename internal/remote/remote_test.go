package remote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeEquivalents(t *testing.T) {
	same := []string{
		"git@github.com:acme/app.git",
		"ssh://git@github.com/acme/app",
		"ssh://git@github.com:22/acme/app.git",
		"git+ssh://git@github.com:22/acme/app.git",
		"https://github.com/ACME/App/",
		"https://github.com/acme/app.git",
		"https://user@github.com/acme/app",
	}
	want := normalize(same[0])
	if want != "github.com/acme/app" {
		t.Fatalf("normalize(%q) = %q", same[0], want)
	}
	for _, u := range same[1:] {
		if got := normalize(u); got != want {
			t.Errorf("normalize(%q) = %q, want %q", u, got, want)
		}
	}
	different := []string{"https://github.com/acme/app2", "https://gitlab.com/acme/app", "ssh://git@github.com:2222/acme/app2"}
	for _, u := range different {
		if Same(same[0], u) {
			t.Errorf("Same(%q, %q) = true", same[0], u)
		}
	}
}

// A port tells repositories apart only within one transport: the same
// server answers https on 443 and ssh on a port of its own.
func TestSamePorts(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"https://git.corp.example/acme/app.git", "ssh://git@git.corp.example:2222/acme/app.git", true},
		{"https://git.corp.example:8443/acme/app", "git@git.corp.example:acme/app", true},
		{"http://git.corp.example/acme/app", "https://git.corp.example/acme/app", true},
		// Without a port a URL may reach any: ~/.ssh/config can set it.
		{"ssh://git@git.corp.example/acme/app", "ssh://git@git.corp.example:2222/acme/app", true},
		{"git@git.corp.example:acme/app", "ssh://git@git.corp.example:2222/acme/app.git", true},
		{"git@git.corp.example:acme/app", "ssh://git.corp.example:22/acme/app", true},
		{"https://git.corp.example/acme/app", "https://git.corp.example:8443/acme/app", true},
		// Two ports named for one transport are two servers.
		{"ssh://git@git.corp.example:22/acme/app", "ssh://git@git.corp.example:2222/acme/app", false},
		{"https://git.corp.example:443/acme/app", "https://git.corp.example:8443/acme/app", false},
		// Brackets inside a path are the path's.
		{"git@host.example:org/a[b]:c.git", "ssh://git@host.example/org/a[b]:c", true},
		{"git@[::1]:acme/app", "ssh://git@[::1]/acme/app.git", true},
		{"https://git.corp.example/acme/app", "ssh://git@git.corp.example:2222/acme/other", false},
	}
	for _, c := range cases {
		if got := Same(c.a, c.b); got != c.want {
			t.Errorf("Same(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestScpLike(t *testing.T) {
	cases := map[string]bool{
		"git@github.com:acme/app": true,
		"github.com:acme/app":     true,
		"C:/repos/app":            false,
		"host/dir:x":              false,
		"/srv/git/app.git":        false,
		"app.git":                 false,
		"git@[::1]:acme/app":      true,
		"ssh://host:22/acme/app":  false,
	}
	for in, want := range cases {
		if got := scpLike(in); got != want {
			t.Errorf("scpLike(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestApplyLongestPrefix(t *testing.T) {
	rules := []rewrite{
		{base: "https://github.com/", prefix: "gh:"},
		{base: "https://github.com/acme/", prefix: "gh:acme/"},
	}
	got, ok := apply(rules, "gh:acme/app")
	if !ok || got != "https://github.com/acme/app" {
		t.Fatalf("apply = %q, %v", got, ok)
	}
	if got, ok := apply(rules, "https://example.com/x"); ok || got != "https://example.com/x" {
		t.Fatalf("apply without a match = %q, %v", got, ok)
	}
}

// A local repository reached through a symbolic link compares equal to its
// real path, also when it is named without the ".git" of its directory, so
// that the last component does not exist.
func TestSameLocalPathThroughSymlink(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "real", "app.git")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(filepath.Join(base, "real"), link); err != nil {
		t.Skipf("cannot create a symbolic link: %v", err)
	}
	for _, u := range []string{filepath.Join(link, "app.git"), filepath.Join(link, "app"), "file:///" + strings.TrimPrefix(filepath.ToSlash(filepath.Join(link, "app")), "/")} {
		if !Same(repo, u) {
			t.Errorf("Same(%q, %q) = false", repo, u)
		}
	}
	if Same(repo, filepath.Join(link, "other")) {
		t.Error("two repositories in one directory compare equal")
	}
}
