package guard

import (
	"strings"
	"testing"

	"github.com/acycl/git-inlay/internal/inlay"
	"github.com/acycl/git-inlay/internal/manifest"
)

func entry(t *testing.T, name string, paths ...string) inlay.Entry {
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
	return inlay.Entry{Key: "main", Name: name, Manifest: m}
}

func TestIndexOwner(t *testing.T) {
	x := NewIndex([]inlay.Entry{entry(t, "team", ".claude/"), entry(t, "me", "notes/research.md")}, false)
	cases := map[string]string{
		".claude/settings.json":  "team",
		".git-inlay/team.paths":  "team",
		".git-inlay/me.paths":    "me",
		"notes/research.md":      "me",
		"notes/other.md":         "",
		".git-inlay/other.paths": "",
		".Claude/settings.json":  "",
		"Notes/Research.md":      "",
	}
	for p, want := range cases {
		o, ok := x.Owner(p)
		if got := o.Inlay; got != want || ok != (want != "") {
			t.Errorf("Owner(%q) = %q, %v; want %q", p, got, ok, want)
		}
	}
	if n := len(x.Claims()); n != 4 {
		t.Errorf("Claims() has %d entries, want 4", n)
	}
}

// On a case-insensitive host every case variant of a claimed path, and of
// the reserved manifest directory, is the claim's, and the owner reports
// the claim as the manifest spells it.
func TestIndexOwnerFolded(t *testing.T) {
	x := NewIndex([]inlay.Entry{entry(t, "team", ".claude/"), entry(t, "me", "Notes/Research.md")}, true)
	cases := map[string]string{
		".Claude/settings.json": "team",
		".CLAUDE/a/b":           "team",
		".claude/settings.json": "team",
		"notes/research.md":     "me",
		"NOTES/RESEARCH.MD":     "me",
		".Git-Inlay/team.paths": "team",
		"notes/research.md.bak": "",
		".claudex/a":            "",
	}
	for p, want := range cases {
		o, ok := x.Owner(p)
		if got := o.Inlay; got != want || ok != (want != "") {
			t.Errorf("Owner(%q) = %q, %v; want %q", p, got, ok, want)
		}
	}
	if o, _ := x.Owner("notes/research.md"); o.Claim.Path != "Notes/Research.md" {
		t.Errorf("Owner reports claim %q, want the manifest's spelling", o.Claim)
	}
	if v, ok := claimed(x, ".GIT-INLAY/other.paths"); !ok || !strings.Contains(v.Reason, "reserved") {
		t.Errorf("claimed(.GIT-INLAY/other.paths) = %v, %v; want the reserved directory", v, ok)
	}
	if !x.Owns(manifest.Claim{Path: "Docs", Dir: true}, "docs/notes.md") {
		t.Error("Owns does not fold case")
	}

	// Letters beyond ASCII fold too, including those whose lower case
	// has two forms.
	g := NewIndex([]inlay.Entry{entry(t, "g", "λόγος/"), entry(t, "s", "ſtore/"), entry(t, "c", "Café/")}, true)
	for p, want := range map[string]string{"ΛΌΓΟΣ/x": "g", "λόγοσ/x": "g", "STORE/x": "s", "CAFÉ/x": "c"} {
		if o, ok := g.Owner(p); !ok || o.Inlay != want {
			t.Errorf("Owner(%q) = %q, %v; want %q", p, o.Inlay, ok, want)
		}
	}
}

func TestOverlaps(t *testing.T) {
	entries := []inlay.Entry{entry(t, "a", "docs/"), entry(t, "b", "docs/research/"), entry(t, "c", "src/")}
	vs := NewIndex(entries, false).Overlaps(entries)
	if len(vs) != 1 || vs[0].Inlay != "a" || !strings.Contains(vs[0].Reason, "inlay b") {
		t.Fatalf("Overlaps = %v", vs)
	}
	// Claims that differ only in case overlap on a case-insensitive host.
	entries = []inlay.Entry{entry(t, "a", "Docs/"), entry(t, "b", "docs/notes.md")}
	if vs := NewIndex(entries, false).Overlaps(entries); len(vs) != 0 {
		t.Fatalf("exact Overlaps = %v", vs)
	}
	if vs := NewIndex(entries, true).Overlaps(entries); len(vs) != 1 {
		t.Fatalf("folded Overlaps = %v", vs)
	}
}

func TestParseRefUpdates(t *testing.T) {
	in := "refs/heads/main 1111 refs/heads/main 2222\n\nrefs/tags/v1 3333 refs/tags/v1 0000000000000000000000000000000000000000\n"
	got, err := ParseRefUpdates(strings.NewReader(in))
	if err != nil || len(got) != 2 {
		t.Fatalf("ParseRefUpdates = %v, %v", got, err)
	}
	if got[0] != (RefUpdate{"refs/heads/main", "1111", "refs/heads/main", "2222"}) {
		t.Errorf("first update = %+v", got[0])
	}
	if !IsZero(got[1].RemoteOID) || IsZero(got[1].LocalOID) || IsZero("") {
		t.Error("IsZero misclassified an oid")
	}
	if _, err := ParseRefUpdates(strings.NewReader("too few fields\n")); err == nil {
		t.Error("malformed line accepted")
	}
}
