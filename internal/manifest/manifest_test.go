package manifest

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestValidatePath(t *testing.T) {
	bad := []string{"", "/abs", "a//b", "a/./b", "../x", "a/..", "#x", " a", "a ", "a /b", "a\\b", ".git/x", ".GIT/x", ".git-inlay/x.paths", ".Git-Inlay/x", "a\nb", "a\rb"}
	for _, p := range bad {
		if err := ValidatePath(p); err == nil {
			t.Errorf("ValidatePath(%q) = nil, want error", p)
		}
	}
	good := []string{".claude", "docs/research", "a b/c", "x#y", "star*", ".gitignore", ".github", "packages/foo/.claude"}
	for _, p := range good {
		if err := ValidatePath(p); err != nil {
			t.Errorf("ValidatePath(%q) = %v, want nil", p, err)
		}
	}
}

func TestParseClaim(t *testing.T) {
	c, err := ParseClaim("docs/")
	if err != nil || c != (Claim{Path: "docs", Dir: true}) {
		t.Fatalf("ParseClaim(docs/) = %+v, %v", c, err)
	}
	c, err = ParseClaim(".claude/settings.json")
	if err != nil || c != (Claim{Path: ".claude/settings.json"}) {
		t.Fatalf("ParseClaim(file) = %+v, %v", c, err)
	}
	if _, err := ParseClaim("/"); err == nil {
		t.Fatal("ParseClaim(/) accepted the root")
	}
}

func TestClaimOwns(t *testing.T) {
	dir := Claim{Path: "a/b", Dir: true}
	for _, p := range []string{"a/b", "a/b/c", "a/b/c/d"} {
		if !dir.Owns(p) {
			t.Errorf("%s does not own %s", dir, p)
		}
	}
	for _, p := range []string{"a", "a/bc", "a/bc/d", "b"} {
		if dir.Owns(p) {
			t.Errorf("%s owns %s", dir, p)
		}
	}
	file := Claim{Path: "a/b"}
	if !file.Owns("a/b") || file.Owns("a/b/c") {
		t.Error("a file claim must own exactly itself")
	}
	if got := (Claim{Path: "a/b/c"}).Ancestors(); !slices.Equal(got, []string{"a", "a/b"}) {
		t.Errorf("Ancestors = %v", got)
	}
}

func TestAddAbsorbsAndRejects(t *testing.T) {
	m := New("x")
	for _, s := range []string{"docs/research/", "docs/notes.md", "src/"} {
		c, _ := ParseClaim(s)
		if _, err := m.Add(c); err != nil {
			t.Fatal(err)
		}
	}
	absorbed, err := m.Add(Claim{Path: "docs", Dir: true})
	if err != nil || len(absorbed) != 2 {
		t.Fatalf("Add(docs/) absorbed %v, err %v; want the two docs claims", absorbed, err)
	}
	if got := m.Claims; len(got) != 2 || got[0].Path != "docs" || got[1].Path != "src" {
		t.Fatalf("claims after absorb = %v", got)
	}
	if _, err := m.Add(Claim{Path: "docs/research", Dir: true}); !errors.Is(err, ErrRedundant) {
		t.Fatalf("Add under docs/ = %v, want ErrRedundant", err)
	}
	if _, err := m.Add(Claim{Path: "docs", Dir: true}); !errors.Is(err, ErrRedundant) {
		t.Fatalf("Add of an identical claim = %v, want ErrRedundant", err)
	}

	m2 := New("y")
	m2.Add(Claim{Path: "x"})
	absorbed, err = m2.Add(Claim{Path: "x", Dir: true})
	if err != nil || len(absorbed) != 1 || !m2.Claims[0].Dir {
		t.Fatalf("changing a claim's kind: absorbed %v, err %v, claims %v", absorbed, err, m2.Claims)
	}
	if !m2.Remove("x/") || len(m2.Claims) != 0 || m2.Remove("x") {
		t.Fatal("Remove did not behave")
	}
}

func TestParseFormatRoundTrip(t *testing.T) {
	in := "# header\n\ndocs/research/\n.claude/\r\nnotes.md\ndocs/research/deep.md\n"
	m, err := Parse("claude", []byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []Claim{{Path: ".claude", Dir: true}, {Path: "docs/research", Dir: true}, {Path: "notes.md"}}
	if !slices.Equal(m.Claims, want) {
		t.Fatalf("Parse = %v, want %v", m.Claims, want)
	}
	out := m.Format()
	again, err := Parse("claude", out)
	if err != nil || !slices.Equal(again.Claims, want) {
		t.Fatalf("round trip = %v, %v\n%s", again.Claims, err, out)
	}
	if _, ok := m.Owner(".git-inlay/claude.paths"); !ok {
		t.Error("the manifest does not own its own file")
	}
	if _, err := Parse("claude", []byte("/abs\n")); err == nil {
		t.Error("Parse accepted an absolute path")
	}
}

// Parse skips a byte order mark and refuses what would otherwise pass for
// a claim: the markers of an unresolved merge and control characters.
func TestParseRefusesNonClaims(t *testing.T) {
	m, err := Parse("x", []byte("\xef\xbb\xbf.claude/\nnotes.md\n"))
	if err != nil || !slices.Equal(m.Claims, []Claim{{Path: ".claude", Dir: true}, {Path: "notes.md"}}) {
		t.Fatalf("Parse with a byte order mark = %v, %v", m, err)
	}
	conflict := "a.md\n<<<<<<< HEAD\nb.md\n||||||| base\n=======\nc.md\n>>>>>>> origin/main\n"
	if _, err := Parse("x", []byte(conflict)); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("Parse of a conflicted manifest = %v, want ErrConflict at line 2", err)
	}
	for _, marker := range []string{"<<<<<<<", "=======", ">>>>>>> theirs", "||||||| base"} {
		if _, err := Parse("x", []byte(marker+"\n")); !errors.Is(err, ErrConflict) {
			t.Errorf("Parse(%q) = %v, want ErrConflict", marker, err)
		}
	}
	// A name that only starts like a marker is a claim.
	if _, err := Parse("x", []byte("=======.md\n")); err != nil {
		t.Errorf("Parse of a path that starts like a marker: %v", err)
	}
	for _, bad := range []string{"a\x00b", "a\tb", "n\x00o\x00t\x00e\x00", "a\x7fb"} {
		if _, err := Parse("x", []byte(bad+"\n")); err == nil || !strings.Contains(err.Error(), "control character") {
			t.Errorf("Parse(%q) = %v, want a control character error", bad, err)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, n := range []string{"claude", "me", "team-notes", "a.b_c", "x1"} {
		if err := ValidName(n); err != nil {
			t.Errorf("ValidName(%q) = %v", n, err)
		}
	}
	for _, n := range []string{"", "Claude", "-x", ".x", "a/b", "a b"} {
		if err := ValidName(n); err == nil {
			t.Errorf("ValidName(%q) = nil, want error", n)
		}
	}
}
