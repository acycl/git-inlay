package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/acycl/git-inlay/internal/hooks"
)

// The manual page, the command reference and the README's command table
// are written by hand, so the tests in this file hold them to the code:
// the verbs dispatch routes, the options the usage text names and the
// verbs register, the hook manager snippets, the Go version, and the links
// between the documents. The plugin's manifests are held to each other.

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

// TestVerbHelpDescribesEveryVerb checks that -h after a verb has something
// to say about it beyond the usage line, which it takes from the summary
// of every command.
func TestVerbHelpDescribesEveryVerb(t *testing.T) {
	for verb := range synopses {
		_, about, _ := strings.Cut(verbHelp(verb), "\n\n")
		if first, _, _ := strings.Cut(about, "\n"); strings.TrimSpace(first) == "" {
			t.Errorf("git inlay %s -h describes nothing:\n%s", verb, verbHelp(verb))
		}
	}
}

// TestSynopsesMatchTheDocuments checks that the usage line each verb
// prints is the synopsis line the command reference and the manual page
// give it.
func TestSynopsesMatchTheDocuments(t *testing.T) {
	reference := readDoc(t, "docs/commands.md")
	page := readDoc(t, "man/git-inlay.1")
	page = strings.NewReplacer(`\-`, "-", `\fB`, "", `\fR`, "", `\fI`, "").Replace(page)
	for _, verb := range documentedVerbs() {
		if _, ok := synopses[verb]; !ok {
			t.Errorf("no synopsis for %q", verb)
			continue
		}
		line := strings.TrimPrefix(usageOf(verb), "usage: ")
		if !strings.Contains(reference, "\n"+line+"\n") {
			t.Errorf("docs/commands.md has no line %q", line)
		}
		if !strings.Contains(page, "\n"+line+"\n") {
			t.Errorf("man/git-inlay.1 has no synopsis line %q", line)
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

// TestUsageNamesEveryFlag checks that the usage text names exactly the
// options the verbs register, so that one added to the code cannot miss the
// usage text, which the tests above then hold the documents to, and one
// dropped from the code cannot linger there.
func TestUsageNamesEveryFlag(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	registered := regexp.MustCompile(`\bfs\.(?:Bool|String)\("([a-z][a-z-]*)"`)
	var flags []string
	for _, src := range sources {
		if strings.HasSuffix(src, "_test.go") {
			continue
		}
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range registered.FindAllStringSubmatch(string(data), -1) {
			flags = append(flags, "--"+m[1])
		}
	}
	slices.Sort(flags)
	flags = slices.Compact(flags)
	// --help and --version are handled by dispatch, not by a flag set.
	named := slices.DeleteFunc(usageFlags(), func(f string) bool { return f == "--help" || f == "--version" })
	if !slices.Equal(flags, named) {
		t.Errorf("the verbs register %v, but the usage text names %v", flags, named)
	}
}

// TestGuardsGuideCarriesSnippets checks that the hook manager snippets in
// docs/guards.md are the ones hooks install prints.
func TestGuardsGuideCarriesSnippets(t *testing.T) {
	page := readDoc(t, "docs/guards.md")
	for _, manager := range []string{"husky", "lefthook", "pre-commit"} {
		for line := range strings.Lines(hooks.Snippet(manager)) {
			// The configuration lines are the indented ones.
			if !strings.HasPrefix(line, "  ") {
				continue
			}
			if want := strings.TrimSpace(line); !strings.Contains(page, want) {
				t.Errorf("docs/guards.md lacks this line of the %s snippet: %s", manager, want)
			}
		}
	}
}

// TestDocsNameTheGoFloor checks that the Go version the documents ask for
// is the one go.mod sets.
func TestDocsNameTheGoFloor(t *testing.T) {
	m := regexp.MustCompile(`(?m)^go (\d+\.\d+)`).FindStringSubmatch(readDoc(t, "go.mod"))
	if m == nil {
		t.Fatal("go.mod has no go directive")
	}
	for _, doc := range []string{"README.md", "docs/getting-started.md"} {
		for _, named := range regexp.MustCompile(`Go (\d+\.\d+)`).FindAllStringSubmatch(readDoc(t, doc), -1) {
			if named[1] != m[1] {
				t.Errorf("%s names Go %s, but go.mod sets %s", doc, named[1], m[1])
			}
		}
	}
}

// headingSlug renders a Markdown heading as the anchor GitHub gives it:
// lower case, punctuation dropped, spaces as hyphens.
func headingSlug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(heading)) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TestDocLinksResolve checks every relative link in the README and the
// guides: the file exists, and so does the heading an anchor names.
func TestDocLinksResolve(t *testing.T) {
	docs, err := filepath.Glob("../../docs/*.md")
	if err != nil {
		t.Fatal(err)
	}
	docs = append(docs, "../../README.md")
	anchors := func(path string) map[string]bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		out := map[string]bool{}
		fenced := false
		for line := range strings.Lines(string(data)) {
			if strings.HasPrefix(line, "```") {
				fenced = !fenced
			}
			if heading := strings.TrimLeft(line, "#"); !fenced && heading != line && strings.HasPrefix(heading, " ") {
				out[headingSlug(heading)] = true
			}
		}
		return out
	}
	link := regexp.MustCompile(`\]\(([^)\s]+)\)`)
	for _, doc := range docs {
		data, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range link.FindAllStringSubmatch(string(data), -1) {
			target, anchor, _ := strings.Cut(m[1], "#")
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			path := doc
			if target != "" {
				path = filepath.Join(filepath.Dir(doc), filepath.FromSlash(target))
			}
			if _, err := os.Stat(path); err != nil {
				t.Errorf("%s links to %s, which does not exist", doc, m[1])
				continue
			}
			if anchor != "" && !anchors(path)[anchor] {
				t.Errorf("%s links to %s, but %s has no heading with that anchor", doc, m[1], path)
			}
		}
	}
}

// TestPluginManifests checks the Claude Code plugin's files, which only a
// machine with claude installed otherwise validates: the marketplace lists
// the plugin where it lives, under the name its own manifest gives, and its
// session-start hook calls a hook this binary has.
func TestPluginManifests(t *testing.T) {
	var marketplace struct {
		Plugins []struct{ Name, Source string }
	}
	if err := json.Unmarshal([]byte(readDoc(t, ".claude-plugin/marketplace.json")), &marketplace); err != nil {
		t.Fatal(err)
	}
	if len(marketplace.Plugins) != 1 {
		t.Fatalf("the marketplace lists %d plugins, want 1", len(marketplace.Plugins))
	}
	listed := marketplace.Plugins[0]
	dir := strings.TrimPrefix(listed.Source, "./")
	var plugin struct{ Name string }
	if err := json.Unmarshal([]byte(readDoc(t, dir+"/.claude-plugin/plugin.json")), &plugin); err != nil {
		t.Fatal(err)
	}
	if plugin.Name != listed.Name {
		t.Errorf("the marketplace lists the plugin as %q, its manifest as %q", listed.Name, plugin.Name)
	}
	var hooksFile struct {
		Hooks map[string][]struct {
			Hooks []struct{ Type, Command string }
		}
	}
	if err := json.Unmarshal([]byte(readDoc(t, dir+"/hooks/hooks.json")), &hooksFile); err != nil {
		t.Fatal(err)
	}
	start := hooksFile.Hooks["SessionStart"]
	if len(start) != 1 || len(start[0].Hooks) != 1 {
		t.Fatalf("hooks.json: SessionStart = %+v, want one command", start)
	}
	if got, want := start[0].Hooks[0].Command, "git-inlay hook session-start"; got != want {
		t.Errorf("hooks.json runs %q, want %q", got, want)
	}
	// The hook the plugin calls is one the binary answers without output
	// outside a repository, where a session may well start.
	var out, errOut strings.Builder
	if code := Run([]string{"hook", "session-start"}, Options{Dir: t.TempDir(), Stdout: &out, Stderr: &errOut}); code != 0 || out.Len() != 0 {
		t.Errorf("hook session-start outside a repository: exit %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
	if _, err := os.Stat("../../" + dir + "/skills/git-inlay/SKILL.md"); err != nil {
		t.Errorf("the plugin has no skill: %v", err)
	}
}
