package remote

import "testing"

func TestNormalizeEquivalents(t *testing.T) {
	same := []string{
		"git@github.com:acme/app.git",
		"ssh://git@github.com/acme/app",
		"ssh://git@github.com:22/acme/app.git",
		"https://github.com/ACME/App/",
		"https://github.com/acme/app.git",
		"https://user@github.com/acme/app",
	}
	want := Normalize(same[0])
	if want != "github.com/acme/app" {
		t.Fatalf("Normalize(%q) = %q", same[0], want)
	}
	for _, u := range same[1:] {
		if got := Normalize(u); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", u, got, want)
		}
	}
	different := []string{"https://github.com/acme/app2", "https://gitlab.com/acme/app", "ssh://git@github.com:2222/acme/app"}
	for _, u := range different {
		if Same(same[0], u) {
			t.Errorf("Same(%q, %q) = true", same[0], u)
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
