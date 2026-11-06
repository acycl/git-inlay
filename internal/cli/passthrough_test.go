package cli

import (
	"slices"
	"strings"
	"testing"
)

func TestTreeWide(t *testing.T) {
	t.Parallel()
	refused := []string{
		"clean -fdx", "clean -xf", "clean -dxf", "clean -f -X", "clean -n -x",
		"clean -fd -e !*", "clean -fd -e!*", "clean -fde !*", "clean -fd --exclude=!*", "clean -fd --exclude !*",
		"clean -fd --excl=!*", "clean -fd --ex !*", "clean -fd --e !*",
		"-c clean.requireForce=false clean -fdx", "--no-pager clean -X", "-C . stash --all",
		"--shallow-file /dev/null clean -fdx", "--git-dir x --work-tree y clean -fdx",
		"stash --all", "stash -a", "stash push -a", "stash push --all", "stash save -a",
		"stash --al", "stash push --a", "stash save --al",
		"stash -m all -a", "stash push -mall -a", "stash push --message all --all", "stash push --mess all --al",
	}
	for _, s := range refused {
		if _, ok := treeWide(strings.Fields(s)); !ok {
			t.Errorf("treeWide(%q) let it through", s)
		}
	}
	allowed := []string{
		"clean -fd", "clean -fdn", "clean -fd -e *.log", "clean -fd --exclude=*.log", "clean -fd -- -x",
		"clean -fde *.log", "clean -fd --ex *.log", "stash", "stash push -u", "stash -m all", "stash push -mall",
		"stash show -a", "stash list --all", "stash apply", "stash pop", "stash -- -a",
		"status --porcelain", "-c core.pager=cat log --all", "",
	}
	for _, s := range allowed {
		if verb, ok := treeWide(strings.Fields(s)); ok {
			t.Errorf("treeWide(%q) refused it as %q", s, verb)
		}
	}
}

func TestGitArgs(t *testing.T) {
	t.Parallel()
	opts, pos := gitArgs(strings.Fields("push -q -mall -a --message=x --message y -- -a"), "m", "--message")
	want := []gitOption{{"q", ""}, {"m", "all"}, {"a", ""}, {"--message", "x"}, {"--message", "y"}}
	if len(opts) != len(want) {
		t.Fatalf("options = %v, want %v", opts, want)
	}
	for i := range want {
		if opts[i] != want[i] {
			t.Errorf("option %d = %v, want %v", i, opts[i], want[i])
		}
	}
	if len(pos) != 1 || pos[0] != "push" {
		t.Errorf("positionals = %v, want [push]", pos)
	}
}

func TestSplitAlias(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"clean -fdx", []string{"clean", "-fdx"}},
		{"clean  -f\t-x", []string{"clean", "-f", "-x"}},
		{`log --format='%h %s'`, []string{"log", "--format=%h %s"}},
		{`commit -m "a b" --allow-empty`, []string{"commit", "-m", "a b", "--allow-empty"}},
		{`echo a\ b`, []string{"echo", "a b"}},
		{`echo "a\"b" '\n'`, []string{"echo", `a"b`, `\n`}},
		{`""`, []string{""}},
		{"", nil},
	} {
		got, ok := splitAlias(tc.in)
		if !ok || !slices.Equal(got, tc.want) {
			t.Errorf("splitAlias(%q) = %q, %v; want %q", tc.in, got, ok, tc.want)
		}
	}
	for _, bad := range []string{`clean "-x`, `clean '-x`, `clean -x\`} {
		if got, ok := splitAlias(bad); ok {
			t.Errorf("splitAlias(%q) = %q, want a rejection", bad, got)
		}
	}
}
