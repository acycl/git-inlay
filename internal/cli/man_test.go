package cli

import (
	"os"
	"strings"
	"testing"
)

// The manual page is written by hand, so hold it to the verbs dispatch
// routes: each must have its synopsis line, apart from the hook family,
// which the shims call, and help.
func TestManPageCoversEveryVerb(t *testing.T) {
	page, err := os.ReadFile("../../man/git-inlay.1")
	if err != nil {
		t.Fatal(err)
	}
	for verb := range implemented {
		if verb == "hook" || verb == "help" {
			continue
		}
		if !strings.Contains(string(page), `\fBgit inlay `+verb+`\fR`) {
			t.Errorf("man/git-inlay.1 has no synopsis line for %q", verb)
		}
	}
}
