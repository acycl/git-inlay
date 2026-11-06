package exclude

import (
	"os"
	"path/filepath"
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

	// A stray end marker before the block must not hide the real one.
	stray := HostEnd + "\n" + HostBegin + "\n/old\n" + HostEnd + "\n"
	if got := Splice([]byte(stray), first); string(got) != HostEnd+"\n"+first {
		t.Fatalf("stray end marker: %q", got)
	}
	if got := Strip([]byte(stray)); string(got) != HostEnd+"\n" {
		t.Fatalf("Strip with stray end marker: %q", got)
	}
}

func TestWriteHostAndRemoveHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exclude")
	m := claims(t, "claude", ".claude/")
	if err := WriteHost(path, m.All()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != HostBlock(m.All()) {
		t.Fatalf("WriteHost on a missing file: %q", got)
	}
	if err := RemoveHost(path); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "" {
		t.Fatalf("RemoveHost left %q", got)
	}
	if err := RemoveHost(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Fatalf("RemoveHost on a missing file: %v", err)
	}
}

// A block whose end marker was deleted runs to the end of the file, so one
// rewrite leaves the file current rather than nesting a second block.
func TestSpliceOrphanBegin(t *testing.T) {
	block := HostBlock([]manifest.Claim{{Path: "x"}})
	orphan := "*.log\n" + HostBegin + "\n/old\n"
	once := Splice([]byte(orphan), block)
	if string(once) != "*.log\n"+block {
		t.Fatalf("Splice = %q", once)
	}
	if twice := Splice(once, block); string(twice) != string(once) {
		t.Fatalf("second Splice = %q, want %q", twice, once)
	}
	path := filepath.Join(t.TempDir(), "exclude")
	if err := os.WriteFile(path, []byte(orphan), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, err := HostCurrent(path, []manifest.Claim{{Path: "x"}}); err != nil || ok {
		t.Fatalf("HostCurrent before WriteHost = %v, %v", ok, err)
	}
	if err := WriteHost(path, []manifest.Claim{{Path: "x"}}); err != nil {
		t.Fatal(err)
	}
	if ok, err := HostCurrent(path, []manifest.Claim{{Path: "x"}}); err != nil || !ok {
		t.Fatalf("HostCurrent after WriteHost = %v, %v", ok, err)
	}
}

// A marker counts only as a whole line: a comment quoting one is user
// content and stays.
func TestMarkersMatchWholeLines(t *testing.T) {
	quoted := "# git-inlay writes " + HostBegin + " here\n*.tmp\n"
	if got := Strip([]byte(quoted)); string(got) != quoted {
		t.Fatalf("Strip = %q", got)
	}
	block := HostBlock([]manifest.Claim{{Path: "x"}})
	if got := Splice([]byte(quoted), block); string(got) != quoted+block {
		t.Fatalf("Splice = %q", got)
	}
	crlf := "*.tmp\r\n" + HostBegin + "\r\n/old\r\n" + HostEnd + "\r\ntail\r\n"
	if got := Strip([]byte(crlf)); string(got) != "*.tmp\r\ntail\r\n" {
		t.Fatalf("Strip of a CRLF file = %q", got)
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
