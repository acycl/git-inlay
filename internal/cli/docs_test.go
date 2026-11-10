package cli

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The manual page, the command reference and the README's command table
// are written by hand, so the tests in this file hold them to the code:
// the verbs dispatch routes and the options the usage text names.

// documentedVerbs returns the verbs a user types, sorted: every verb
// dispatch routes apart from the hook family, which the shims call, and
// help.
func documentedVerbs() []string {
	var verbs []string
	for verb := range implemented {
		if verb != "hook" && verb != "help" {
			verbs = append(verbs, verb)
		}
	}
	slices.Sort(verbs)
	return verbs
}

// usageFlags returns the long options the usage text names, sorted and
// without repeats.
func usageFlags() []string {
	flags := regexp.MustCompile(`--[a-z][a-z-]*`).FindAllString(usage, -1)
	slices.Sort(flags)
	return slices.Compact(flags)
}

// hasFlag reports whether text names the option flag, and not merely a
// longer option that starts with it.
func hasFlag(text, flag string) bool {
	return regexp.MustCompile(regexp.QuoteMeta(flag) + `([^a-z-]|$)`).MatchString(text)
}

// readDoc returns the content of a document, given its path from the root
// of the repository.
func readDoc(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile("../../" + path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestManPageCoversUsage checks that the manual page has a synopsis line
// for every verb and names every option of the usage text.
func TestManPageCoversUsage(t *testing.T) {
	page := readDoc(t, "man/git-inlay.1")
	for _, verb := range documentedVerbs() {
		if !strings.Contains(page, `\fBgit inlay `+verb+`\fR`) {
			t.Errorf("man/git-inlay.1 has no synopsis line for %q", verb)
		}
	}
	// roff writes a hyphen as \-.
	plain := strings.ReplaceAll(page, `\-`, "-")
	for _, flag := range usageFlags() {
		if !hasFlag(plain, flag) {
			t.Errorf("man/git-inlay.1 does not mention %s", flag)
		}
	}
}

// TestCommandReferenceCoversUsage checks that docs/commands.md has a
// synopsis line for every verb and names every option of the usage text.
func TestCommandReferenceCoversUsage(t *testing.T) {
	page := readDoc(t, "docs/commands.md")
	for _, verb := range documentedVerbs() {
		if !regexp.MustCompile(`(?m)^git inlay ` + verb + `\b`).MatchString(page) {
			t.Errorf("docs/commands.md has no synopsis line for %q", verb)
		}
	}
	for _, flag := range usageFlags() {
		if !hasFlag(page, flag) {
			t.Errorf("docs/commands.md does not mention %s", flag)
		}
	}
}

// TestReadmeListsEveryVerb checks that the README's command table has a
// row for every verb. The table is a summary, so options are not held to
// it, and version is left to the command reference.
func TestReadmeListsEveryVerb(t *testing.T) {
	page := readDoc(t, "README.md")
	for _, verb := range documentedVerbs() {
		if verb == "version" {
			continue
		}
		if !regexp.MustCompile("(?m)^\\| `" + verb + `\b`).MatchString(page) {
			t.Errorf("README.md has no command table row for %q", verb)
		}
	}
}
