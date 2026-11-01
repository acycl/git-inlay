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

// Scenario 6: linked worktrees. attach copies an inlay from the main
// worktree, the copies commit independently, the host exclude is the union
// across worktrees, and prune removes inlays once their worktree is gone,
// keeping any with unpushed commits unless forced.
func TestWorktreeAttachAndPrune(t *testing.T) {
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
	contains(t, f.inlayOK(f.root, "check"), "ok:", "check in main with a worktree claim")

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
// --fetch for the remote's view; --porcelain on ls, which and status gives
// stable tab-separated lines.
func TestStatusAndPorcelain(t *testing.T) {
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
	if got := f.inlayOK(f.root, "which", "--porcelain", ".claude/new.md", "README.md"); got != ".claude/new.md\tclaude\nREADME.md\thost" {
		t.Fatalf("which --porcelain = %q", got)
	}
}

// detach never deletes a file the host also tracks, which an upstream
// commit at a claimed path can bring about; check reports that instead.
func TestDetachLeavesHostTrackedFiles(t *testing.T) {
	f := newFixture(t)
	private := f.bare("claude-private")
	f.inlayOK(f.root, "init", "claude", "--remote", private)
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
