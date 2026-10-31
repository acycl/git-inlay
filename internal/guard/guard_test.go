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
	x := NewIndex([]inlay.Entry{entry(t, "team", ".claude/"), entry(t, "me", "notes/research.md")})
	cases := map[string]string{
		".claude/settings.json":  "team",
		".git-inlay/team.paths":  "team",
		".git-inlay/me.paths":    "me",
		"notes/research.md":      "me",
		"notes/other.md":         "",
		".git-inlay/other.paths": "",
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

func TestOverlaps(t *testing.T) {
	entries := []inlay.Entry{entry(t, "a", "docs/"), entry(t, "b", "docs/research/"), entry(t, "c", "src/")}
	vs := Overlaps(entries)
	if len(vs) != 1 || vs[0].Inlay != "a" || !strings.Contains(vs[0].Reason, "inlay b") {
		t.Fatalf("Overlaps = %v", vs)
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
