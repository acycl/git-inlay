package gitx

// The tests in this file are the mechanics tests of docs/design.md section 8.
// Each one pins down a git behaviour the design depends on, using raw git and
// no production code, so a failure here means the design must change before
// code is written against it.

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
)

// TestInlaySharesHostWorktree checks mechanic 1: a bare-initialized git dir
// with core.bare=false, driven with explicit --git-dir and --work-tree, tracks
// files in the host's working tree while the host, with the paths in its
// info/exclude, sees nothing.
func TestInlaySharesHostWorktree(t *testing.T) {
	h := newHost(t)
	ov := h.newInlay(t, "claude", "/*\n!/.claude/\n")
	h.exclude(t, "/.claude\n")

	h.write(t, ".claude/settings.json", "{}\n")

	if got := h.git(t, "status", "--porcelain"); got != "" {
		t.Fatalf("host status after creating inlay file = %q, want empty", got)
	}
	if got, want := ov.git(t, "status", "--porcelain"), "?? .claude/"; got != want {
		t.Fatalf("inlay status = %q, want %q", got, want)
	}

	ov.git(t, "add", ".claude")
	ov.git(t, "commit", "-q", "-m", "claude settings")

	if got := ov.git(t, "ls-files"); got != ".claude/settings.json" {
		t.Fatalf("inlay ls-files = %q, want .claude/settings.json", got)
	}
	if got := h.git(t, "ls-files"); got != "README.md" {
		t.Fatalf("host ls-files = %q, want README.md only", got)
	}
	if got := h.git(t, "status", "--porcelain"); got != "" {
		t.Fatalf("host status after inlay commit = %q, want empty", got)
	}

	// A modification is visible to the inlay and recoverable with checkout.
	h.write(t, ".claude/settings.json", "{\"changed\":true}\n")
	if got, want := ov.git(t, "status", "--porcelain"), " M .claude/settings.json"; got != want {
		t.Fatalf("inlay status after edit = %q, want %q", got, want)
	}
	ov.git(t, "checkout", "--", ".claude")
	if got := h.read(t, ".claude/settings.json"); got != "{}\n" {
		t.Fatalf("after checkout, settings.json = %q, want original", got)
	}
}

// TestCommonDirExcludeAppliesInLinkedWorktree checks mechanic 2: a pattern in
// the common dir's info/exclude is honoured inside a linked worktree, which is
// what lets one managed block cover every worktree of the host. It also pins
// the rev-parse answers the worktree key is derived from.
func TestCommonDirExcludeAppliesInLinkedWorktree(t *testing.T) {
	h := newHost(t)
	h.exclude(t, "/.claude\n")

	wtPath := filepath.Join(filepath.Dir(h.root), "wt")
	h.git(t, "worktree", "add", "-q", "-b", "feature", wtPath)
	wt := &repo{root: wtPath, env: h.env}

	wt.write(t, ".claude/settings.json", "{}\n")
	if got := wt.git(t, "status", "--porcelain"); got != "" {
		t.Fatalf("linked worktree status = %q, want empty", got)
	}
	if !wt.ignored(t, ".claude/settings.json") {
		t.Fatal("linked worktree does not ignore a path excluded in the common dir")
	}

	// Git prints forward slashes on every platform, so convert its output to
	// native separators before comparing with filepath-built paths.
	hostGit := realPath(t, filepath.Join(h.root, ".git"))
	if got := filepath.FromSlash(wt.git(t, "rev-parse", "--path-format=absolute", "--git-common-dir")); got != hostGit {
		t.Fatalf("common dir from linked worktree = %q, want %q", got, hostGit)
	}
	wantGitDir := filepath.Join(hostGit, "worktrees", "wt")
	if got := filepath.FromSlash(wt.git(t, "rev-parse", "--path-format=absolute", "--git-dir")); got != wantGitDir {
		t.Fatalf("git dir from linked worktree = %q, want %q", got, wantGitDir)
	}
}

// TestHostCleanLeavesInlayGitDir checks mechanic 3: git clean with -x
// removes the inlay's files from the tree, because the host ignores them,
// but never touches .git, so the inlay git dir survives and a checkout
// restores the files.
func TestHostCleanLeavesInlayGitDir(t *testing.T) {
	h := newHost(t)
	ov := h.newInlay(t, "claude", "/*\n!/.claude/\n")
	h.exclude(t, "/.claude\n")
	h.write(t, ".claude/settings.json", "{}\n")
	ov.git(t, "add", ".claude")
	ov.git(t, "commit", "-q", "-m", "claude settings")

	for _, flags := range []string{"-xfdq", "-xffdq"} {
		h.git(t, "clean", flags)

		if _, err := os.Stat(filepath.Join(h.root, ".claude", "settings.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("after clean %s, settings.json stat error = %v, want not exist", flags, err)
		}
		if _, err := os.Stat(ov.gitDir); err != nil {
			t.Fatalf("after clean %s, inlay git dir: %v", flags, err)
		}
		if got, want := ov.git(t, "status", "--porcelain"), " D .claude/settings.json"; got != want {
			t.Fatalf("inlay status after clean %s = %q, want %q", flags, got, want)
		}

		ov.git(t, "checkout", "--", ".")
		if got := h.read(t, ".claude/settings.json"); got != "{}\n" {
			t.Fatalf("after restore, settings.json = %q", got)
		}
	}
}

// TestNegationChainReincludesNestedClaims checks mechanic 4: the inlay
// exclude produced by the algorithm in docs/design.md section 4.4 makes
// plain git see exactly the claimed paths, including a .claude directory
// nested two levels down, and nothing the host owns.
func TestNegationChainReincludesNestedClaims(t *testing.T) {
	const chain = `/*
!/.git-inlay/
/.git-inlay/*
!/docs/
/docs/*
!/packages/
/packages/*
!/packages/foo/
/packages/foo/*
!/.claude/
!/.git-inlay/claude.paths
!/docs/research/
!/packages/foo/.claude/
`
	h := newHost(t)
	ov := h.newInlay(t, "claude", chain)
	h.exclude(t, "/.git-inlay\n/.claude\n/docs/research\n/packages/foo/.claude\n")

	claimed := []string{
		".claude/settings.json",
		".git-inlay/claude.paths",
		"docs/research/notes.md",
		"packages/foo/.claude/settings.json",
	}
	hostOwned := []string{
		"docs/README.md",
		"packages/bar/.claude/settings.json",
		"packages/foo/main.go",
	}
	// Another inlay's manifest: excluded by the host, and not this inlay's.
	const foreign = ".git-inlay/other.paths"

	for _, p := range slices.Concat(claimed, hostOwned, []string{foreign}) {
		h.write(t, p, p+"\n")
	}

	var want []string
	for _, p := range claimed {
		want = append(want, "?? "+p)
	}
	got := strings.Split(ov.git(t, "status", "--porcelain", "-uall"), "\n")
	if !slices.Equal(got, want) {
		t.Fatalf("inlay status -uall =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	for _, p := range slices.Concat(hostOwned, []string{foreign}) {
		if !ov.ignored(t, p) {
			t.Errorf("inlay does not ignore host-owned path %s", p)
		}
	}
	for _, p := range claimed {
		if ov.ignored(t, p) {
			t.Errorf("inlay ignores claimed path %s", p)
		}
	}

	h.git(t, "add", "-A")
	wantHost := slices.Concat([]string{"README.md"}, hostOwned)
	slices.Sort(wantHost)
	if gotHost := strings.Split(h.git(t, "ls-files"), "\n"); !slices.Equal(gotHost, wantHost) {
		t.Fatalf("host ls-files after add -A = %v, want %v", gotHost, wantHost)
	}
	if !h.ignored(t, foreign) {
		t.Errorf("host does not ignore %s", foreign)
	}
}

// TestHostExcludeWithoutSlashMatchesFileOrDirectory pins the reason the host
// managed block omits trailing slashes: a pattern without one matches a file
// or a directory of that name, so a claimed directory replaced by a file is
// still hidden from the host.
func TestHostExcludeWithoutSlashMatchesFileOrDirectory(t *testing.T) {
	h := newHost(t)
	h.exclude(t, "/.claude\n")

	h.write(t, ".claude", "a file, not a directory\n")
	if got := h.git(t, "status", "--porcelain"); got != "" {
		t.Fatalf("host status with .claude as a file = %q, want empty", got)
	}
	if err := os.Remove(filepath.Join(h.root, ".claude")); err != nil {
		t.Fatal(err)
	}
	h.write(t, ".claude/settings.json", "{}\n")
	if got := h.git(t, "status", "--porcelain"); got != "" {
		t.Fatalf("host status with .claude as a directory = %q, want empty", got)
	}
}

// TestWorktreeAddRefusesNonEmptyDirectory pins the reason inlays do not use
// git's own linked worktrees (docs/design.md section 7.4): a worktree cannot
// be added at a path that already has content, and the host worktree root
// always does.
func TestWorktreeAddRefusesNonEmptyDirectory(t *testing.T) {
	h := newHost(t)
	occupied := filepath.Join(filepath.Dir(h.root), "occupied")
	if err := os.MkdirAll(occupied, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(occupied, "existing.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.run("worktree", "add", "-q", "-b", "feature", occupied); err == nil {
		t.Fatal("git worktree add into a non-empty directory succeeded; the design could use native worktrees after all")
	}
}

// repo is a git repository under test, addressed by its working tree and, for
// an inlay, an explicit git dir.
type repo struct {
	root   string   // working tree
	gitDir string   // empty for a repository discovered from root
	env    []string // isolated environment for git
}

// newHost creates a host repository with one commit in a fresh temp dir and
// an isolated git environment.
func newHost(t *testing.T) *repo {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "host")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	h := &repo{root: root, env: isolatedEnv(t, base)}
	h.git(t, "init", "-q", "-b", "main")
	h.write(t, "README.md", "# host\n")
	h.git(t, "add", "README.md")
	h.git(t, "commit", "-q", "-m", "init")
	return h
}

// newInlay creates an inlay git dir under the host's .git in the layout
// from docs/design.md section 4.1, configured as in section 4.2, with the given
// info/exclude content.
func (h *repo) newInlay(t *testing.T, name, exclude string) *repo {
	t.Helper()
	gitDir := filepath.Join(h.root, ".git", "inlays", "main", name)
	h.git(t, "init", "-q", "--bare", "-b", "main", gitDir)
	ov := &repo{root: h.root, gitDir: gitDir, env: h.env}
	ov.git(t, "config", "core.bare", "false")
	ov.git(t, "config", "core.worktree", h.root)
	writeFile(t, filepath.Join(gitDir, "info", "exclude"), exclude)
	return ov
}

// exclude replaces the repository's info/exclude with the given content.
func (r *repo) exclude(t *testing.T, content string) {
	t.Helper()
	dir := r.gitDir
	if dir == "" {
		dir = filepath.Join(r.root, ".git")
	}
	writeFile(t, filepath.Join(dir, "info", "exclude"), content)
}

// git runs a git command and fails the test on error. Output has trailing
// newlines removed and nothing else, so porcelain status columns survive.
func (r *repo) git(t *testing.T, args ...string) string {
	t.Helper()
	out, err := r.run(args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// run runs a git command in the working tree, adding explicit --git-dir and
// --work-tree flags for an inlay, and returns stdout with trailing newlines
// removed.
func (r *repo) run(args ...string) (string, error) {
	if r.gitDir != "" {
		args = slices.Concat([]string{"--git-dir=" + r.gitDir, "--work-tree=" + r.root}, args)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = r.root
	cmd.Env = r.env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// ignored reports whether git check-ignore considers the path ignored.
func (r *repo) ignored(t *testing.T, path string) bool {
	t.Helper()
	_, err := r.run("check-ignore", "-q", path)
	if err == nil {
		return true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false
	}
	t.Fatalf("git check-ignore %s: %v", path, err)
	return false
}

// write creates a file under the working tree, creating parent directories.
func (r *repo) write(t *testing.T, rel, content string) {
	t.Helper()
	writeFile(t, filepath.Join(r.root, filepath.FromSlash(rel)), content)
}

// read returns the content of a file under the working tree.
func (r *repo) read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// isolatedEnv builds a git environment that ignores the developer's own
// configuration and any inherited repository variables.
func isolatedEnv(t *testing.T, base string) []string {
	t.Helper()
	home := filepath.Join(base, "home")
	cfg := filepath.Join(home, "gitconfig")
	writeFile(t, cfg, "[user]\n\tname = test\n\temail = test@example.com\n[init]\n\tdefaultBranch = main\n")
	return []string{
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=" + cfg,
		"GIT_CONFIG_NOSYSTEM=1",
		"LC_ALL=C",
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + os.TempDir(),
	}
}

// writeFile writes content to path, creating parent directories.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// realPath resolves symlinks so paths compare equal to what git reports.
func realPath(t *testing.T, path string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestCatFileBatchReportsMissing pins the cat-file --batch answers the
// manifest reader relies on: an unborn HEAD, a commit without the file and
// an unknown revision all say "missing" with exit status 0, while a present
// file, reached through a branch or an annotated tag, comes back as a blob
// header followed by its content and a newline.
func TestCatFileBatchReportsMissing(t *testing.T) {
	h := newHost(t)
	catFile := func(dir, obj string) string {
		t.Helper()
		cmd := exec.Command("git", "-C", dir, "cat-file", "--batch")
		cmd.Env = h.env
		cmd.Stdin = strings.NewReader(obj + "\n")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("cat-file --batch %s: %v", obj, err)
		}
		return string(out)
	}
	const obj = "HEAD:.git-inlay/x.paths"

	empty := filepath.Join(filepath.Dir(h.root), "empty")
	h.git(t, "init", "-q", "-b", "main", empty)
	if got := catFile(empty, obj); got != obj+" missing\n" {
		t.Fatalf("unborn HEAD: %q", got)
	}
	if got := catFile(h.root, obj); got != obj+" missing\n" {
		t.Fatalf("commit without the file: %q", got)
	}
	if got := catFile(h.root, "nosuchrev:x"); got != "nosuchrev:x missing\n" {
		t.Fatalf("unknown revision: %q", got)
	}

	h.write(t, ".git-inlay/x.paths", "docs/\n")
	h.git(t, "add", ".git-inlay")
	h.git(t, "commit", "-q", "-m", "manifest")
	h.git(t, "tag", "-a", "-m", "release", "v1")
	for _, rev := range []string{"HEAD", "v1"} {
		header, body, _ := strings.Cut(catFile(h.root, rev+":.git-inlay/x.paths"), "\n")
		if f := strings.Fields(header); len(f) != 3 || f[1] != "blob" || body != "docs/\n\n" {
			t.Fatalf("%s: header %q, body %q", rev, header, body)
		}
	}
}

// TestConfigHookRunsDespiteHooksPath pins the config-defined hooks of git
// 2.54 that hooks install falls back to under a hook manager: a hook
// defined in the repository config runs although core.hooksPath points
// elsewhere, its shell command gets the hook's arguments appended as "$@"
// and the hook's stdin, its failure refuses the commit even though the
// hook in core.hooksPath succeeds, and hook.<name>.enabled=false turns it
// off.
func TestConfigHookRunsDespiteHooksPath(t *testing.T) {
	if ok, err := (Runner{}).AtLeast(2, 54); err != nil || !ok {
		t.Skip("config-defined hooks need git 2.54 or newer")
	}
	h := newHost(t)
	base := filepath.Dir(h.root)
	remote := filepath.Join(base, "remote.git")
	h.git(t, "init", "-q", "--bare", "-b", "main", remote)
	h.git(t, "remote", "add", "origin", remote)

	managed := filepath.Join(base, "managed")
	writeFile(t, filepath.Join(managed, "pre-commit"), "#!/bin/sh\necho managed >> \"$0.ran\"\n")
	if err := os.Chmod(filepath.Join(managed, "pre-commit"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.git(t, "config", "core.hooksPath", managed)
	record := filepath.ToSlash(filepath.Join(base, "record"))
	// The command ends without "$@", as hooks.Command does: git appends it.
	h.git(t, "config", "hook.probe.command", `cat > '`+record+`.stdin'; printf '%s\n' "$#" > '`+record+`.args'; printf '%s\n' >> '`+record+`.args'`)
	h.git(t, "config", "hook.probe.event", "pre-push")
	h.git(t, "config", "hook.refuse.command", "exit 1")
	h.git(t, "config", "hook.refuse.event", "pre-commit")

	h.write(t, "a.txt", "a\n")
	h.git(t, "add", "a.txt")
	if _, err := h.run("commit", "-q", "-m", "a"); err == nil {
		t.Fatal("a commit passed although a config-defined pre-commit hook failed")
	}
	h.git(t, "config", "hook.refuse.enabled", "false")
	h.git(t, "commit", "-q", "-m", "a")
	if _, err := os.Stat(filepath.Join(managed, "pre-commit.ran")); err != nil {
		t.Fatal("the hook in core.hooksPath did not run beside the config-defined one")
	}

	h.git(t, "push", "-q", "origin", "main")
	args, err := os.ReadFile(record + ".args")
	if err != nil {
		t.Fatal("the config-defined pre-push hook did not run despite core.hooksPath")
	}
	if got, want := string(args), "2\norigin\n"+remote+"\n"; filepath.ToSlash(got) != filepath.ToSlash(want) {
		t.Fatalf("config-defined hook arguments = %q, want %q", got, want)
	}
	stdin, err := os.ReadFile(record + ".stdin")
	if err != nil || !strings.HasPrefix(string(stdin), "refs/heads/main ") {
		t.Fatalf("config-defined hook stdin = %q, %v", stdin, err)
	}
}

// TestWorktreeGitdirFile pins the file git-inlay reads to find a linked
// worktree's root and to tell whether it is gone, without a process:
// $C/worktrees/<id>/gitdir names the worktree's .git file, as an absolute
// path, or since git 2.48 under worktree.useRelativePaths as one relative
// to $C/worktrees/<id>. Git itself calls the worktree prunable once that
// file is missing, unless $C/worktrees/<id>/locked exists.
func TestWorktreeGitdirFile(t *testing.T) {
	h := newHost(t)
	base := filepath.Dir(h.root)
	cases := []struct {
		id       string
		relative bool
	}{{"abs", false}, {"rel", true}}
	if ok, err := (Runner{}).AtLeast(2, 48); err != nil || !ok {
		cases = cases[:1]
	}
	for _, c := range cases {
		wt := filepath.Join(base, c.id)
		args := []string{"worktree", "add", "-q", "-b", c.id, wt}
		if c.relative {
			args = append([]string{"-c", "worktree.useRelativePaths=true"}, args...)
		}
		h.git(t, args...)
		admin := filepath.Join(h.root, ".git", "worktrees", c.id)
		data, err := os.ReadFile(filepath.Join(admin, "gitdir"))
		if err != nil {
			t.Fatal(err)
		}
		p := strings.TrimSpace(string(data))
		if filepath.IsAbs(p) == c.relative {
			t.Fatalf("%s: gitdir file = %q, relative = %v", c.id, p, c.relative)
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(admin, p)
		}
		if got, want := realPath(t, p), realPath(t, filepath.Join(wt, ".git")); got != want {
			t.Fatalf("%s: gitdir file resolves to %s, want %s", c.id, got, want)
		}

		if err := os.RemoveAll(wt); err != nil {
			t.Fatal(err)
		}
		list := h.git(t, "worktree", "list", "--porcelain")
		if !strings.Contains(list, "prunable") {
			t.Fatalf("%s: git does not call a deleted worktree prunable:\n%s", c.id, list)
		}
		// git worktree lock writes this file; its presence is the lock.
		writeFile(t, filepath.Join(admin, "locked"), "")
		if list := h.git(t, "worktree", "list", "--porcelain"); strings.Contains(list, "prunable") {
			t.Fatalf("%s: git calls a locked worktree prunable:\n%s", c.id, list)
		}
		if err := os.Remove(filepath.Join(admin, "locked")); err != nil {
			t.Fatal(err)
		}
		h.git(t, "worktree", "prune")
	}
}

// TestStatusPorcelainV2Branch pins the lines status reads from one
// git status --porcelain=v2 --branch: the branch, "(detached)" without
// one, the upstream and "+ahead -behind", one line per changed path and
// "? " per untracked one. An upstream whose tracking ref is gone keeps
// its branch.upstream line but loses branch.ab.
func TestStatusPorcelainV2Branch(t *testing.T) {
	h := newHost(t)
	remote := filepath.Join(filepath.Dir(h.root), "remote.git")
	h.git(t, "init", "-q", "--bare", "-b", "main", remote)
	h.git(t, "remote", "add", "origin", remote)
	h.git(t, "push", "-q", "-u", "origin", "main")
	h.write(t, "b.txt", "b\n")
	h.git(t, "add", "b.txt")
	h.git(t, "commit", "-q", "-m", "b")
	h.write(t, "README.md", "changed\n")
	h.write(t, "c.txt", "c\n")

	lines := strings.Split(h.git(t, "status", "--porcelain=v2", "--branch"), "\n")
	for _, want := range []string{"# branch.head main", "# branch.upstream origin/main", "# branch.ab +1 -0", "? c.txt"} {
		if !slices.Contains(lines, want) {
			t.Fatalf("status lacks %q:\n%s", want, strings.Join(lines, "\n"))
		}
	}
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "1 .M ") && strings.HasSuffix(l, " README.md") }) {
		t.Fatalf("status lacks the changed README.md:\n%s", strings.Join(lines, "\n"))
	}

	h.git(t, "update-ref", "-d", "refs/remotes/origin/main")
	out := h.git(t, "status", "--porcelain=v2", "--branch")
	if !strings.Contains(out, "# branch.upstream origin/main") || strings.Contains(out, "# branch.ab") {
		t.Fatalf("status with a gone upstream:\n%s", out)
	}

	h.git(t, "checkout", "-q", "--detach")
	if out := h.git(t, "status", "--porcelain=v2", "--branch"); !strings.Contains(out, "# branch.head (detached)") {
		t.Fatalf("status when detached:\n%s", out)
	}
}

// TestExcludeMatchesNonUTF8Bytes pins that git matches an exclude pattern
// byte for byte, so a claim whose name is not valid UTF-8 (a Latin-1 name
// on Linux) is ignored only if the pattern carries the same raw bytes.
// macOS and Windows refuse such names.
func TestExcludeMatchesNonUTF8Bytes(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("the filesystem refuses names that are not valid UTF-8")
	}
	h := newHost(t)
	name := "caf\xe9"
	if err := os.WriteFile(filepath.Join(h.root, name), []byte("x\n"), 0o644); err != nil {
		t.Skipf("the filesystem refuses %q: %v", name, err)
	}
	h.exclude(t, "/"+name+"\n")
	if !h.ignored(t, name) {
		t.Fatal("a pattern with the raw bytes does not ignore the file")
	}
	h.exclude(t, "/caf�\n")
	if h.ignored(t, name) {
		t.Fatal("a pattern with U+FFFD in place of the byte ignores the file")
	}
}

// TestHostNegationExposesHostFilesToInlay pins the limit of the inlay's
// exclude file: a .gitignore in the tree outranks info/exclude, so a host
// pattern that re-includes a path makes the inlay see that host file as
// untracked, where git clean reaches it. That holds for a file at the root
// and for one directly inside a directory the negation chain re-includes
// on the way to a claim; a deeper host file stays hidden behind its
// excluded parent.
func TestHostNegationExposesHostFilesToInlay(t *testing.T) {
	h := newHost(t)
	ov := h.newInlay(t, "claude", "/*\n!/docs/\n/docs/*\n!/docs/research/\n")
	h.exclude(t, "/docs/research\n")
	h.write(t, ".gitignore", "*.cfg\n!sample.cfg\n")
	h.write(t, "sample.cfg", "KEY=\n")
	h.write(t, "docs/.gitignore", "!keep.md\n")
	h.write(t, "docs/keep.md", "x\n")
	h.write(t, "src/.gitignore", "!deep.md\n")
	h.write(t, "src/deep.md", "x\n")
	h.write(t, "docs/research/notes.md", "n\n")

	want := "docs/keep.md\ndocs/research/notes.md\nsample.cfg"
	if got := ov.git(t, "ls-files", "--others", "--exclude-standard"); got != want {
		t.Fatalf("inlay untracked files =\n%s\nwant\n%s", got, want)
	}

	ov.git(t, "clean", "-fdq")
	for _, p := range []string{"sample.cfg", "docs/keep.md"} {
		if _, err := os.Stat(filepath.Join(h.root, filepath.FromSlash(p))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("after the inlay's clean -fd, %s stat error = %v, want not exist", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(h.root, "src", "deep.md")); err != nil {
		t.Errorf("the inlay's clean -fd reached a host file behind an excluded directory: %v", err)
	}
}
