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
	"strings"
	"testing"
)

var binDir string

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
	build := exec.Command("go", "build", "-o", bin, "../../cmd/git-inlay")
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
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = f.env
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
	contains(t, f.inlayOK(f.root, "check"), "ok:", "check")
}

// Scenario 4: claims are refused for host-tracked paths and across inlays.
func TestClaimRefusals(t *testing.T) {
	f := newFixture(t)
	f.write("docs/README.md", "d\n")
	f.git(f.root, "add", "docs")
	f.git(f.root, "commit", "-q", "-m", "docs")
	f.inlayOK(f.root, "init", "team")
	f.inlayOK(f.root, "init", "me")

	contains(t, f.inlayFails(f.root, 1, "claim", "team", "README.md"), "tracked by the host", "claiming a tracked file")
	contains(t, f.inlayFails(f.root, 1, "claim", "team", "docs"), "tracked by the host", "claiming a tracked directory")
	f.inlayOK(f.root, "claim", "team", "private/notes/")
	contains(t, f.inlayFails(f.root, 1, "claim", "me", "private/notes/today.md"), "already owned by inlay team", "claiming under another inlay")
	contains(t, f.inlayFails(f.root, 1, "claim", "me", "private/"), "would contain private/notes/, which inlay team claims", "claiming above another inlay")
	_, errOut, code := f.inlay(f.root, "claim", "team", "private/notes/deep.md")
	if code != 0 || !strings.Contains(errOut, "already covered") {
		t.Fatalf("redundant claim: exit %d, stderr %q", code, errOut)
	}
	contains(t, f.inlayFails(f.root, 2, "init", "status"), "cannot name an inlay", "reserved name")
	f.inlayFails(f.root, 2, "init", "Bad_Name")
	contains(t, f.inlayOK(f.root, "check"), "ok: 2 inlay(s), 1 claim(s)", "check")
}

// Scenario 10: passthrough runs git against the inlay and refuses bad names.
func TestPassthrough(t *testing.T) {
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

// Scenario 5: an inlay remote that resolves to a host remote is refused.
func TestRemoteGuard(t *testing.T) {
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

// Scenario 8: with core.hooksPath set, install prints the snippet and writes nothing.
func TestHooksPathManaged(t *testing.T) {
	f := newFixture(t)
	f.git(f.root, "config", "core.hooksPath", ".husky")
	if err := os.MkdirAll(filepath.Join(f.root, ".husky"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := f.inlayOK(f.root, "hooks", "install")
	contains(t, out, "core.hooksPath", "install output")
	contains(t, out, "husky manages", "install output")
	if f.exists(".git/hooks/pre-commit") {
		t.Fatal("install wrote a shim despite core.hooksPath")
	}
	contains(t, f.inlayOK(f.root, "hooks", "status"), "managed", "status")
}

// A hook that is not ours is left alone and the user is told what to add.
func TestForeignHookLeftAlone(t *testing.T) {
	f := newFixture(t)
	f.write(".git/hooks/pre-push", "#!/bin/sh\nexit 0\n")
	out := f.inlayOK(f.root, "hooks", "install")
	contains(t, out, "not managed by git-inlay", "install output")
	if f.read(".git/hooks/pre-push") != "#!/bin/sh\nexit 0\n" {
		t.Fatal("foreign pre-push hook was modified")
	}
	contains(t, f.read(".git/hooks/pre-commit"), "git-inlay hook pre-commit", "pre-commit shim")
}

// Unclaim returns paths to the host's view without touching the files.
func TestUnclaim(t *testing.T) {
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
}

// A host .gitignore that ignores dotfiles but re-includes a path cannot be
// overridden by the managed block, so claiming that path is refused; the
// manifest directory itself, which the same pattern ignores, is still
// staged.
func TestClaimRefusesHostNegation(t *testing.T) {
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
	f := newFixture(t)
	f.git(f.root, "config", "--global", "core.excludesFile", filepath.Join(f.base, "home", "does-not-exist"))
	f.inlayOK(f.root, "init", "claude")
	f.write(".claude/settings.json", "{}\n")
	f.inlayOK(f.root, "claim", "claude", ".claude")
	if got, want := f.inlayOK(f.root, "claude", "ls-files"), ".claude/settings.json\n.git-inlay/claude.paths"; got != want {
		t.Fatalf("inlay ls-files = %q, want %q", got, want)
	}
}
