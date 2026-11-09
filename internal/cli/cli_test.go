package cli_test

// End-to-end scenarios from PLAN.md section 8, run through the built binary
// as "git inlay ..." against real git in temp directories.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/acycl/git-inlay/internal/gitx"
)

var (
	binDir   string // holds the git-inlay binary the tests run
	coverDir string // GOCOVERDIR for that binary, when coverage is wanted
)

// TestMain builds the binary the tests drive. With GOCOVERDIR set it builds
// for coverage of every package, and each run writes its counters there,
// which is the only way the end-to-end tests show up in coverage:
//
//	mkdir cov && GOCOVERDIR=$PWD/cov go test ./internal/cli && go tool covdata percent -i=cov
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "git-inlay-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	bin := filepath.Join(dir, "git-inlay")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	args := []string{"build", "-o", bin}
	if coverDir = os.Getenv("GOCOVERDIR"); coverDir != "" {
		// Each run's cwd is inside a fixture, where a relative path would
		// name nothing.
		if coverDir, err = filepath.Abs(coverDir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		args = append(args, "-cover", "-coverpkg=github.com/acycl/git-inlay/...")
	}
	build := exec.Command("go", append(args, "../../cmd/git-inlay")...)
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build git-inlay:", err)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	binDir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fixture is a host repository with a local bare remote and an isolated git
// environment that has git-inlay on PATH.
type fixture struct {
	t      *testing.T
	base   string
	root   string
	remote string
	env    []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	cfg := filepath.Join(home, "gitconfig")
	writeFile(t, cfg, "[user]\n\tname = test\n\temail = test@example.com\n[init]\n\tdefaultBranch = main\n")
	f := &fixture{t: t, base: base, root: filepath.Join(base, "host"), remote: filepath.Join(base, "host.git")}
	f.env = []string{
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=" + cfg,
		"GIT_CONFIG_NOSYSTEM=1",
		"LC_ALL=C",
		"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"TMPDIR=" + os.TempDir(),
	}
	if coverDir != "" {
		f.env = append(f.env, "GOCOVERDIR="+coverDir)
	}
	f.git(base, "init", "-q", "--bare", "-b", "main", f.remote)
	if err := os.Mkdir(f.root, 0o755); err != nil {
		t.Fatal(err)
	}
	f.git(f.root, "init", "-q", "-b", "main")
	f.write("README.md", "# host\n")
	f.git(f.root, "add", "README.md")
	f.git(f.root, "commit", "-q", "-m", "init")
	f.git(f.root, "remote", "add", "origin", f.remote)
	f.git(f.root, "push", "-q", "-u", "origin", "main")
	return f
}

// run executes a command and returns stdout, stderr and the exit status.
func (f *fixture) run(dir, name string, args ...string) (string, string, int) {
	f.t.Helper()
	return f.runWith(dir, nil, name, args...)
}

// runWith is run with extra environment variables appended.
func (f *fixture) runWith(dir string, extra []string, name string, args ...string) (string, string, int) {
	f.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(slices.Clone(f.env), extra...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		f.t.Fatalf("%s %s: %v", name, strings.Join(args, " "), err)
	}
	return strings.TrimRight(out.String(), "\n"), errOut.String(), code
}

func (f *fixture) git(dir string, args ...string) string {
	f.t.Helper()
	out, errOut, code := f.run(dir, "git", args...)
	if code != 0 {
		f.t.Fatalf("git %s: exit %d\n%s", strings.Join(args, " "), code, errOut)
	}
	return out
}

// gitFails runs git expecting failure and returns its stderr.
func (f *fixture) gitFails(dir string, args ...string) string {
	f.t.Helper()
	out, errOut, code := f.run(dir, "git", args...)
	if code == 0 {
		f.t.Fatalf("git %s succeeded, want failure\n%s", strings.Join(args, " "), out)
	}
	return errOut
}

// inlay runs "git inlay ..." so git's own subcommand lookup is exercised.
func (f *fixture) inlay(dir string, args ...string) (string, string, int) {
	f.t.Helper()
	return f.run(dir, "git", append([]string{"inlay"}, args...)...)
}

func (f *fixture) inlayOK(dir string, args ...string) string {
	f.t.Helper()
	out, errOut, code := f.inlay(dir, args...)
	if code != 0 {
		f.t.Fatalf("git inlay %s: exit %d\n%s", strings.Join(args, " "), code, errOut)
	}
	return out
}

// inlayFails runs git inlay expecting the given exit status and returns stderr.
func (f *fixture) inlayFails(dir string, want int, args ...string) string {
	f.t.Helper()
	out, errOut, code := f.inlay(dir, args...)
	if code != want {
		f.t.Fatalf("git inlay %s: exit %d, want %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, want, out, errOut)
	}
	return errOut
}

func (f *fixture) write(rel, content string) {
	f.t.Helper()
	writeFile(f.t, filepath.Join(f.root, filepath.FromSlash(rel)), content)
}

func (f *fixture) read(rel string) string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.root, filepath.FromSlash(rel)))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

func (f *fixture) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(f.root, filepath.FromSlash(rel)))
	return err == nil
}

// bare creates a local bare repository to serve as an inlay remote.
func (f *fixture) bare(name string) string {
	f.t.Helper()
	p := filepath.Join(f.base, name+".git")
	f.git(f.base, "init", "-q", "--bare", "-b", "main", p)
	return p
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func contains(t *testing.T, got, want, what string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%s does not mention %q:\n%s", what, want, got)
	}
}

// Scenario 1: init, claim, commit; the host stays clean and add -A stages nothing.
func TestInitClaimCommit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	contains(t, f.inlayOK(f.root, "init", "claude"), "initialized inlay claude", "init output")
	f.write(".claude/settings.json", "{}\n")
	contains(t, f.inlayOK(f.root, "claim", "claude", ".claude"), "claimed .claude/ for inlay claude", "claim output")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "settings")

	if got := f.git(f.root, "status", "--porcelain"); got != "" {
		t.Fatalf("host status = %q, want clean", got)
	}
	f.git(f.root, "add", "-A")
	if got := f.git(f.root, "diff", "--cached", "--name-only"); got != "" {
		t.Fatalf("host staged %q after add -A", got)
	}
	if got, want := f.inlayOK(f.root, "claude", "ls-files"), ".claude/settings.json\n.git-inlay/claude.paths"; got != want {
		t.Fatalf("inlay ls-files = %q, want %q", got, want)
	}
	if got := f.inlayOK(f.root, "ls"); got != "claude\t.claude/" {
		t.Fatalf("ls = %q", got)
	}
	want := ".claude/settings.json\tclaude\nREADME.md\thost\nnothing\tnone"
	if got := f.inlayOK(f.root, "which", ".claude/settings.json", "README.md", "nothing"); got != want {
		t.Fatalf("which = %q, want %q", got, want)
	}
	excl := f.read(".git/info/exclude")
	contains(t, excl, "/.git-inlay\n", "host exclude")
	contains(t, excl, "/.claude\n", "host exclude")
	if got := f.read(".git-inlay/claude.paths"); !strings.HasSuffix(got, "\n.claude/\n") {
		t.Fatalf("manifest = %q", got)
	}

	// A new file under the claimed directory belongs to the inlay, not the host.
	f.write(".claude/skills/x.md", "x\n")
	if got := f.inlayOK(f.root, "claude", "status", "--porcelain"); got != "?? .claude/skills/" {
		t.Fatalf("inlay status = %q", got)
	}
	if got := f.git(f.root, "status", "--porcelain"); got != "" {
		t.Fatalf("host status after new inlay file = %q", got)
	}
	f.inlayOK(f.root, "hooks", "install")
	contains(t, f.inlayOK(f.root, "check"), "ok:", "check")
}

// Scenario 4: claims are refused for host-tracked paths and across inlays.
func TestClaimRefusals(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write("docs/README.md", "d\n")
	f.git(f.root, "add", "docs")
	f.git(f.root, "commit", "-q", "-m", "docs")
	f.inlayOK(f.root, "init", "team")
	f.inlayOK(f.root, "init", "me")

	contains(t, f.inlayFails(f.root, 1, "claim", "team", "README.md"), "tracked by the host", "claiming a tracked file")
	contains(t, f.inlayFails(f.root, 1, "claim", "team", "docs"), "tracked by the host", "claiming a tracked directory")
	// Magit exports GIT_LITERAL_PATHSPECS, which would turn the literal
	// pathspec that asks the host into a file name that matches nothing.
	if _, errOut, code := f.runWith(f.root, []string{"GIT_LITERAL_PATHSPECS=1"}, "git", "inlay", "claim", "team", "README.md"); code != 1 || !strings.Contains(errOut, "tracked by the host") {
		t.Fatalf("claim of a tracked file under GIT_LITERAL_PATHSPECS: exit %d, stderr %q", code, errOut)
	}
	// Claims are vetted as a batch, but the first path that fails still
	// decides the error.
	contains(t, f.inlayFails(f.root, 1, "claim", "team", "fresh/", "README.md", "../outside"), "README.md is tracked by the host", "first failure in a batch")
	contains(t, f.inlayFails(f.root, 1, "claim", "team", "fresh/", "../outside", "README.md"), "is outside the repository", "a resolve failure first in a batch")
	f.inlayOK(f.root, "claim", "team", "private/notes/")
	contains(t, f.inlayFails(f.root, 1, "claim", "me", "private/notes/today.md"), "already owned by inlay team", "claiming under another inlay")
	contains(t, f.inlayFails(f.root, 1, "claim", "me", "private/"), "would contain private/notes/, which inlay team claims", "claiming above another inlay")
	_, errOut, code := f.inlay(f.root, "claim", "team", "private/notes/deep.md")
	if code != 0 || !strings.Contains(errOut, "already covered") {
		t.Fatalf("redundant claim: exit %d, stderr %q", code, errOut)
	}
	contains(t, f.inlayFails(f.root, 2, "init", "status"), "cannot name an inlay", "reserved name")
	contains(t, f.inlayFails(f.root, 2, "init", "sync"), "reserved for a future git-inlay command", "held name")
	f.inlayFails(f.root, 2, "init", "Bad_Name")
	f.inlayOK(f.root, "hooks", "install")
	contains(t, f.inlayOK(f.root, "check"), "ok: 2 inlay(s), 1 claim(s)", "check")
}

// A claim's kind is fixed when it is made, so a path claimed before it
// exists is a file claim, with a warning, and check reports it once the
// path turns out to be a directory; claiming it again changes the kind.
func TestClaimKindFollowsDisk(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "claude")
	f.inlayOK(f.root, "hooks", "install")
	_, errOut, code := f.inlay(f.root, "claim", "claude", ".claude/skills/internal", "later/")
	if code != 0 || !strings.Contains(errOut, ".claude/skills/internal does not exist yet and is claimed as a file") {
		t.Fatalf("claim of a missing path: exit %d, stderr %q", code, errOut)
	}
	if strings.Contains(errOut, "later") {
		t.Fatalf("a trailing slash still drew the warning: %q", errOut)
	}
	f.write(".claude/skills/internal/x.md", "x\n")
	contains(t, f.inlayFails(f.root, 1, "check"), ".claude/skills/internal: is claimed as a file but is a directory", "check on a file claim that is a directory")
	contains(t, f.inlayOK(f.root, "which", ".claude/skills/internal/x.md"), "\tnone", "which before the claim is fixed")

	_, errOut, code = f.inlay(f.root, "claim", "claude", ".claude/skills/internal")
	if code != 0 || !strings.Contains(errOut, "now covers the earlier claim") {
		t.Fatalf("claiming the directory again: exit %d, stderr %q", code, errOut)
	}
	contains(t, f.inlayOK(f.root, "which", ".claude/skills/internal/x.md"), "\tclaude", "which after the claim is fixed")
	contains(t, f.inlayOK(f.root, "check"), "ok:", "check after the claim is fixed")
}

// Every verb reads its arguments the same way: "--" ends the options, an
// unknown option is a usage error rather than a path, and --help prints
// the usage instead of acting.
func TestArgumentsAndHelp(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "claude")
	f.write("foo.md", "x\n")
	f.write("-dash.md", "x\n")
	f.inlayOK(f.root, "claim", "claude", "--", "foo.md", "-dash.md")
	if got, want := f.inlayOK(f.root, "ls"), "claude\t-dash.md\nclaude\tfoo.md"; got != want {
		t.Fatalf("ls after claim -- = %q, want %q", got, want)
	}
	f.inlayFails(f.root, 2, "claim", "claude", "-dash.md")
	f.inlayOK(f.root, "unclaim", "claude", "--", "-dash.md")
	for _, args := range [][]string{
		{"claim", "claude", "--help"}, {"unclaim", "claude", "-h"}, {"ls", "--help"},
		{"clone", "--help"}, {"restore", "-h"}, {"hooks", "--help"}, {"help"},
	} {
		contains(t, f.inlayOK(f.root, args...), "usage: git inlay <command>", strings.Join(args, " "))
	}
	if got, want := f.inlayOK(f.root, "ls"), "claude\tfoo.md"; got != want {
		t.Fatalf("ls after the help requests = %q, want %q", got, want)
	}
}

// Host commits made while the host still tracked a path, pushed after the
// path moved into an inlay, cannot be told from a leak, so the refusal
// says how to check them and get them out.
func TestPushOfCommitsThatPredateTheClaim(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write("CLAUDE.md", "one\n")
	f.git(f.root, "add", "CLAUDE.md")
	f.git(f.root, "commit", "-q", "-m", "add CLAUDE.md")
	f.git(f.root, "push", "-q", "origin", "main")
	f.write("CLAUDE.md", "two\n")
	f.git(f.root, "commit", "-q", "-am", "edit CLAUDE.md")
	f.git(f.root, "rm", "-q", "--cached", "CLAUDE.md")
	f.git(f.root, "commit", "-q", "-m", "move CLAUDE.md to an inlay")
	f.inlayOK(f.root, "init", "claude")
	f.inlayOK(f.root, "claim", "claude", "CLAUDE.md")
	f.inlayOK(f.root, "hooks", "install")
	errOut := f.gitFails(f.root, "push", "-q", "origin", "main")
	contains(t, errOut, "CLAUDE.md: belongs to inlay claude", "pre-push")
	contains(t, errOut, `made before the path was claimed, while the host still tracked it, they hold nothing of the inlay's: check with git log -p <remote>..HEAD -- ":(top,literal)CLAUDE.md"`, "pre-push fix")
}

// An interrupt from the terminal reaches git-inlay as well as git. It must
// outlive it, leaving the decision to git, and still answer with git's
// exit status.
func TestPassthroughOutlivesInterrupt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Process.Signal cannot send an interrupt on Windows")
	}
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "claude")
	// A shell alias stands in for a git command that is still running; the
	// interrupt goes to git-inlay alone.
	cmd := exec.Command(filepath.Join(binDir, "git-inlay"), "claude", "-c", "alias.wait=!sleep 3; exit 7", "wait")
	cmd.Dir, cmd.Env = f.root, f.env
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	var exitErr *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("passthrough after an interrupt: %v, want git's exit status 7", err)
	}
}

// A teammate's commit that carries a path outside the inlay's claims is
// written over the host's file by a pull, since git overwrites ignored
// files silently. Passthrough says so once; check keeps reporting it.
func TestPullOfPathOutsideClaimsWarns(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	private := f.bare("claude-private")
	f.inlayOK(f.root, "init", "claude", "--remote", private)
	f.inlayOK(f.root, "claude", "push", "-q", "-u", "origin", "main")

	// The teammate, with no guard in the way, commits the host's README.
	mate := filepath.Join(f.base, "mate")
	f.git(f.base, "clone", "-q", private, mate)
	writeFile(t, filepath.Join(mate, "README.md"), "# not the host's\n")
	f.git(mate, "add", "README.md")
	f.git(mate, "commit", "-q", "-m", "stray")
	f.git(mate, "push", "-q", "origin", "main")

	_, errOut, code := f.inlay(f.root, "claude", "pull", "-q")
	if code != 0 {
		t.Fatalf("pull: exit %d\n%s", code, errOut)
	}
	contains(t, errOut, "inlay claude now tracks 1 path(s) outside its claims", "pull warning")
	contains(t, errOut, "README.md: tracked by inlay claude but not under any of its claims", "pull warning")
	if got := f.git(f.root, "status", "--porcelain"); got != " M README.md" {
		t.Fatalf("host status after the pull = %q", got)
	}
	if _, errOut, _ := f.inlay(f.root, "claude", "status", "--porcelain"); strings.Contains(errOut, "outside its claims") {
		t.Fatalf("the warning repeated on the next command: %q", errOut)
	}
}

// Git skips a hook that is not executable, so a shim that lost the bit is
// no guard: status and check say so, and hooks install puts it back.
func TestShimWithoutExecutableBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no executable bit")
	}
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "claude")
	f.inlayOK(f.root, "hooks", "install")
	hostShim := filepath.Join(f.root, ".git", "hooks", "pre-commit")
	inlayShim := filepath.Join(f.root, ".git", "inlays", "main", "claude", "hooks", "pre-push")
	for _, p := range []string{hostShim, inlayShim} {
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	errOut := f.inlayFails(f.root, 1, "check")
	contains(t, errOut, "host pre-commit hook: is not executable", "check with an inert host shim")
	contains(t, errOut, "claude: has a config that does not name this host", "check with an inert inlay shim")
	contains(t, f.inlayOK(f.root, "hooks", "status"), "inert", "hooks status")

	contains(t, f.inlayOK(f.root, "hooks", "install"), "pre-commit  installed", "hooks install")
	for _, p := range []string{hostShim, inlayShim} {
		if fi, err := os.Stat(p); err != nil || fi.Mode()&0o111 == 0 {
			t.Fatalf("%s is still not executable (%v)", p, err)
		}
	}
	contains(t, f.inlayOK(f.root, "check"), "ok:", "check after hooks install")
}

// init guards a new inlay from its first commit: it installs the shims
// where the hooks directory is free, leaves a hook of the host's own
// alone, and installs nothing with --no-hooks.
func TestInitInstallsFreeGuards(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	foreign := "#!/bin/sh\nexit 0\n"
	f.write(".git/hooks/pre-push", foreign)
	out, errOut, code := f.inlay(f.root, "init", "claude")
	if code != 0 {
		t.Fatalf("init: exit %d\n%s", code, errOut)
	}
	contains(t, out, "installed the host's pre-commit guard(s)", "init output")
	if got := f.read(".git/hooks/pre-push"); got != foreign {
		t.Fatalf("init rewrote the host's own pre-push hook:\n%s", got)
	}
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	f.git(f.root, "add", "-f", ".claude/settings.json")
	contains(t, f.gitFails(f.root, "commit", "-q", "-m", "leak"), "belongs to inlay claude", "pre-commit after init")

	g := newFixture(t)
	out, errOut, code = g.inlay(g.root, "init", "claude", "--no-hooks")
	if code != 0 || strings.Contains(out, "installed the host's") {
		t.Fatalf("init --no-hooks: exit %d, stdout %q", code, out)
	}
	contains(t, errOut, "host leak guards are not active", "init --no-hooks")
	if g.exists(".git/hooks/pre-commit") {
		t.Fatal("init --no-hooks installed a shim")
	}
}

// An inlay whose name is a verb could never be reached by passthrough, so
// clone refuses one.
func TestCloneRefusesVerbName(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	private := f.bare("status-private")
	src := filepath.Join(f.base, "src")
	writeFile(t, filepath.Join(src, ".git-inlay", "status.paths"), "notes.md\n")
	writeFile(t, filepath.Join(src, "notes.md"), "n\n")
	f.git(src, "init", "-q", "-b", "main")
	f.git(src, "add", "-A")
	f.git(src, "commit", "-q", "-m", "inlay")
	f.git(src, "push", "-q", private, "main")
	contains(t, f.inlayFails(f.root, 1, "clone", private), `"status" is a git-inlay command and cannot name an inlay`, "clone of an inlay named after a verb")
	if f.exists("notes.md") {
		t.Fatal("the refused clone checked out files")
	}
}

// Scenario 10: passthrough runs git against the inlay and refuses bad names.
func TestPassthrough(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "claude")
	got := f.inlayOK(f.root, "claude", "rev-parse", "--git-dir")
	if !strings.HasSuffix(filepath.ToSlash(got), ".git/inlays/main/claude") {
		t.Fatalf("passthrough git dir = %q", got)
	}
	f.inlayFails(f.root, 2, "claude")
	contains(t, f.inlayFails(f.root, 1, "nope", "status"), "no inlay named", "unknown inlay")
	f.inlayFails(f.root, 2, "Nope", "status")

	// Relative paths resolve from the directory the command runs in.
	f.write("sub/.keep", "")
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	sub := filepath.Join(f.root, "sub")
	contains(t, f.inlayOK(sub, "claude", "status", "--porcelain", "--", "../.claude"), "settings.json", "status from a subdirectory")

	// Git's own exit status comes back unchanged.
	if _, _, code := f.inlay(f.root, "claude", "rev-parse", "--verify", "-q", "refs/heads/nonexistent"); code != 1 {
		t.Fatalf("passthrough exit = %d, want git's 1", code)
	}
}

// Scenario 3: clone onto a fresh host clone restores the files and the exclude block.
func TestCloneOntoFreshHost(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	private := f.bare("claude-private")
	f.inlayOK(f.root, "init", "claude", "--remote", private)
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "settings")
	f.inlayOK(f.root, "claude", "push", "-q", "-u", "origin", "main")

	host2 := filepath.Join(f.base, "host2")
	f.git(f.base, "clone", "-q", f.remote, host2)
	contains(t, f.inlayOK(host2, "clone", private), "attached inlay claude", "clone output")
	if b, err := os.ReadFile(filepath.Join(host2, ".claude", "settings.json")); err != nil || string(b) != "{}\n" {
		t.Fatalf("settings on host2 = %q, %v", b, err)
	}
	if got := f.git(host2, "status", "--porcelain"); got != "" {
		t.Fatalf("host2 status = %q", got)
	}
	excl, _ := os.ReadFile(filepath.Join(host2, ".git", "info", "exclude"))
	contains(t, string(excl), "/.claude\n", "host2 exclude")
	if got := f.inlayOK(host2, "ls"); got != "claude\t.claude/" {
		t.Fatalf("host2 ls = %q", got)
	}
	contains(t, f.inlayFails(host2, 1, "clone", private), "already attached", "second clone")

	host3 := filepath.Join(f.base, "host3")
	f.git(f.base, "clone", "-q", f.remote, host3)
	contains(t, f.inlayFails(host3, 1, "clone", private, "other"), `is the inlay "claude"`, "clone with the wrong name")
	writeFile(t, filepath.Join(host3, ".claude", "settings.json"), "local\n")
	contains(t, f.inlayFails(host3, 1, "clone", private), "would be overwritten", "clone over an untracked file")
	if _, err := os.Stat(filepath.Join(host3, ".git", "inlays", "main", "claude")); err == nil {
		t.Fatal("a failed clone left the inlay attached")
	}
}

// A claim that arrives through a pull applies as soon as the pull returns:
// the host ignores the new files and both exclude files are current, with
// no further git-inlay command. An unclaim travels the same way.
func TestPulledClaimsApplyAtOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	private := f.bare("team")
	f.inlayOK(f.root, "init", "team", "--remote", private)
	f.write("CLAUDE.local.md", "notes\n")
	f.inlayOK(f.root, "claim", "team", "CLAUDE.local.md")
	f.inlayOK(f.root, "team", "commit", "-q", "-m", "claims")
	f.inlayOK(f.root, "team", "push", "-q", "-u", "origin", "main")
	f.inlayOK(f.root, "hooks", "install")

	host2 := filepath.Join(f.base, "host2")
	f.git(f.base, "clone", "-q", f.remote, host2)
	f.inlayOK(host2, "clone", private)
	writeFile(t, filepath.Join(host2, "docs", "private", "plan.md"), "plan\n")
	f.inlayOK(host2, "claim", "team", "docs/private")
	f.inlayOK(host2, "team", "commit", "-q", "-m", "claim docs/private")
	f.inlayOK(host2, "team", "push", "-q")

	if _, errOut, code := f.inlay(f.root, "team", "pull", "-q"); code != 0 || errOut != "" {
		t.Fatalf("pulling a sound claim: exit %d, stderr %q", code, errOut)
	}
	if got := f.read("docs/private/plan.md"); got != "plan\n" {
		t.Fatalf("pulled file = %q", got)
	}
	if got := f.git(f.root, "status", "--porcelain", "--untracked-files=all"); got != "" {
		t.Fatalf("host status right after pulling a claim = %q", got)
	}
	contains(t, f.inlayOK(f.root, "check"), "ok:", "check right after pulling a claim")

	f.inlayOK(host2, "unclaim", "team", "docs/private")
	f.inlayOK(host2, "team", "commit", "-q", "-m", "unclaim docs/private")
	f.inlayOK(host2, "team", "push", "-q")
	f.inlayOK(f.root, "team", "pull", "-q")
	if strings.Contains(f.read(".git/info/exclude"), "/docs/private") {
		t.Fatalf("the managed block still hides an unclaimed path:\n%s", f.read(".git/info/exclude"))
	}
	contains(t, f.inlayOK(f.root, "check"), "ok:", "check right after pulling an unclaim")
}

// A teammate untracks a host file and claims it; pulling the inlay before
// the host's untracking commit claims a path the host still tracks, which
// the pull warns about at once with check's fix.
func TestPulledClaimOnHostTrackedPath(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	private := f.bare("team")
	f.inlayOK(f.root, "init", "team", "--remote", private)
	f.inlayOK(f.root, "team", "push", "-q", "-u", "origin", "main")
	f.write("shared.md", "host\n")
	f.git(f.root, "add", "shared.md")
	f.git(f.root, "commit", "-q", "-m", "shared.md")
	f.git(f.root, "push", "-q", "origin", "main")
	f.inlayOK(f.root, "hooks", "install")

	host2 := filepath.Join(f.base, "host2")
	f.git(f.base, "clone", "-q", f.remote, host2)
	f.inlayOK(host2, "clone", private)
	f.git(host2, "rm", "-q", "--cached", "shared.md")
	f.git(host2, "commit", "-q", "-m", "untrack shared.md")
	f.git(host2, "push", "-q", "origin", "main")
	writeFile(t, filepath.Join(host2, "shared.md"), "private\n")
	f.inlayOK(host2, "claim", "team", "shared.md")
	f.inlayOK(host2, "team", "commit", "-q", "-m", "claim shared.md")
	f.inlayOK(host2, "team", "push", "-q")

	_, errOut, code := f.inlay(f.root, "team", "pull", "-q")
	if code != 0 {
		t.Fatalf("pull: exit %d\n%s", code, errOut)
	}
	contains(t, errOut, "the claims of inlay team changed, and 1 path(s) are out of place", "pull warning")
	contains(t, errOut, "shared.md: tracked by the host but claimed by inlay team", "pull warning")
	contains(t, errOut, `fix: git rm --cached -- "shared.md" && git commit`, "pull warning")
	// From the inlay's side the host's copy is an ignored file, which git
	// replaces without asking; that is what makes the warning matter.
	if f.read("shared.md") != "private\n" {
		t.Fatal("git kept the host's copy; the scenario no longer tests what it should")
	}
	if got := f.git(f.root, "status", "--porcelain"); got != " M shared.md" {
		t.Fatalf("host status after the inlay pull = %q", got)
	}

	// While that problem stands, a pull that changes no claim vets
	// nothing and says nothing.
	writeFile(t, filepath.Join(host2, "shared.md"), "private, edited\n")
	f.inlayOK(host2, "team", "commit", "-q", "-a", "-m", "edit")
	f.inlayOK(host2, "team", "push", "-q")
	if _, errOut, code := f.inlay(f.root, "team", "pull", "-q"); code != 0 || errOut != "" {
		t.Fatalf("pulling an edit: exit %d, stderr %q", code, errOut)
	}

	// Nor does another inlay's claim change repeat team's problem.
	f.inlayOK(f.root, "init", "local")
	f.write("drafts/a.md", "a\n")
	f.inlayOK(f.root, "claim", "local", "drafts")
	f.inlayOK(f.root, "local", "commit", "-q", "-m", "drafts")
	for _, rev := range []string{"HEAD~1", "HEAD"} {
		if _, errOut, code := f.inlay(f.root, "local", "checkout", "-q", rev, "--", ".git-inlay/local.paths"); code != 0 || errOut != "" {
			t.Fatalf("checking out local's manifest at %s: exit %d, stderr %q", rev, code, errOut)
		}
	}

	// The fix and the teammate's commit delete the same entry, so the host
	// pull merges cleanly and leaves the inlay's file in place.
	f.git(f.root, "rm", "-q", "--cached", "shared.md")
	f.git(f.root, "commit", "-q", "-m", "untrack shared.md here too")
	f.git(f.root, "pull", "-q", "--no-rebase", "origin", "main")
	if got := f.read("shared.md"); got != "private, edited\n" {
		t.Fatalf("shared.md after the host pull = %q", got)
	}
	contains(t, f.inlayOK(f.root, "check"), "ok:", "check after the fix")
}

// A passthrough command that leaves a manifest which does not parse fails
// the command when git succeeded, and only warns when git failed, whose
// exit status then comes back unchanged.
func TestPassthroughSyncFailure(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "team")
	manifest := filepath.ToSlash(filepath.Join(f.root, ".git-inlay", "team.paths"))
	good := f.read(".git-inlay/team.paths")
	breakIt := `alias.breakit=!printf 'a//b\n' >> "` + manifest + `"`

	errOut := f.inlayFails(f.root, 1, "team", "-c", breakIt, "breakit")
	contains(t, errOut, "git succeeded, but the exclude files were not brought up to date", "sync failure after git succeeded")
	contains(t, errOut, "empty path segment", "sync failure after git succeeded")

	f.write(".git-inlay/team.paths", good)
	errOut = f.inlayFails(f.root, 5, "team", "-c", breakIt+"; exit 5", "breakit")
	contains(t, errOut, "warning: the exclude files were not brought up to date", "sync failure after git failed")
	contains(t, errOut, "empty path segment", "sync failure after git failed")
}

// Scenario 5: an inlay remote that resolves to a host remote is refused.
func TestRemoteGuard(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.git(f.root, "remote", "add", "public", "https://github.com/acme/app.git")
	for _, u := range []string{"git@github.com:acme/app.git", "ssh://git@github.com/acme/App", "https://github.com/ACME/app/", f.remote} {
		contains(t, f.inlayFails(f.root, 1, "init", "x", "--remote", u), "host's remote", "init --remote "+u)
	}
	private := f.bare("x-private")
	f.inlayOK(f.root, "init", "x", "--remote", private)

	// Pointing the inlay at the host's remote afterwards is caught at push time and by check.
	f.inlayOK(f.root, "x", "remote", "set-url", "origin", f.remote)
	_, errOut, code := f.inlay(f.root, "x", "push", "-q", "origin", "main")
	if code == 0 {
		t.Fatal("push of an inlay to the host's remote succeeded")
	}
	contains(t, errOut, "refusing to push inlay x", "inlay pre-push")
	contains(t, f.inlayFails(f.root, 1, "check"), "host's remote", "check")
}

// Scenario 2: the host hooks block a forced add at commit and at push,
// including after a fast-forward merge that runs no pre-commit hook.
func TestLeakGuards(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "claude")
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "settings")
	contains(t, f.inlayOK(f.root, "hooks", "install"), "installed", "hooks install")
	for _, h := range []string{"pre-commit", "pre-push"} {
		if !f.exists(".git/hooks/" + h) {
			t.Fatalf("%s hook not installed", h)
		}
	}

	f.git(f.root, "add", "-f", ".claude/settings.json")
	errOut := f.gitFails(f.root, "commit", "-q", "-m", "leak")
	contains(t, errOut, ".claude/settings.json: belongs to inlay claude", "pre-commit")
	contains(t, errOut, `git inlay claude add -- ":(top,literal).claude/settings.json" && git inlay claude commit -m`, "pre-commit fix")

	f.git(f.root, "commit", "-q", "--no-verify", "-m", "leak")
	contains(t, f.gitFails(f.root, "push", "-q", "origin", "main"), ".claude/settings.json: belongs to inlay claude", "pre-push")
	contains(t, f.inlayFails(f.root, 1, "check"), "tracked by the host but claimed by inlay claude", "check")

	f.git(f.root, "reset", "-q", "--hard", "origin/main")
	f.inlayOK(f.root, "claude", "checkout", "--", ".")
	if f.read(".claude/settings.json") != "{}\n" {
		t.Fatal("inlay checkout did not restore the file")
	}

	f.git(f.root, "checkout", "-q", "-b", "feat")
	f.git(f.root, "add", "-f", ".claude/settings.json")
	f.git(f.root, "commit", "-q", "--no-verify", "-m", "leak on branch")
	f.git(f.root, "checkout", "-q", "main")
	f.git(f.root, "merge", "-q", "--ff-only", "feat")
	contains(t, f.gitFails(f.root, "push", "-q", "origin", "main"), ".claude/settings.json", "pre-push after fast-forward")

	f.git(f.root, "reset", "-q", "--hard", "origin/main")
	f.inlayOK(f.root, "claude", "checkout", "--", ".")
	f.write("CHANGELOG.md", "x\n")
	f.git(f.root, "add", "CHANGELOG.md")
	f.git(f.root, "commit", "-q", "-m", "clean")
	f.git(f.root, "push", "-q", "origin", "main:clean-branch")

	contains(t, f.inlayOK(f.root, "hooks", "uninstall"), "absent", "hooks uninstall")
	if f.exists(".git/hooks/pre-push") {
		t.Fatal("pre-push shim survived uninstall")
	}
}

// configHooks reports whether git runs config-defined hooks (git 2.54).
func configHooks(t *testing.T) bool {
	t.Helper()
	ok, err := gitx.Runner{}.AtLeast(2, 54)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// A worktree added with relative paths (git 2.48) is found and not taken
// for gone, and its inlays' manifests are read from its tree; once its
// directory is deleted it is gone.
func TestWorktreeRelativePaths(t *testing.T) {
	t.Parallel()
	if ok, err := (gitx.Runner{}).AtLeast(2, 48); err != nil || !ok {
		t.Skip("relative worktree paths need git 2.48 or newer")
	}
	f := newFixture(t)
	f.inlayOK(f.root, "init", "local")
	f.write("notes/a.md", "a\n")
	f.inlayOK(f.root, "claim", "local", "notes")
	f.inlayOK(f.root, "local", "commit", "-q", "-m", "notes")
	wt := filepath.Join(f.base, "wts", "wt")
	f.git(f.root, "-c", "worktree.useRelativePaths=true", "worktree", "add", "-q", wt, "-b", "feature")
	if got := f.read(".git/worktrees/wt/gitdir"); filepath.IsAbs(strings.TrimSpace(got)) {
		t.Fatalf("gitdir file = %q, want a relative path", got)
	}
	f.inlayOK(wt, "attach", "local")
	f.inlayOK(f.root, "hooks", "install")
	contains(t, f.inlayOK(f.root, "check"), "ok:", "check with a relative-path worktree")
	if got := f.inlayOK(f.root, "prune", "--dry-run"); got != "nothing to prune" {
		t.Fatalf("prune --dry-run with a relative-path worktree = %q", got)
	}

	writeFile(t, filepath.Join(wt, "wt-drafts", "a.md"), "a\n")
	f.inlayOK(wt, "claim", "local", "wt-drafts")
	f.inlayOK(f.root, "ls")
	contains(t, f.read(".git/info/exclude"), "/wt-drafts\n", "the managed block after a command in main")

	// A manifest broken by hand in one worktree is that worktree's to
	// report; another reads the committed one instead of failing.
	manifest := filepath.Join(wt, ".git-inlay", "local.paths")
	good := readAt(t, wt, ".git-inlay/local.paths")
	writeFile(t, manifest, good+"bad\\path\n")
	f.inlayFails(wt, 1, "ls")
	contains(t, f.inlayOK(f.root, "ls"), "local\tnotes/", "ls in main beside a broken manifest")
	writeFile(t, manifest, good)

	if err := os.RemoveAll(filepath.Join(f.base, "wts")); err != nil {
		t.Fatal(err)
	}
	contains(t, f.inlayFails(f.root, 1, "check"), "worktree wt/wt, which no longer exists", "check after the worktree is deleted")
	contains(t, f.inlayOK(f.root, "prune", "--dry-run"), "wt/wt\tlocal\twould", "prune --dry-run after the worktree is deleted")
}

// Pre-commit applies pre-push's diff filter: a staged type change at a
// claimed path, which only a path leaked past the guards can have, is
// refused like any other change there.
func TestPreCommitRefusesTypeChange(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "claude")
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	f.inlayOK(f.root, "hooks", "install")
	f.git(f.root, "add", "-f", ".claude/settings.json")
	f.git(f.root, "commit", "-q", "--no-verify", "-m", "leak")
	path := filepath.Join(f.root, ".claude", "settings.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../README.md", path); err != nil {
		t.Skipf("cannot make a symlink: %v", err)
	}
	f.git(f.root, "add", "-f", ".claude/settings.json")
	if got := f.git(f.root, "diff", "--cached", "--name-status"); !strings.HasPrefix(got, "T") {
		t.Skipf("git stored the symlink as a file (core.symlinks=false?): %q", got)
	}
	contains(t, f.gitFails(f.root, "commit", "-q", "-m", "retype"), ".claude/settings.json: belongs to inlay claude", "pre-commit on a type change")
}

// check reads without writing: a stale managed block or inlay exclude is a
// violation it reports and leaves for any other command to repair.
func TestCheckReportsStaleExcludes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "claude")
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	f.inlayOK(f.root, "hooks", "install")
	contains(t, f.inlayOK(f.root, "check"), "ok:", "check")

	block := f.read(".git/info/exclude")
	edited := strings.Replace(block, "/.claude\n", "", 1)
	f.write(".git/info/exclude", edited)
	inlayExclude := ".git/inlays/main/claude/info/exclude"
	f.write(inlayExclude, "/*\n")
	errOut := f.inlayFails(f.root, 1, "check")
	contains(t, errOut, "or has one that does not match the inlays' claims", "check with a stale block")
	contains(t, errOut, "claude: has a missing or out-of-date info/exclude", "check with a stale inlay exclude")
	if strings.Contains(errOut, "is not ignored by the host") {
		t.Fatal("check judged I2 against a stale block")
	}
	contains(t, errOut, "fix: run a git inlay command other than check", "check with a stale block")
	if f.read(".git/info/exclude") != edited || f.read(inlayExclude) != "/*\n" {
		t.Fatal("check rewrote an exclude file")
	}

	f.inlayOK(f.root, "ls")
	contains(t, f.inlayOK(f.root, "check"), "ok:", "check after ls")
}

// An interrupted clone leaves its staging directory, which no verb takes
// for an inlay; prune lists and removes it, but not one so recent that a
// clone may still be filling it.
func TestPruneAbandonedClone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "local")
	old := filepath.Join(f.root, ".git", "inlays", "main", ".clone-old")
	fresh := filepath.Join(f.root, ".git", "inlays", "main", ".clone-fresh")
	writeFile(t, filepath.Join(old, "objects", "pack", "x.pack"), "x")
	writeFile(t, filepath.Join(fresh, "HEAD"), "ref: refs/heads/main\n")
	// Liveness is the newest time anywhere in the tree, so a recent pack
	// deep inside keeps a staging directory; age the whole of old.
	past := time.Now().Add(-48 * time.Hour)
	for _, p := range []string{filepath.Join(old, "objects", "pack", "x.pack"), filepath.Join(old, "objects", "pack"), filepath.Join(old, "objects"), old} {
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}
	// In fresh only the file inside is recent, as while a fetch writes.
	if err := os.Chtimes(fresh, past, past); err != nil {
		t.Fatal(err)
	}

	if got := f.inlayOK(f.root, "prune", "--dry-run"); got != "main\t.clone-old\twould remove (abandoned clone)" {
		t.Fatalf("prune --dry-run = %q", got)
	}
	if !f.exists(".git/inlays/main/.clone-old") {
		t.Fatal("a dry run removed an abandoned clone")
	}
	if got := f.inlayOK(f.root, "prune"); got != "main\t.clone-old\tremoved (abandoned clone)" {
		t.Fatalf("prune = %q", got)
	}
	if f.exists(".git/inlays/main/.clone-old") || !f.exists(".git/inlays/main/.clone-fresh") {
		t.Fatal("prune removed the wrong staging directory")
	}
	if got := f.inlayOK(f.root, "prune"); got != "nothing to prune" {
		t.Fatalf("prune with only a recent clone = %q", got)
	}
}

// Scenario 8: with core.hooksPath set, install writes nothing in the hooks
// directory. Git 2.54 and newer get config-defined hooks; older git gets
// the snippet for the hook manager, and check can only warn.
func TestHooksPathManaged(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.git(f.root, "config", "core.hooksPath", ".husky")
	if err := os.MkdirAll(filepath.Join(f.root, ".husky"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.inlayOK(f.root, "init", "claude")
	out := f.inlayOK(f.root, "hooks", "install")
	if f.exists(".git/hooks/pre-commit") {
		t.Fatal("install wrote a shim despite core.hooksPath")
	}
	if configHooks(t) {
		contains(t, out, "pre-commit  configured hook.git-inlay-pre-commit", "install output")
		contains(t, f.inlayOK(f.root, "hooks", "status"), "pre-push    configured", "status")
		return
	}
	contains(t, out, "core.hooksPath", "install output")
	contains(t, out, "husky manages", "install output")
	contains(t, f.inlayOK(f.root, "hooks", "status"), "managed", "status")
	_, errOut, _ := f.inlay(f.root, "check")
	contains(t, errOut, "cannot verify that", "check under a hook manager")
}

// A hook that is not ours is left alone. Git 2.54 and newer get a
// config-defined hook beside it; older git tells the user what to add.
func TestForeignHookLeftAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(".git/hooks/pre-push", "#!/bin/sh\nexit 0\n")
	out := f.inlayOK(f.root, "hooks", "install")
	if configHooks(t) {
		contains(t, out, "pre-push    configured hook.git-inlay-pre-push", "install output")
	} else {
		contains(t, out, "not managed by git-inlay", "install output")
	}
	if f.read(".git/hooks/pre-push") != "#!/bin/sh\nexit 0\n" {
		t.Fatal("foreign pre-push hook was modified")
	}
	contains(t, f.read(".git/hooks/pre-commit"), "hook pre-commit", "pre-commit shim")
}

// On git 2.54 and newer, hooks install guards a host whose hooks a manager
// owns through config-defined hooks that run beside the manager's, and
// check reports a guard that is missing or disabled.
func TestConfigHooksBesideHookManager(t *testing.T) {
	t.Parallel()
	if !configHooks(t) {
		t.Skip("config-defined hooks need git 2.54 or newer")
	}
	f := newFixture(t)
	ran := filepath.ToSlash(filepath.Join(f.base, "ran"))
	for name, body := range map[string]string{
		"pre-commit": "#!/bin/sh\necho pre-commit >> '" + ran + "'\n",
		"pre-push":   "#!/bin/sh\ncat >/dev/null\necho pre-push >> '" + ran + "'\n",
	} {
		p := filepath.Join(f.root, ".hooks", name)
		writeFile(t, p, body)
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.git(f.root, "config", "core.hooksPath", ".hooks")
	f.inlayOK(f.root, "init", "claude")
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "settings")
	errOut := f.inlayFails(f.root, 1, "check")
	contains(t, errOut, "host pre-commit hook: is managed", "check before install")
	contains(t, errOut, "fix: git inlay hooks install", "check before install")

	contains(t, f.inlayOK(f.root, "hooks", "install"), "pre-commit  configured hook.git-inlay-pre-commit", "install")
	contains(t, f.inlayOK(f.root, "hooks", "install"), "pre-push    configured hook.git-inlay-pre-push", "second install")
	contains(t, f.inlayOK(f.root, "check"), "ok:", "check after install")

	f.write("CHANGELOG.md", "x\n")
	f.git(f.root, "add", "CHANGELOG.md")
	f.git(f.root, "commit", "-q", "-m", "clean")
	f.git(f.root, "push", "-q", "origin", "main")
	if got, err := os.ReadFile(ran); err != nil || string(got) != "pre-commit\npre-push\n" {
		t.Fatalf("the hook manager's hooks ran %q, %v", got, err)
	}

	f.git(f.root, "add", "-f", ".claude/settings.json")
	contains(t, f.gitFails(f.root, "commit", "-q", "-m", "leak"), ".claude/settings.json: belongs to inlay claude", "pre-commit beside the manager")
	f.git(f.root, "commit", "-q", "--no-verify", "-m", "leak")
	contains(t, f.gitFails(f.root, "push", "-q", "origin", "main"), ".claude/settings.json: belongs to inlay claude", "pre-push beside the manager")
	f.git(f.root, "reset", "-q", "--hard", "origin/main")
	f.inlayOK(f.root, "claude", "checkout", "--", ".")

	// Git's own boolean rules decide, so "0k" is false too.
	f.git(f.root, "config", "hook.git-inlay-pre-commit.enabled", "0k")
	contains(t, f.inlayOK(f.root, "hooks", "status"), "pre-commit  disabled", "status of a disabled guard")
	contains(t, f.inlayFails(f.root, 1, "check"), "host pre-commit hook: is disabled", "check with a disabled guard")
	f.inlayOK(f.root, "hooks", "install")
	contains(t, f.inlayOK(f.root, "check"), "ok:", "check after reinstall")

	// The section's own enabled = true outranks a global enabled = false.
	f.git(f.root, "config", "--global", "hook.git-inlay-pre-commit.enabled", "false")
	contains(t, f.inlayOK(f.root, "hooks", "status"), "pre-commit  configured", "status under a global enabled = false")
	f.git(f.root, "add", "-f", ".claude/settings.json")
	contains(t, f.gitFails(f.root, "commit", "-q", "-m", "leak"), ".claude/settings.json: belongs to inlay claude", "pre-commit under a global enabled = false")
	f.git(f.root, "reset", "-q")
	f.git(f.root, "config", "--global", "--unset", "hook.git-inlay-pre-commit.enabled")

	// An include later in the file outranks the section, so install writes
	// it afresh after the include.
	inc := filepath.Join(f.base, "team.inc")
	writeFile(t, inc, "[hook \"git-inlay-pre-commit\"]\n\tenabled = false\n")
	f.git(f.root, "config", "include.path", filepath.ToSlash(inc))
	contains(t, f.inlayFails(f.root, 1, "check"), "host pre-commit hook: is disabled", "check with a later include disabling the guard")
	contains(t, f.inlayOK(f.root, "hooks", "install"), "pre-commit  configured", "install after a later include")
	contains(t, f.inlayOK(f.root, "check"), "ok:", "check after install beside a later include")
	f.git(f.root, "config", "--unset", "include.path")

	// A value git cannot parse as a boolean counts as off rather than
	// failing status, and install rewrites it.
	f.git(f.root, "config", "hook.git-inlay-pre-commit.enabled", "nope")
	contains(t, f.inlayOK(f.root, "hooks", "status"), "pre-commit  disabled", "status with an unparseable enabled")
	f.inlayOK(f.root, "hooks", "install")
	contains(t, f.inlayOK(f.root, "check"), "ok:", "check after rewriting an unparseable enabled")

	// What install cannot outrank, such as the environment's config, it
	// reports as still off.
	out, _, code := f.runWith(f.root, []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=hook.git-inlay-pre-commit.enabled", "GIT_CONFIG_VALUE_0=false"},
		"git", "inlay", "hooks", "install")
	if code != 0 {
		t.Fatalf("install under GIT_CONFIG_* exited %d", code)
	}
	contains(t, out, "pre-commit  disabled", "install under an environment enabled=false")
	contains(t, out, "is still false", "install under an environment enabled=false")

	// A hand-made section that would not run the guard does not count.
	f.git(f.root, "config", "hook.git-inlay-pre-push.command", "bin=$(command -v git-inlay) || bin=x")
	contains(t, f.inlayFails(f.root, 1, "check"), "host pre-push hook: is managed", "check with a truncated command")
	f.inlayOK(f.root, "hooks", "install")

	// With per-worktree config, another worktree may point core.hooksPath
	// elsewhere and rely on the shared section, so a shim goes in beside it.
	f.git(f.root, "config", "extensions.worktreeConfig", "true")
	f.git(f.root, "config", "--unset", "core.hooksPath")
	contains(t, f.inlayOK(f.root, "hooks", "install"), "pre-commit  installed", "install under extensions.worktreeConfig")
	if _, _, code := f.run(f.root, "git", "config", "--get", "hook.git-inlay-pre-push.event"); code != 0 {
		t.Fatal("install dropped the shared section under extensions.worktreeConfig")
	}
	f.git(f.root, "config", "--unset", "extensions.worktreeConfig")

	// Otherwise, once the hooks directory is free again, the shim, which
	// every git runs, replaces the config-defined hook rather than
	// doubling it.
	contains(t, f.inlayOK(f.root, "hooks", "install"), "pre-commit  installed", "install with the hooks directory free")
	if _, _, code := f.run(f.root, "git", "config", "--get-regexp", `^hook\.git-inlay-`); code != 1 {
		t.Fatal("install kept a git-inlay hook section beside the shim")
	}
	f.git(f.root, "config", "core.hooksPath", ".hooks")
	f.inlayOK(f.root, "hooks", "install")

	f.inlayOK(f.root, "hooks", "uninstall")
	if _, _, code := f.run(f.root, "git", "config", "--get-regexp", `^hook\.git-inlay-`); code != 1 {
		t.Fatal("uninstall left a git-inlay hook section in the config")
	}
	contains(t, f.inlayFails(f.root, 1, "check"), "host pre-push hook: is managed", "check after uninstall")
	f.git(f.root, "config", "--unset", "core.hooksPath")
	f.inlayOK(f.root, "hooks", "uninstall")
	if f.exists(".git/hooks/pre-commit") {
		t.Fatal("uninstall left the shim")
	}
}

// Unclaim returns paths to the host's view without touching the files.
func TestUnclaim(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "claude")
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "settings")
	contains(t, f.inlayOK(f.root, "unclaim", "claude", ".claude"), "unclaimed .claude from inlay claude", "unclaim output")
	if got := f.git(f.root, "status", "--porcelain"); got != "?? .claude/" {
		t.Fatalf("host status after unclaim = %q", got)
	}
	if got := f.inlayOK(f.root, "claude", "ls-files"); got != ".git-inlay/claude.paths" {
		t.Fatalf("inlay ls-files after unclaim = %q", got)
	}
	if got := f.inlayOK(f.root, "ls"); got != "claude\t(no claims)" {
		t.Fatalf("ls after unclaim = %q", got)
	}
}

// A commit that only removes a leaked claimed path from the host may be
// pushed: it is the documented fix, and it publishes nothing new.
func TestDeletionPushAllowed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "claude")
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "settings")
	f.inlayOK(f.root, "hooks", "install")

	f.git(f.root, "add", "-f", ".claude/settings.json")
	f.git(f.root, "commit", "-q", "--no-verify", "-m", "leak")
	f.git(f.root, "push", "-q", "--no-verify", "origin", "main")
	f.git(f.root, "rm", "-q", "--cached", ".claude/settings.json")
	f.git(f.root, "commit", "-q", "-m", "unleak")
	f.git(f.root, "push", "-q", "origin", "main")
	if !f.exists(".claude/settings.json") {
		t.Fatal("rm --cached removed the file from disk")
	}
	contains(t, f.inlayOK(f.root, "check"), "ok:", "check after the fix")
}

// A committed host .gitignore outranks the inlay's exclude file, so a claim
// the host ignores is tracked anyway, with a warning, while the user's
// global excludes still apply.
func TestClaimUnderHostGitignore(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(".gitignore", ".claude/\n")
	f.git(f.root, "add", ".gitignore")
	f.git(f.root, "commit", "-q", "-m", "ignore claude")
	global := filepath.Join(f.base, "home", "global-ignore")
	writeFile(t, global, "settings.local.json\n")
	f.git(f.root, "config", "--global", "core.excludesFile", global)

	f.inlayOK(f.root, "init", "claude")
	f.write(".claude/settings.json", "{}\n")
	f.write(".claude/settings.local.json", "{}\n")
	_, errOut, code := f.inlay(f.root, "claim", "claude", ".claude")
	if code != 0 {
		t.Fatalf("claim: exit %d\n%s", code, errOut)
	}
	contains(t, errOut, "ignored by .gitignore", "claim warning")
	if got, want := f.inlayOK(f.root, "claude", "ls-files"), ".claude/settings.json\n.git-inlay/claude.paths"; got != want {
		t.Fatalf("inlay ls-files = %q, want %q", got, want)
	}
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "settings")
	if got := f.git(f.root, "status", "--porcelain"); got != "" {
		t.Fatalf("host status = %q", got)
	}

	// Cloning onto a host whose .gitignore covers the path still refuses to
	// overwrite an existing file, which git's checkout would do silently.
	private := f.bare("claude-private")
	f.inlayOK(f.root, "claude", "remote", "add", "origin", private)
	f.inlayOK(f.root, "claude", "push", "-q", "-u", "origin", "main")
	host2 := filepath.Join(f.base, "host2")
	f.git(f.base, "clone", "-q", f.remote, host2)
	writeFile(t, filepath.Join(host2, ".claude", "settings.json"), "local\n")
	contains(t, f.inlayFails(host2, 1, "clone", private), "would be overwritten", "clone over an ignored file")
	if b, _ := os.ReadFile(filepath.Join(host2, ".claude", "settings.json")); string(b) != "local\n" {
		t.Fatalf("clone overwrote the local file: %q", b)
	}
}

// A global core.hooksPath must not disable the inlay's own push guard.
func TestGlobalHooksPathKeepsInlayGuard(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.git(f.root, "config", "--global", "core.hooksPath", filepath.Join(f.base, "nohooks"))
	private := f.bare("x-private")
	f.inlayOK(f.root, "init", "x", "--remote", private)
	f.inlayOK(f.root, "x", "remote", "set-url", "origin", f.remote)
	_, errOut, code := f.inlay(f.root, "x", "push", "-q", "origin", "main")
	if code == 0 {
		t.Fatal("push to the host's remote succeeded despite a global core.hooksPath")
	}
	contains(t, errOut, "refusing to push inlay x", "inlay pre-push")
}

// On a case-insensitive filesystem a claim typed in the wrong case is still
// matched against what the host tracks.
func TestClaimCaseInsensitive(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write("CaseProbe", "")
	if _, err := os.Stat(filepath.Join(f.root, "caseprobe")); err != nil {
		t.Skip("filesystem is case-sensitive")
	}
	if err := os.Remove(filepath.Join(f.root, "CaseProbe")); err != nil {
		t.Fatal(err)
	}
	f.write("docs/a.md", "a\n")
	f.git(f.root, "add", "docs")
	f.git(f.root, "commit", "-q", "-m", "docs")
	f.inlayOK(f.root, "init", "me")
	contains(t, f.inlayFails(f.root, 1, "claim", "me", "Docs"), "tracked by the host", "claim with the wrong case")

	// Git folds case here, so a path that reaches the guards in another
	// case than the claim is still the claim's: a case-only rename on disk
	// must not let a forced add through.
	f.inlayOK(f.root, "hooks", "install")
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "me", ".claude")
	for _, step := range [][2]string{{".claude", ".claude-tmp"}, {".claude-tmp", ".Claude"}} {
		if err := os.Rename(filepath.Join(f.root, step[0]), filepath.Join(f.root, step[1])); err != nil {
			t.Fatal(err)
		}
	}
	f.git(f.root, "add", "-f", ".Claude/settings.json")
	contains(t, f.gitFails(f.root, "commit", "-q", "-m", "leak"), ".Claude/settings.json: belongs to inlay me", "pre-commit with the wrong case")
	contains(t, f.inlayFails(f.root, 1, "check", "--staged"), ".Claude/settings.json: belongs to inlay me", "check --staged with the wrong case")
	f.git(f.root, "commit", "-q", "--no-verify", "-m", "leak")
	contains(t, f.inlayFails(f.root, 1, "check"), ".Claude/settings.json: tracked by the host but claimed by inlay me", "check with the wrong case")
	if got := f.inlayOK(f.root, "which", ".CLAUDE/settings.json"); got != ".CLAUDE/settings.json\tme" {
		t.Fatalf("which with the wrong case = %q", got)
	}

	// Git's icase pathspec folds ASCII only; check folds every letter.
	f.write("Café/a.md", "a\n")
	f.inlayOK(f.root, "claim", "me", "Café")
	for _, step := range [][2]string{{"Café", "cafe-tmp"}, {"cafe-tmp", "CAFÉ"}} {
		if err := os.Rename(filepath.Join(f.root, step[0]), filepath.Join(f.root, step[1])); err != nil {
			t.Fatal(err)
		}
	}
	f.git(f.root, "add", "-f", "CAFÉ/a.md")
	f.git(f.root, "commit", "-q", "--no-verify", "-m", "leak")
	contains(t, f.inlayFails(f.root, 1, "check"), "CAFÉ/a.md: tracked by the host but claimed by inlay me", "check with a non-ASCII case variant")
}

// A host .gitignore that ignores dotfiles but re-includes a path cannot be
// overridden by the managed block, so claiming that path is refused; the
// manifest directory itself, which the same pattern ignores, is still
// staged.
func TestClaimRefusesHostNegation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(".gitignore", ".*\n!.gitignore\n!.claude\n")
	f.git(f.root, "add", ".gitignore")
	f.git(f.root, "commit", "-q", "-m", "dotfiles ignored except claude")
	f.inlayOK(f.root, "init", "claude")
	if got := f.inlayOK(f.root, "claude", "ls-files"); got != ".git-inlay/claude.paths" {
		t.Fatalf("manifest not staged under an ignoring .gitignore: ls-files = %q", got)
	}
	f.write(".claude/settings.json", "{}\n")
	contains(t, f.inlayFails(f.root, 1, "claim", "claude", ".claude"), "re-included by .gitignore", "claim under a negation")
	f.inlayOK(f.root, "claim", "claude", ".secrets/")
	if got := f.inlayOK(f.root, "ls"); got != "claude\t.secrets/" {
		t.Fatalf("ls = %q", got)
	}
}

// A file sitting where a claimed directory must go would be deleted by
// checkout, so clone refuses it.
func TestCloneRefusesFileInTheWay(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	private := f.bare("claude-private")
	f.inlayOK(f.root, "init", "claude", "--remote", private)
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "settings")
	f.inlayOK(f.root, "claude", "push", "-q", "-u", "origin", "main")

	host2 := filepath.Join(f.base, "host2")
	f.git(f.base, "clone", "-q", f.remote, host2)
	writeFile(t, filepath.Join(host2, ".claude"), "a file, not a directory\n")
	contains(t, f.inlayFails(host2, 1, "clone", private), "in the way", "clone over a blocking file")
	if b, _ := os.ReadFile(filepath.Join(host2, ".claude")); string(b) != "a file, not a directory\n" {
		t.Fatalf("clone deleted the blocking file: %q", b)
	}
}

// A configured global excludes file that does not exist is skipped, as git
// itself does, instead of failing every claim.
func TestMissingGlobalExcludesIsSkipped(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.git(f.root, "config", "--global", "core.excludesFile", filepath.Join(f.base, "home", "does-not-exist"))
	f.inlayOK(f.root, "init", "claude")
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	if got, want := f.inlayOK(f.root, "claude", "ls-files"), ".claude/settings.json\n.git-inlay/claude.paths"; got != want {
		t.Fatalf("inlay ls-files = %q, want %q", got, want)
	}
}

// Scenario 6: linked worktrees. attach copies an inlay from the main
// worktree, the copies commit independently, the host exclude is the union
// across worktrees, and prune removes inlays once their worktree is gone,
// keeping any with unpushed commits unless forced.
func TestWorktreeAttachAndPrune(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	private := f.bare("claude-private")
	f.inlayOK(f.root, "init", "claude", "--remote", private)
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "settings")
	f.inlayOK(f.root, "claude", "push", "-q", "-u", "origin", "main")
	f.inlayOK(f.root, "init", "local")
	f.write("notes/a.md", "a\n")
	f.inlayOK(f.root, "claim", "local", "notes")
	f.inlayOK(f.root, "local", "commit", "-q", "-m", "notes")

	wt := filepath.Join(f.base, "wts", "wt")
	f.git(f.root, "worktree", "add", "-q", wt, "-b", "feature")
	contains(t, f.inlayFails(wt, 1, "attach", "nope"), `no inlay named "nope" is attached to another worktree`, "attach of an unknown inlay")
	f.inlayFails(wt, 2, "attach")
	f.inlayFails(wt, 2, "attach", "--all", "claude")
	contains(t, f.inlayOK(wt, "ls"), "attached in other worktrees: claude, local", "ls hint")

	// An uncommitted change in the main copy is reported, not copied.
	f.write(".claude/settings.json", "{\"dirty\":true}\n")
	out, errOut, code := f.inlay(wt, "attach", "claude")
	if code != 0 {
		t.Fatalf("attach: exit %d\n%s", code, errOut)
	}
	contains(t, out, "attached inlay claude from worktree main (branch main, 1 claims)", "attach output")
	contains(t, errOut, "has uncommitted changes, which attach does not copy", "attach warning")
	f.inlayOK(f.root, "claude", "checkout", "-q", "--", ".claude/settings.json")
	if got := readAt(t, wt, ".claude/settings.json"); got != "{}\n" {
		t.Fatalf("settings in the worktree = %q", got)
	}
	if got := f.git(wt, "status", "--porcelain"); got != "" {
		t.Fatalf("worktree host status = %q", got)
	}
	if got := f.inlayOK(wt, "ls"); got != "claude\t.claude/" {
		t.Fatalf("ls in the worktree = %q", got)
	}
	contains(t, f.inlayFails(wt, 1, "attach", "claude"), "already attached", "second attach")
	// The copy tracks the real remote, not the sibling, and keeps no
	// scratch refs.
	if got := f.inlayOK(wt, "claude", "remote", "get-url", "origin"); got != private {
		t.Fatalf("origin in the worktree copy = %q, want %q", got, private)
	}
	if got := f.inlayOK(wt, "claude", "rev-parse", "--abbrev-ref", "@{upstream}"); got != "origin/main" {
		t.Fatalf("upstream in the worktree copy = %q", got)
	}
	if got := f.inlayOK(wt, "claude", "for-each-ref", "refs/sibling/"); got != "" {
		t.Fatalf("scratch refs left behind: %q", got)
	}

	// Commits are independent and travel through the remote.
	writeFile(t, filepath.Join(wt, ".claude", "settings.json"), "{\"wt\":1}\n")
	f.inlayOK(wt, "claude", "commit", "-q", "-a", "-m", "from the worktree")
	if f.read(".claude/settings.json") != "{}\n" {
		t.Fatal("a commit in the worktree copy changed the main copy")
	}
	f.inlayOK(wt, "claude", "push", "-q")
	f.inlayOK(f.root, "claude", "pull", "-q")
	if f.read(".claude/settings.json") != "{\"wt\":1}\n" {
		t.Fatal("the main copy did not receive the worktree's commit")
	}

	// A claim made in the worktree is excluded in every worktree.
	writeFile(t, filepath.Join(wt, "wt-only", "x"), "x\n")
	f.inlayOK(wt, "claim", "claude", "wt-only")
	f.inlayOK(wt, "claude", "commit", "-q", "-m", "wt-only")
	if _, _, code := f.run(f.root, "git", "check-ignore", "-q", "wt-only"); code != 0 {
		t.Fatal("the main worktree does not ignore a path claimed in a linked worktree")
	}
	f.inlayOK(f.root, "hooks", "install")
	contains(t, f.inlayOK(f.root, "check"), "ok:", "check in main with a worktree claim")

	// A claim staged but not committed in one worktree survives a command
	// in another, which reads that worktree's manifest from its tree.
	f.write("drafts/a.md", "a\n")
	f.inlayOK(f.root, "claim", "local", "drafts")
	f.inlayOK(wt, "ls")
	contains(t, f.read(".git/info/exclude"), "/drafts\n", "the managed block after a command in another worktree")
	if got := f.git(f.root, "status", "--porcelain"); got != "" {
		t.Fatalf("host status in main after a command in another worktree = %q", got)
	}
	f.inlayOK(f.root, "unclaim", "local", "drafts")
	if err := os.RemoveAll(filepath.Join(f.root, "drafts")); err != nil {
		t.Fatal(err)
	}

	// A stash in the worktree's copy is work too.
	writeFile(t, filepath.Join(wt, ".claude", "settings.json"), "{\"stash\":1}\n")
	f.inlayOK(wt, "claude", "stash", "-q")

	// --all brings over what is left; local has no remote to reuse.
	out, errOut, code = f.inlay(wt, "attach", "--all")
	if code != 0 {
		t.Fatalf("attach --all: exit %d\n%s", code, errOut)
	}
	contains(t, out, "attached inlay local from worktree main", "attach --all output")
	contains(t, errOut, "inlay local has no remote", "attach --all warning")
	if got := readAt(t, wt, "notes/a.md"); got != "a\n" {
		t.Fatalf("notes in the worktree = %q", got)
	}
	if got := f.inlayOK(wt, "attach", "--all"); got != "no inlays are attached to other worktrees of this host" {
		t.Fatalf("attach --all with nothing left = %q", got)
	}
	if got := f.inlayOK(f.root, "prune"); got != "nothing to prune" {
		t.Fatalf("prune with every worktree present = %q", got)
	}

	// A locked worktree whose path is missing, as with an unmounted volume,
	// is not stale.
	f.git(f.root, "worktree", "lock", wt)
	if err := os.RemoveAll(filepath.Join(f.base, "wts")); err != nil {
		t.Fatal(err)
	}
	if got := f.inlayOK(f.root, "prune"); got != "nothing to prune" {
		t.Fatalf("prune with a locked worktree = %q", got)
	}
	contains(t, f.inlayOK(f.root, "check"), "ok:", "check with a locked worktree")

	// Unlocked, with even the parent directory gone, the inlays are stale.
	// claude has the unpushed wt-only commit and local has no remote at
	// all, so both are kept until pushed or forced.
	if err := os.Remove(filepath.Join(f.root, ".git", "worktrees", "wt", "locked")); err != nil {
		t.Fatal(err)
	}
	contains(t, f.inlayFails(f.root, 1, "check"), "worktree wt/wt, which no longer exists", "check with a stale inlay")
	out = f.inlayOK(f.root, "prune", "--dry-run")
	contains(t, out, "wt/wt\tclaude\twould keep (1 unpushed commit(s), 1 stash(es))", "dry run")
	contains(t, out, "wt/wt\tlocal\twould keep (2 unpushed commit(s))", "dry run")
	if !f.exists(".git/inlays/wt/wt/claude") {
		t.Fatal("a dry run removed an inlay")
	}
	out, errOut, code = f.inlay(f.root, "prune")
	if code != 1 {
		t.Fatalf("prune with unpushed commits: exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	contains(t, errOut, "claude: push with: git --git-dir=", "prune advice")
	contains(t, errOut, filepath.Join(".git", "inlays", "wt", "wt", "claude")+" --work-tree=", "prune advice")
	contains(t, errOut, " push origin main", "prune advice")
	contains(t, errOut, "local has no remote; add one with:", "prune advice for an inlay without a remote")
	if !f.exists(".git/inlays/wt/wt/claude") || !f.exists(".git/inlays/wt/wt/local") {
		t.Fatal("prune removed an inlay with unpushed commits")
	}

	// The advised push works with the worktree and its parent gone, and
	// passes the inlay's own guard.
	staleClaude := filepath.Join(f.root, ".git", "inlays", "wt", "wt", "claude")
	common := filepath.Join(f.root, ".git")
	stale := func(args ...string) string {
		t.Helper()
		return f.git(f.base, append([]string{"--git-dir=" + staleClaude, "--work-tree=" + common}, args...)...)
	}
	stale("push", "-q", "origin", "main")

	// It also passes when an unrelated repository, with a remote at the
	// inlay's own URL, has since appeared at the old path: the guard
	// answers from the inlay's real host, not from whatever sits there now.
	tree := stale("rev-parse", "HEAD^{tree}")
	commit := stale("commit-tree", tree, "-p", "HEAD", "-m", "after the worktree went")
	stale("update-ref", "refs/heads/main", commit)
	f.git(f.base, "clone", "-q", f.remote, wt)
	f.git(wt, "remote", "add", "mirror", private)
	stale("push", "-q", "origin", "main")
	if err := os.RemoveAll(filepath.Join(f.base, "wts")); err != nil {
		t.Fatal(err)
	}

	// The stash still holds claude back, with its own advice.
	out, errOut, code = f.inlay(f.root, "prune")
	if code != 1 {
		t.Fatalf("prune with a stash: exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	contains(t, out, "wt/wt\tclaude\tkept (1 stash(es))", "prune with a stash")
	contains(t, errOut, "claude has 1 stash(es)", "prune advice for a stash")
	stale("stash", "drop", "-q")

	// The pushed inlay goes; the other stays until forced.
	out, errOut, code = f.inlay(f.root, "prune")
	if code != 1 {
		t.Fatalf("prune with one unpushed inlay: exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	contains(t, out, "wt/wt\tclaude\tremoved", "prune after the push")
	contains(t, out, "wt/wt\tlocal\tkept (2 unpushed commit(s))", "prune after the push")
	if f.exists(".git/inlays/wt/wt/claude") {
		t.Fatal("the pushed inlay was not removed")
	}
	contains(t, f.inlayOK(f.root, "prune", "--force"), "wt/wt\tlocal\tremoved (2 unpushed commit(s))", "forced prune")
	if f.exists(".git/inlays/wt") {
		t.Fatal("an empty worktree directory was left behind")
	}
	// With the worktree's claim gone, so is its exclude entry.
	if _, _, code := f.run(f.root, "git", "check-ignore", "-q", "wt-only"); code == 0 {
		t.Fatal("the main worktree still ignores the pruned worktree's claim")
	}
	contains(t, f.inlayOK(f.root, "check"), "ok: 2 inlay(s), 2 claim(s)", "check after prune")
}

// Scenario 9: detach with and without --keep-files, refusing uncommitted
// and unpushed work unless forced.
func TestDetach(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	private := f.bare("claude-private")
	f.inlayOK(f.root, "init", "claude", "--remote", private)
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "settings")
	f.inlayOK(f.root, "init", "local")
	f.write("notes/a.md", "a\n")
	f.inlayOK(f.root, "claim", "local", "notes")
	f.inlayOK(f.root, "local", "commit", "-q", "-m", "notes")

	contains(t, f.inlayFails(f.root, 1, "detach", "nope"), "no inlay named", "detach of an unknown inlay")
	f.inlayFails(f.root, 2, "detach")
	contains(t, f.inlayFails(f.root, 1, "detach", "claude"), "commit(s) on no remote", "detach with unpushed commits")
	f.inlayOK(f.root, "claude", "push", "-q", "-u", "origin", "main")
	f.write(".claude/settings.json", "{\"edited\":true}\n")
	contains(t, f.inlayFails(f.root, 1, "detach", "claude"), "uncommitted changes", "detach with a dirty tree")
	f.inlayOK(f.root, "claude", "checkout", "-q", "--", ".claude/settings.json")
	f.write(".claude/settings.json", "{\"stashed\":true}\n")
	f.inlayOK(f.root, "claude", "stash", "-q")
	contains(t, f.inlayFails(f.root, 1, "detach", "claude"), "has 1 stash(es)", "detach with a stash")
	f.inlayOK(f.root, "claude", "stash", "drop", "-q")

	if got := f.inlayOK(f.root, "detach", "claude"); got != "detached inlay claude; 2 file(s) removed" {
		t.Fatalf("detach = %q", got)
	}
	for _, rel := range []string{".claude", ".git-inlay/claude.paths", ".git/inlays/main/claude"} {
		if f.exists(rel) {
			t.Fatalf("%s survived detach", rel)
		}
	}
	if !f.exists(".git-inlay/local.paths") {
		t.Fatal("detach removed another inlay's manifest")
	}
	if got := f.inlayOK(f.root, "ls"); got != "local\tnotes/" {
		t.Fatalf("ls after detach = %q", got)
	}
	if got := f.git(f.root, "status", "--porcelain"); got != "" {
		t.Fatalf("host status after detach = %q", got)
	}
	if strings.Contains(f.read(".git/info/exclude"), "/.claude\n") {
		t.Fatal("the detached inlay's claim is still excluded")
	}
	f.inlayOK(f.root, "hooks", "install")
	contains(t, f.inlayOK(f.root, "check"), "ok: 1 inlay(s), 1 claim(s)", "check after detach")

	contains(t, f.inlayFails(f.root, 1, "detach", "local"), "has no remote", "detach of an inlay without a remote")
	_, errOut, code := f.inlay(f.root, "detach", "local", "--force", "--keep-files")
	if code != 0 {
		t.Fatalf("detach --force --keep-files: exit %d\n%s", code, errOut)
	}
	contains(t, errOut, "visible to the host as untracked", "keep-files warning")
	if !f.exists("notes/a.md") || !f.exists(".git-inlay/local.paths") {
		t.Fatal("--keep-files did not keep the files")
	}
	if got := f.git(f.root, "status", "--porcelain"); got != "?? .git-inlay/\n?? notes/" {
		t.Fatalf("host status after a kept detach = %q", got)
	}
	// With no inlay left, the managed block is gone from the host exclude.
	if strings.Contains(f.read(".git/info/exclude"), "git-inlay") {
		t.Fatal("the managed block survived the last detach")
	}
	if got := f.inlayOK(f.root, "ls"); got != "no inlays are attached to this worktree" {
		t.Fatalf("ls after the last detach = %q", got)
	}
}

// status reports branch, upstream relation and tree state per inlay, with
// --fetch for the remote's view; --porcelain on ls and status gives
// stable tab-separated lines.
func TestStatusAndPorcelain(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	private := f.bare("claude-private")
	if got := f.inlayOK(f.root, "status"); got != "no inlays are attached to this worktree" {
		t.Fatalf("status with no inlays = %q", got)
	}
	if got := f.inlayOK(f.root, "status", "--porcelain"); got != "" {
		t.Fatalf("status --porcelain with no inlays = %q", got)
	}
	f.inlayOK(f.root, "init", "claude", "--remote", private)
	f.inlayOK(f.root, "init", "local")
	if got, want := f.inlayOK(f.root, "status"), "claude\tmain\tno upstream\tclean\nlocal\tmain\tno remote\tclean"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "settings")
	f.inlayOK(f.root, "claude", "push", "-q", "-u", "origin", "main")
	contains(t, f.inlayOK(f.root, "status"), "claude\tmain\tup to date\tclean", "status when pushed")
	f.write(".claude/settings.json", "{\"a\":1}\n")
	f.write(".claude/new.md", "n\n")
	contains(t, f.inlayOK(f.root, "status"), "claude\tmain\tup to date\t1 changed, 1 untracked", "status with a dirty tree")
	f.inlayOK(f.root, "claude", "add", "-A")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "more")
	contains(t, f.inlayOK(f.root, "status"), "claude\tmain\tahead 1\tclean", "status when ahead")
	contains(t, f.inlayOK(f.root, "status", "--porcelain"), "claude\tmain\torigin/main\t1\t0\t0\t0", "porcelain status")

	// Someone else pushes to the private remote.
	peer := filepath.Join(f.base, "peer")
	f.git(f.base, "clone", "-q", private, peer)
	writeFile(t, filepath.Join(peer, ".claude", "settings.json"), "{\"peer\":1}\n")
	f.git(peer, "commit", "-q", "-a", "-m", "peer")
	f.git(peer, "push", "-q", "origin", "main")
	contains(t, f.inlayOK(f.root, "status"), "claude\tmain\tahead 1\tclean", "status before fetch")
	contains(t, f.inlayOK(f.root, "status", "--fetch"), "claude\tmain\tahead 1, behind 1\tclean", "status after fetch")
	contains(t, f.inlayOK(f.root, "status", "--porcelain"), "local\tmain\t\t0\t0\t0\t0", "porcelain status without a remote")

	if got := f.inlayOK(f.root, "ls", "--porcelain"); got != "claude\t.claude/" {
		t.Fatalf("ls --porcelain = %q", got)
	}
	if got := f.inlayOK(f.root, "ls"); got != "claude\t.claude/\nlocal\t(no claims)" {
		t.Fatalf("ls = %q", got)
	}
	if got := f.inlayOK(f.root, "which", ".claude/new.md", "README.md"); got != ".claude/new.md\tclaude\nREADME.md\thost" {
		t.Fatalf("which = %q", got)
	}
	f.inlayFails(f.root, 2, "which", "--porcelain", "README.md")
	if got, want := f.inlayOK(f.root, "--version"), f.inlayOK(f.root, "version"); got != want || !strings.Contains(got, "git") {
		t.Fatalf("--version = %q, version = %q", got, want)
	}
}

// detach never deletes a file the host also tracks, which an upstream
// commit at a claimed path can bring about; check reports that instead.
func TestDetachLeavesHostTrackedFiles(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	private := f.bare("claude-private")
	f.inlayOK(f.root, "init", "claude", "--remote", private, "--no-hooks")
	f.write(".claude/settings.json", "{}\n")
	f.write(".claude/other.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "settings")
	f.inlayOK(f.root, "claude", "push", "-q", "-u", "origin", "main")
	f.git(f.root, "add", "-f", ".claude/settings.json")
	f.git(f.root, "commit", "-q", "-m", "leak")

	out, errOut, code := f.inlay(f.root, "detach", "claude")
	if code != 0 {
		t.Fatalf("detach: exit %d\n%s", code, errOut)
	}
	contains(t, out, "detached inlay claude; 2 file(s) removed", "detach output")
	contains(t, errOut, "left 1 file(s) the host also tracks: .claude/settings.json", "detach warning")
	if !f.exists(".claude/settings.json") || f.exists(".claude/other.json") {
		t.Fatal("detach deleted the wrong files")
	}
	if got := f.git(f.root, "status", "--porcelain"); got != "" {
		t.Fatalf("host status after detach = %q", got)
	}
}

// A claimed directory may hold more files than a command line carries, so
// claim stages them through stdin and detach asks the host by claim.
func TestClaimAndDetachLargeDirectory(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "claude")
	// Enough literal pathspecs to pass the limit: 32 KiB on Windows, about
	// 2 MiB elsewhere.
	n := 25000
	if runtime.GOOS == "windows" {
		n = 1500
	}
	name := strings.Repeat("n", 90)
	for i := range n {
		f.write(fmt.Sprintf("notes/%s-%05d.md", name, i), "x\n")
	}
	contains(t, f.inlayOK(f.root, "claim", "claude", "notes"), fmt.Sprintf("staged %d file(s)", n), "claim output")
	contains(t, f.inlayOK(f.root, "detach", "claude", "--force"), fmt.Sprintf("%d file(s) removed", n+1), "detach output")
	if f.exists("notes") {
		t.Fatal("detach left the claimed directory behind")
	}
}

// Hook scripts under core.hooksPath that call the guards, as the husky and
// lefthook snippets do, refuse leaks at commit and at push with git's
// stdin forwarded.
func TestHooksPathScriptsRunGuards(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.git(f.root, "config", "core.hooksPath", ".husky")
	for name, body := range map[string]string{
		"pre-commit": "#!/bin/sh\ngit-inlay hook pre-commit\n",
		"pre-push":   "#!/bin/sh\ngit-inlay hook pre-push \"$@\"\n",
	} {
		p := filepath.Join(f.root, ".husky", name)
		writeFile(t, p, body)
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.inlayOK(f.root, "init", "claude")
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "settings")
	status := f.inlayOK(f.root, "hooks", "status")
	contains(t, status, "managed", "hooks status")
	contains(t, status, "inlay:claude installed", "inlay guard under a hooks path")

	f.git(f.root, "add", "-f", ".claude/settings.json")
	contains(t, f.gitFails(f.root, "commit", "-q", "-m", "leak"), ".claude/settings.json: belongs to inlay claude", "pre-commit through the hooks path")
	f.git(f.root, "commit", "-q", "--no-verify", "-m", "leak")
	contains(t, f.gitFails(f.root, "push", "-q", "origin", "main"), ".claude/settings.json: belongs to inlay claude", "pre-push through the hooks path")
}

// The pre-commit framework runs the guards with no stdin and the refs in
// environment variables, which is what its snippet relies on.
func TestPreCommitFrameworkProtocol(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "claude", "--no-hooks")
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "settings")

	// pre-commit inspects the index as staged.
	f.git(f.root, "add", "-f", ".claude/settings.json")
	contains(t, f.inlayFails(f.root, 1, "hook", "pre-commit"), ".claude/settings.json: belongs to inlay claude", "pre-commit via the framework")
	f.git(f.root, "commit", "-q", "-m", "leak")
	base := f.git(f.root, "rev-parse", "origin/main")
	head := f.git(f.root, "rev-parse", "HEAD")

	// pre-push gets the range from the environment.
	env := []string{"PRE_COMMIT_FROM_REF=" + base, "PRE_COMMIT_TO_REF=" + head, "PRE_COMMIT_REMOTE_NAME=origin"}
	_, errOut, code := f.runWith(f.root, env, "git", "inlay", "hook", "pre-push")
	if code != 1 {
		t.Fatalf("pre-push via the framework: exit %d\n%s", code, errOut)
	}
	contains(t, errOut, ".claude/settings.json: belongs to inlay claude", "pre-push via the framework")

	// A new branch has no FROM_REF; commits the remote already has are
	// skipped, so the same leak is refused before it reaches origin and
	// not after.
	env = []string{"PRE_COMMIT_TO_REF=" + head, "PRE_COMMIT_REMOTE_NAME=origin"}
	if _, errOut, code := f.runWith(f.root, env, "git", "inlay", "hook", "pre-push"); code != 1 {
		t.Fatalf("pre-push of a new branch via the framework: exit %d\n%s", code, errOut)
	}
	f.git(f.root, "push", "-q", "origin", "main")
	if _, errOut, code := f.runWith(f.root, env, "git", "inlay", "hook", "pre-push"); code != 0 {
		t.Fatalf("pre-push with everything on the remote: exit %d\n%s", code, errOut)
	}
}

// A push by URL has no remote-tracking refs to subtract, so the whole
// history behind the update is scanned: a leak that reached a named remote
// earlier is still refused on its way to another repository.
func TestPushByURLScansHistory(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "claude")
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "settings")
	f.inlayOK(f.root, "hooks", "install")
	f.git(f.root, "add", "-f", ".claude/settings.json")
	f.git(f.root, "commit", "-q", "--no-verify", "-m", "leak")
	f.git(f.root, "push", "-q", "--no-verify", "origin", "main")
	f.write("CHANGELOG.md", "x\n")
	f.git(f.root, "add", "CHANGELOG.md")
	f.git(f.root, "commit", "-q", "-m", "clean")

	// Only the new commit goes to origin, so that push passes.
	f.git(f.root, "push", "-q", "origin", "main")
	mirror := f.bare("mirror")
	contains(t, f.gitFails(f.root, "push", "-q", mirror, "main"), ".claude/settings.json: belongs to inlay claude", "push by URL")
}

// A passthrough form that acts on ignored files is refused, since from the
// inlay's side the whole host tree is ignored; "--" ends flag parsing.
func TestPassthroughRefusesTreeWideForms(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "claude")
	f.write("scratch.txt", "untracked in the host\n")
	for _, args := range [][]string{
		{"clean", "-fdx"}, {"clean", "-f", "-X"}, {"clean", "-n", "-x"},
		{"stash", "--all"}, {"stash", "push", "-a"},
	} {
		errOut := f.inlayFails(f.root, 1, append([]string{"claude"}, args...)...)
		contains(t, errOut, "whole host tree", "refusal of "+strings.Join(args, " "))
	}
	if !f.exists("scratch.txt") || !f.exists("README.md") {
		t.Fatal("a refused command touched the host tree")
	}
	// The ordinary forms still run, against the inlay alone.
	f.inlayOK(f.root, "claude", "clean", "-fdn")
	f.inlayOK(f.root, "claude", "stash", "list")
	if !f.exists("scratch.txt") {
		t.Fatal("clean -n removed a file")
	}
	if got := f.inlayOK(f.root, "which", "--", "README.md"); got != "README.md\thost" {
		t.Fatalf("which -- README.md = %q", got)
	}
}

// A failed init leaves neither a git dir that would look attached nor a
// stray manifest in the tree.
func TestInitFailureLeavesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// An empty ident makes the initial commit fail.
	_, errOut, code := f.runWith(f.root, []string{"GIT_AUTHOR_NAME=", "GIT_COMMITTER_NAME="}, "git", "inlay", "init", "claude")
	if code != 3 {
		t.Fatalf("init with a broken ident: exit %d, want 3\n%s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".git", "inlays", "main", "claude")); err == nil {
		t.Fatal("a failed init left its git dir behind")
	}
	if f.exists(".git-inlay") {
		t.Fatal("a failed init left .git-inlay in the tree")
	}
	if got := f.git(f.root, "status", "--porcelain"); got != "" {
		t.Fatalf("host status after a failed init = %q, want clean", got)
	}
	f.inlayOK(f.root, "init", "claude")
}

// The inlay's push guard judges outgoing commits by the manifest they
// carry, not by an uncommitted edit of the working-tree manifest, since the
// committed one is what a clone enforces.
func TestInlayPushUsesCommittedManifest(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	private := f.bare("claude-private")
	f.inlayOK(f.root, "init", "claude", "--remote", private)
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "settings")

	// A claim added by hand to the tree manifest, with its file committed
	// but the manifest not: the pushed tip does not cover the file.
	f.write("notes.md", "private\n")
	f.write(".git-inlay/claude.paths", f.read(".git-inlay/claude.paths")+"notes.md\n")
	f.inlayOK(f.root, "claude", "add", "-f", "--", "notes.md")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "notes", "--", "notes.md")
	_, errOut, code := f.inlay(f.root, "claude", "push", "-q", "origin", "main")
	if code == 0 {
		t.Fatal("push of a file outside the committed manifest succeeded")
	}
	contains(t, errOut, "notes.md: is in the tree being pushed but under no claim", "inlay pre-push")

	// With the manifest committed too, the same push passes.
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "claim notes", "--", ".git-inlay/claude.paths")
	f.inlayOK(f.root, "claude", "push", "-q", "origin", "main")
}

// Only the pushed tip must satisfy its manifest: commits that carried a
// path under a claim since dropped may still be pushed, to the first
// remote or to a new one.
func TestInlayPushAfterUnclaim(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	private := f.bare("claude-private")
	f.inlayOK(f.root, "init", "claude", "--remote", private)
	f.write("notes/a.md", "a\n")
	f.inlayOK(f.root, "claim", "claude", "notes")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "notes")
	f.inlayOK(f.root, "unclaim", "claude", "notes")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "unclaim notes")
	f.inlayOK(f.root, "claude", "push", "-q", "origin", "main")

	second := f.bare("claude-second")
	f.inlayOK(f.root, "claude", "remote", "add", "second", second)
	f.inlayOK(f.root, "claude", "push", "-q", "second", "main")
}

// Moving the host checkout leaves the inlays' absolute paths stale; the
// next command repairs them, so the inlay's push guard still finds its host.
func TestMovedCheckoutRepairsInlayConfig(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	private := f.bare("claude-private")
	f.inlayOK(f.root, "init", "claude", "--remote", private)
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "settings")

	moved := filepath.Join(f.base, "moved")
	if err := os.Rename(f.root, moved); err != nil {
		t.Fatal(err)
	}
	f.root = moved
	f.inlayOK(moved, "ls")
	gitDir := filepath.Join(moved, ".git", "inlays", "main", "claude")
	want := f.git(moved, "rev-parse", "--path-format=absolute", "--show-toplevel")
	got := f.git(moved, "--git-dir="+gitDir, "--work-tree="+moved, "config", "core.worktree")
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("core.worktree after the move = %q, want %q", got, want)
	}
	f.inlayOK(moved, "claude", "push", "-q", "origin", "main")
	f.inlayOK(moved, "hooks", "install")
	contains(t, f.inlayOK(moved, "check"), "ok: 1 inlay(s)", "check after the move")

	// The sync step trusts the stamp Configure leaves, so a hand edit of
	// the config survives an ordinary command; check reports it and
	// hooks install puts it back.
	f.git(moved, "--git-dir="+gitDir, "--work-tree="+moved, "config", "core.hooksPath", filepath.Join(f.base, "elsewhere"))
	f.inlayOK(moved, "ls")
	contains(t, f.inlayFails(moved, 1, "check"), "claude: has a config that does not name this host", "check after a hand edit")
	f.inlayOK(moved, "hooks", "install")
	contains(t, f.inlayOK(moved, "check"), "ok: 1 inlay(s)", "check after hooks install")
}

// An alias at the command position is read through, nested ones too; a
// shell alias cannot be and runs as git would run it.
func TestPassthroughResolvesAliases(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "claude")
	f.write("scratch.txt", "untracked in the host\n")
	f.git(f.root, "config", "--global", "alias.nuke", "clean -fdx")
	f.git(f.root, "config", "--global", "alias.nuke2", "nuke")
	f.git(f.root, "config", "--global", "alias.cl", "clean -fd")
	f.git(f.root, "config", "--global", "alias.sh", "!echo shell alias")
	// Git ignores an alias that hides a command, so these must not count.
	f.git(f.root, "config", "--global", "alias.stash", "stash list")
	f.git(f.root, "config", "--global", "alias.status", "clean -fdx")
	for _, args := range [][]string{
		{"nuke"}, {"nuke2"}, {"cl", "-x"}, {"-c", "clean.requireForce=false", "nuke"},
		{"-c", "alias.z=clean -fdx", "z"}, {"stash", "--all"},
	} {
		errOut := f.inlayFails(f.root, 1, append([]string{"claude"}, args...)...)
		contains(t, errOut, "whole host tree", "refusal of "+strings.Join(args, " "))
	}
	contains(t, f.inlayFails(f.root, 1, "claude", "nuke"), "git nuke, an alias of git clean -fdx,", "expansion in the refusal")
	if !f.exists("scratch.txt") {
		t.Fatal("a refused alias touched the host tree")
	}
	f.inlayOK(f.root, "claude", "cl", "-n")
	f.inlayOK(f.root, "claude", "status", "--porcelain")
	contains(t, f.inlayOK(f.root, "claude", "sh"), "shell alias", "shell alias passthrough")
}

// pathWithoutInlay returns a PATH that finds git but no git-inlay, so a
// hook runs the way a GUI client runs it, without the user's PATH. Any
// installed git-inlay is hidden by dropping its directory; git itself is
// reached through a link, since it may live in the same directory.
func pathWithoutInlay(t *testing.T) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	name := "git-inlay"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	var dirs []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			continue
		}
		dirs = append(dirs, dir)
	}
	if runtime.GOOS == "windows" {
		gitDir := filepath.Dir(gitPath)
		if !slices.ContainsFunc(dirs, func(d string) bool { return strings.EqualFold(filepath.Clean(d), gitDir) }) {
			t.Skip("git-inlay is installed beside git, which cannot be hidden without a symlink")
		}
		return strings.Join(dirs, string(os.PathListSeparator))
	}
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(gitPath, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	return strings.Join(append([]string{bin}, dirs...), string(os.PathListSeparator))
}

// The hook shims fall back to the binary that installed them when
// git-inlay is not on PATH, as under a GUI client, and still fail closed
// when that binary is gone too.
func TestHookShimFallsBackToInstallingBinary(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "claude")
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	f.inlayOK(f.root, "hooks", "install")
	shim := f.read(".git/hooks/pre-commit")
	start := strings.Index(shim, "|| bin='")
	if start < 0 {
		t.Fatalf("the shim records no fallback path:\n%s", shim)
	}
	start += len("|| bin='")
	end := start + strings.Index(shim[start:], "'")

	noBin := []string{"PATH=" + pathWithoutInlay(t)}
	f.git(f.root, "add", "-f", ".claude/settings.json")
	_, errOut, code := f.runWith(f.root, noBin, "git", "commit", "-q", "-m", "leak")
	if code == 0 {
		t.Fatal("a leaking commit passed with git-inlay off PATH")
	}
	contains(t, errOut, ".claude/settings.json: belongs to inlay claude", "pre-commit through the recorded binary")
	f.git(f.root, "reset", "-q", "--", ".claude/settings.json")
	f.write("CHANGELOG.md", "x\n")
	f.git(f.root, "add", "CHANGELOG.md")
	if _, errOut, code := f.runWith(f.root, noBin, "git", "commit", "-q", "-m", "clean"); code != 0 {
		t.Fatalf("a clean commit failed with git-inlay off PATH:\n%s", errOut)
	}

	f.write(".git/hooks/pre-commit", shim[:start]+"/nonexistent/git-inlay"+shim[end:])
	f.write("CHANGELOG.md", "y\n")
	f.git(f.root, "add", "CHANGELOG.md")
	_, errOut, code = f.runWith(f.root, noBin, "git", "commit", "-q", "-m", "blind")
	if code == 0 {
		t.Fatal("a commit passed with git-inlay nowhere to be found")
	}
	contains(t, errOut, "binary not on PATH or at /nonexistent/git-inlay; refusing", "fail-closed message")
}

// Attaching an inlay whose linked worktree was deleted from disk, not
// through git worktree remove, recovers its files and history into this
// worktree. The orphan stays stale for check, and prune keeps it for its
// commits on no remote until forced.
func TestAttachFromVanishedWorktree(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	wt := filepath.Join(f.base, "wts", "wt")
	f.git(f.root, "worktree", "add", "-q", wt, "-b", "feature")
	f.inlayOK(wt, "init", "notes")
	writeFile(t, filepath.Join(wt, "notes", "a.md"), "a\n")
	f.inlayOK(wt, "claim", "notes", "notes")
	f.inlayOK(wt, "notes", "commit", "-q", "-m", "first note")
	writeFile(t, filepath.Join(wt, "notes", "b.md"), "b\n")
	f.inlayOK(wt, "notes", "add", "notes/b.md")
	f.inlayOK(wt, "notes", "commit", "-q", "-m", "second note")
	history := f.inlayOK(wt, "notes", "log", "--format=%H %s")

	// Deleted by hand with its parent, so git still keeps the worktree's
	// administrative directory, whose gitdir file now names nothing, and
	// the inlay's core.worktree names a path git cannot even start in.
	if err := os.RemoveAll(filepath.Dir(wt)); err != nil {
		t.Fatal(err)
	}
	contains(t, f.inlayFails(f.root, 1, "check"), "worktree wt/wt, which no longer exists", "check after the worktree is deleted")
	if f.exists("notes") {
		t.Fatal("the main worktree has the inlay's files before attach")
	}

	out, errOut, code := f.inlay(f.root, "attach", "notes")
	if code != 0 {
		t.Fatalf("attach from a vanished worktree: exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	contains(t, out, "attached inlay notes from worktree wt/wt (branch main, 1 claims)", "attach output")
	contains(t, errOut, "inlay notes has no remote", "attach warning")
	if strings.Contains(errOut, "uncommitted changes") {
		t.Fatalf("attach reported uncommitted changes in a worktree that is gone:\n%s", errOut)
	}
	if f.read("notes/a.md") != "a\n" || f.read("notes/b.md") != "b\n" {
		t.Fatal("attach did not restore the inlay's files")
	}
	if got := f.inlayOK(f.root, "notes", "log", "--format=%H %s"); got != history {
		t.Fatalf("history after attach = %q, want %q", got, history)
	}
	if got := f.inlayOK(f.root, "notes", "status", "--porcelain"); got != "" {
		t.Fatalf("inlay status after attach = %q", got)
	}
	if got := f.git(f.root, "status", "--porcelain"); got != "" {
		t.Fatalf("host status after attach = %q", got)
	}
	if got := f.inlayOK(f.root, "ls"); got != "notes\tnotes/" {
		t.Fatalf("ls after attach = %q", got)
	}

	// The orphan's claims still bind every worktree until it is pruned,
	// and its commits are on no remote, so prune keeps it although this
	// worktree's copy now holds them.
	f.inlayOK(f.root, "hooks", "install")
	contains(t, f.inlayFails(f.root, 1, "check"), "worktree wt/wt, which no longer exists", "check after attach")
	out, errOut, code = f.inlay(f.root, "prune")
	if code != 1 {
		t.Fatalf("prune of the orphan: exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	contains(t, out, "wt/wt\tnotes\tkept (3 unpushed commit(s))", "prune of the orphan")
	contains(t, errOut, "notes has no remote; add one with:", "prune advice")
	contains(t, f.inlayOK(f.root, "prune", "--force"), "wt/wt\tnotes\tremoved (3 unpushed commit(s))", "forced prune")
	if f.exists(".git/inlays/wt") {
		t.Fatal("prune left the orphan's directory behind")
	}
	if _, _, code := f.run(f.root, "git", "check-ignore", "-q", "notes/a.md"); code != 0 {
		t.Fatal("the host stopped ignoring the recovered claim after prune")
	}
	contains(t, f.inlayOK(f.root, "check"), "ok: 1 inlay(s), 1 claim(s)", "check after prune")
}

// Pushing a branch deletion passes the host pre-push guard, which skips an
// update whose local object id is zero, even when the branch it deletes
// carries a leaked claimed path: deleting it is the fix.
func TestPushBranchDeletion(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "claude")
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	f.inlayOK(f.root, "claude", "commit", "-q", "-m", "settings")
	f.inlayOK(f.root, "hooks", "install")

	// The leak goes on a branch of its own; a mixed reset leaves main and
	// the file in the tree as they were.
	f.git(f.root, "add", "-f", ".claude/settings.json")
	f.git(f.root, "commit", "-q", "--no-verify", "-m", "leak")
	f.git(f.root, "branch", "leaky")
	f.git(f.root, "reset", "-q", "HEAD~1")
	contains(t, f.gitFails(f.root, "push", "-q", "origin", "leaky"), ".claude/settings.json: belongs to inlay claude", "pre-push of the leaky branch")
	f.git(f.root, "push", "-q", "--no-verify", "origin", "leaky")
	if got := f.git(f.root, "ls-remote", "--heads", "origin", "leaky"); got == "" {
		t.Fatal("the leaky branch did not reach the remote")
	}

	f.git(f.root, "push", "-q", "origin", ":leaky")
	if got := f.git(f.root, "ls-remote", "--heads", "origin", "leaky"); got != "" {
		t.Fatalf("the remote still has the deleted branch: %q", got)
	}
	if f.read(".claude/settings.json") != "{}\n" {
		t.Fatal("the inlay's file changed")
	}
}

// A claim whose name needs escaping in gitignore syntax, with '#', '!', a
// space and '[', is excluded in the host and included in the inlay as the
// literal path, not as a pattern matching a sibling.
func TestClaimNeedsEscaping(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	const dir = "a#b !c [d]"
	if err := os.MkdirAll(filepath.Join(f.root, dir), 0o755); err != nil {
		t.Skipf("the filesystem refuses %q: %v", dir, err)
	}
	f.write(dir+"/x.md", "x\n")
	f.inlayOK(f.root, "init", "odd")
	f.inlayOK(f.root, "claim", "odd", dir)
	f.inlayOK(f.root, "odd", "commit", "-q", "-m", "odd")

	if got := f.git(f.root, "status", "--porcelain"); got != "" {
		t.Fatalf("host status = %q, want clean", got)
	}
	if _, errOut, code := f.run(f.root, "git", "check-ignore", "-q", dir+"/x.md"); code != 0 {
		t.Fatalf("the host does not ignore %s/x.md: exit %d\n%s", dir, code, errOut)
	}
	if got, want := f.inlayOK(f.root, "odd", "ls-files", "-z"), ".git-inlay/odd.paths\x00"+dir+"/x.md\x00"; got != want {
		t.Fatalf("inlay ls-files = %q, want %q", got, want)
	}
	if got := f.inlayOK(f.root, "ls"); got != "odd\t"+dir+"/" {
		t.Fatalf("ls = %q", got)
	}

	// Unescaped, "[d]" would be a class matching "d", so this sibling
	// would be ignored by the host and tracked by the inlay.
	const sibling = "a#b !c d"
	f.write(sibling+"/y.md", "y\n")
	if _, _, code := f.run(f.root, "git", "check-ignore", "-q", sibling+"/y.md"); code != 1 {
		t.Fatalf("check-ignore of %s/y.md: exit %d, want 1 (not ignored)", sibling, code)
	}
	if got := f.inlayOK(f.root, "odd", "status", "--porcelain"); got != "" {
		t.Fatalf("inlay status with the sibling present = %q, want clean", got)
	}
	if err := os.RemoveAll(filepath.Join(f.root, sibling)); err != nil {
		t.Fatal(err)
	}

	f.inlayOK(f.root, "hooks", "install")
	contains(t, f.inlayOK(f.root, "check"), "ok: 1 inlay(s), 1 claim(s)", "check")
}

// The session-start briefing names this worktree's claims and how to record
// them, mentions missing guards and inlays held only by other worktrees,
// and says nothing where there is nothing to brief on.
func TestSessionStartBriefing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	plain := filepath.Join(f.base, "plain")
	if err := os.Mkdir(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{plain, f.root} {
		if got := f.inlayOK(dir, "hook", "session-start"); got != "" {
			t.Fatalf("briefing in %s = %q, want nothing", dir, got)
		}
	}
	f.inlayFails(f.root, 2, "hook", "session-start", "extra")

	f.inlayOK(f.root, "init", "team", "--no-hooks")
	f.write(".claude/settings.json", "{}\n")
	f.write("CLAUDE.local.md", "notes\n")
	f.inlayOK(f.root, "claim", "team", "CLAUDE.local.md", ".claude")
	f.inlayOK(f.root, "team", "commit", "-q", "-m", "claims") // attach checks out what is committed
	f.inlayOK(f.root, "init", "empty", "--no-hooks")
	sub := filepath.Join(f.root, "pkg")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got := f.inlayOK(sub, "hook", "session-start")
	contains(t, got, "  team: .claude/, CLAUDE.local.md\n", "briefing")
	contains(t, got, "  empty: (no claims yet)\n", "briefing")
	contains(t, got, "git inlay <name> commit", "briefing")
	contains(t, got, "git inlay hooks install", "briefing without guards")

	f.inlayOK(f.root, "hooks", "install")
	if got := f.inlayOK(f.root, "hook", "session-start"); strings.Contains(got, "hooks install") {
		t.Fatalf("briefing with guards installed still asks for them:\n%s", got)
	}
	if got := f.git(f.root, "status", "--porcelain"); got != "" {
		t.Fatalf("host status after the briefing = %q", got)
	}

	wt := filepath.Join(f.base, "wt")
	f.git(f.root, "worktree", "add", "-q", wt, "-b", "feature")
	got = f.inlayOK(wt, "hook", "session-start")
	contains(t, got, "other worktrees of this repository hold inlays that this one does not (empty, team)", "briefing in a worktree without inlays")
	// The pre-commit fix names the attach an inlay held elsewhere needs, and
	// anchors the path at the root, so it works from a subdirectory too.
	stray := filepath.Join(wt, "CLAUDE.local.md")
	writeFile(t, stray, "stray\n")
	f.git(wt, "add", "-f", "CLAUDE.local.md")
	contains(t, f.gitFails(wt, "commit", "-q", "-m", "leak"),
		`fix: git restore --staged -- ":(top,literal)CLAUDE.local.md"   then, to commit it to its inlay, move the file aside, run git inlay attach team`,
		"pre-commit in a worktree without the inlay")
	wtSub := filepath.Join(wt, "pkg")
	if err := os.Mkdir(wtSub, 0o755); err != nil {
		t.Fatal(err)
	}
	f.git(wtSub, "restore", "--staged", "--", ":(top,literal)CLAUDE.local.md")
	if got := f.git(wt, "diff", "--cached", "--name-only"); got != "" {
		t.Fatalf("restore --staged from a subdirectory left %q staged", got)
	}
	if err := os.Rename(stray, stray+".aside"); err != nil {
		t.Fatal(err)
	}
	f.inlayOK(wt, "attach", "team")
	if err := os.Rename(stray+".aside", stray); err != nil {
		t.Fatal(err)
	}
	f.inlayOK(wtSub, "team", "add", "--", ":(top,literal)CLAUDE.local.md")
	if got := f.inlayOK(wt, "team", "diff", "--cached", "--name-only"); got != "CLAUDE.local.md" {
		t.Fatalf("inlay add from a subdirectory staged %q", got)
	}
	got = f.inlayOK(wt, "hook", "session-start")
	contains(t, got, "  team: .claude/, CLAUDE.local.md\n", "briefing after attach")
	contains(t, got, "Other worktrees hold inlays that this one does not (empty)", "briefing after attach")
}
