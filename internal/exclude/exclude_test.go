package exclude

import (
	"strings"
	"testing"

	"github.com/acycl/git-inlay/internal/manifest"
)

func claims(t *testing.T, name string, paths ...string) *manifest.Manifest {
	t.Helper()
	m := manifest.New(name)
	for _, p := range paths {
		c, err := manifest.ParseClaim(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Add(c); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func TestInlayMatchesPlanExample(t *testing.T) {
	m := claims(t, "claude", ".claude/", "docs/research/", "packages/foo/.claude/")
	got := Inlay(m)
	_, body, _ := strings.Cut(got, "\n")
	want := `/*
!/.git-inlay/
/.git-inlay/*
!/docs/
/docs/*
!/packages/
/packages/*
!/packages/foo/
/packages/foo/*
!/.claude/
!/.git-inlay/claude.paths
!/docs/research/
!/packages/foo/.claude/
`
	if body != want {
		t.Fatalf("Inlay chain:\n%s\nwant:\n%s", body, want)
	}
}

func TestHostBlock(t *testing.T) {
	m := claims(t, "claude", "docs/research/", ".claude/", "odd#name")
	got := HostBlock(m.All())
	want := HostBegin + "\n/.claude\n/.git-inlay\n/docs/research\n/odd\\#name\n" + HostEnd + "\n"
	if got != want {
		t.Fatalf("HostBlock:\n%s\nwant:\n%s", got, want)
	}
}

func TestSplicePreservesOtherContent(t *testing.T) {
	first := HostBlock(nil)
	s := Splice([]byte("*.log"), first)
	if string(s) != "*.log\n"+first {
		t.Fatalf("append: %q", s)
	}
	s = Splice(append(s, "tail\n"...), "# >>> git-inlay managed block; do not edit >>>\n/x\n# <<< git-inlay managed block <<<\n")
	if string(s) != "*.log\n"+HostBegin+"\n/x\n"+HostEnd+"\ntail\n" {
		t.Fatalf("replace: %q", s)
	}
	if got := Strip(s); string(got) != "*.log\ntail\n" {
		t.Fatalf("Strip: %q", got)
	}
}

func TestEscape(t *testing.T) {
	cases := map[string]string{
		"plain":          "plain",
		"a#b!c*d?e[f\\g": "a\\#b\\!c\\*d\\?e\\[f\\\\g",
		"trail ":         "trail\\ ",
	}
	for in, want := range cases {
		if got := Escape(in); got != want {
			t.Errorf("Escape(%q) = %q, want %q", in, got, want)
		}
	}
}
