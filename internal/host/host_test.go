package host

import "testing"

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
	if !ascii([]string{":(literal).claude", "docs/a.md"}) {
		t.Error("ascii rejected ASCII pathspecs")
	}
	if ascii([]string{":(literal).claude", ":(literal)Café"}) {
		t.Error("ascii accepted a non-ASCII pathspec")
	}
}
