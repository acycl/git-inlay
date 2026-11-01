package cli_test

// Day-in-the-life scenarios on a host shaped like an open-source monorepo.
// They automate the dogfooding step from PLAN.md section 9: a team keeps
// its Claude Code settings interleaved with the project's public .claude
// directory, one person keeps research notes, and ordinary git work on the
// host must never disturb either.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

const publicSkill = "---\nname: review\n---\nPublic review skill.\n"

// inlays are the two inlays dogfood attaches.
var inlays = []string{"team", "research"}

// private are the files the inlays own, keyed by path: Claude Code settings
// and an internal skill interleaved with the public .claude directory, a
// nested package's settings, a local guide the host already ignores, and
// research notes.
var private = map[string]string{
	".claude/settings.json":                    "{\"permissions\":{\"allow\":[\"Bash(go test:*)\"]}}\n",
	".claude/skills/internal/release/SKILL.md": "---\nname: release\n---\nHow we cut releases.\n",
	"CLAUDE.local.md":                          "# Team notes\n",
	"packages/api/.claude/settings.json":       "{\"permissions\":{\"allow\":[\"Bash(go run:*)\"]}}\n",
	"docs/research/2026-10-31.md":              "# Findings\n",
}

// monorepo reshapes the fixture's host into an open-source monorepo: a
// committed .gitignore with local-file rules, a public CLAUDE.md and
// .claude/skills/, packages with their own directories, a submodule, a
// release branch and a tag that predate the agent config, a foreign hook,
// and the global excludes entry Claude Code writes for settings.local.json.
func (f *fixture) monorepo() {
	t := f.t
	t.Helper()
	f.git(f.root, "config", "--global", "protocol.file.allow", "always")
	writeFile(t, filepath.Join(f.base, "home", ".config", "git", "ignore"), "**/.claude/settings.local.json\n")

	dep := filepath.Join(f.base, "dep")
	if err := os.Mkdir(dep, 0o755); err != nil {
		t.Fatal(err)
	}
	f.git(dep, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dep, "dep.go"), "package dep\n")
	f.git(dep, "add", "dep.go")
	f.git(dep, "commit", "-q", "-m", "dep")

	f.write(".gitignore", "/bin/\ndist/\nnode_modules/\n")
	f.write("go.mod", "module example.com/mono\n\ngo 1.22\n")
	f.write("packages/api/main.go", "package main\n\nfunc main() {}\n")
	f.write("packages/api/README.md", "# api\n")
	f.write("packages/web/index.html", "<!doctype html>\n")
	f.write("tools/ci/lint.sh", "#!/bin/sh\nexit 0\n")
	f.write("docs/README.md", "# docs\n")
	f.git(f.root, "submodule", "add", "--quiet", dep, "vendor/dep")
	f.git(f.root, "add", "-A")
	f.git(f.root, "commit", "-q", "-m", "scaffold")
	f.git(f.root, "tag", "v1.0.0")

	f.git(f.root, "checkout", "-q", "-b", "release/1.x")
	f.write("packages/api/README.md", "# api\n\nFixed in 1.x.\n")
	f.git(f.root, "commit", "-q", "-a", "-m", "fix api docs")
	f.git(f.root, "checkout", "-q", "main")

	f.write(".gitignore", "/bin/\ndist/\nnode_modules/\n*.local.json\nCLAUDE.local.md\n.env\n.env.*\n")
	f.write("CLAUDE.md", "# Project guide\n")
	f.write(".claude/skills/review/SKILL.md", publicSkill)
	f.git(f.root, "add", "-A")
	f.git(f.root, "commit", "-q", "-m", "agent config")
	f.git(f.root, "push", "-q", "origin", "main", "release/1.x", "v1.0.0")

	hook := filepath.Join(f.root, ".git", "hooks", "commit-msg")
	writeFile(t, hook, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
}

// dogfood attaches the inlays team and research with private's files,
// pushes both to their own remotes and installs the host guards. It returns
// the contents the files must keep and everything claim printed to stderr.
func (f *fixture) dogfood() (snap map[string]string, warnings string) {
	f.t.Helper()
	f.inlayOK(f.root, "init", "team", "--remote", f.bare("team"))
	f.inlayOK(f.root, "init", "research", "--remote", f.bare("research"))
	snap = map[string]string{}
	for rel, content := range private {
		f.write(rel, content)
		snap[rel] = content
	}
	// Claude Code's per-machine file; the global excludes keep it out.
	f.write("packages/api/.claude/settings.local.json", "{}\n")
	var w strings.Builder
	claim := func(dir string, args ...string) {
		f.t.Helper()
		_, errOut, code := f.inlay(dir, append([]string{"claim"}, args...)...)
		if code != 0 {
			f.t.Fatalf("claim %s: exit %d\n%s", strings.Join(args, " "), code, errOut)
		}
		w.WriteString(errOut)
	}
	claim(f.root, "team", ".claude/settings.json", ".claude/skills/internal", "CLAUDE.local.md")
	claim(filepath.Join(f.root, "packages", "api"), "team", ".claude")
	claim(f.root, "research", "docs/research")
	f.inlayOK(f.root, "team", "commit", "-q", "-m", "team settings")
	f.inlayOK(f.root, "research", "commit", "-q", "-m", "notes")
	f.inlayOK(f.root, "team", "push", "-q", "-u", "origin", "main")
	f.inlayOK(f.root, "research", "push", "-q", "-u", "origin", "main")
	f.inlayOK(f.root, "hooks", "install")
	return snap, w.String()
}

// steady asserts the state every host operation must leave behind in dir:
// the host and every inlay report clean, check passes, and the private
// files are byte-identical to snap.
func (f *fixture) steady(dir, step string, snap map[string]string) {
	f.t.Helper()
	if got := f.git(dir, "status", "--porcelain"); got != "" {
		f.t.Fatalf("after %s: host status = %q, want clean", step, got)
	}
	for _, in := range inlays {
		if got := f.inlayOK(dir, in, "status", "--porcelain"); got != "" {
			f.t.Fatalf("after %s: inlay %s status = %q, want clean", step, in, got)
		}
	}
	if _, errOut, code := f.inlay(dir, "check"); code != 0 {
		f.t.Fatalf("after %s: check exited %d\n%s", step, code, errOut)
	}
	for rel, want := range snap {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			f.t.Fatalf("after %s: %s: %v", step, rel, err)
		}
		if string(b) != want {
			f.t.Fatalf("after %s: %s = %q, want %q", step, rel, b, want)
		}
	}
}

// Private and public files share .claude/ without either side seeing the
// other's: the host stages nothing private, the inlay shows nothing public,
// and new files land with whoever owns their directory.
func TestMonorepoInterleavedClaudeDir(t *testing.T) {
	f := newFixture(t)
	f.monorepo()
	snap, warnings := f.dogfood()
	f.steady(f.root, "setup", snap)
	contains(t, warnings, "CLAUDE.local.md is ignored by .gitignore", "claim warning")

	want := ".claude/settings.json\n.claude/skills/internal/release/SKILL.md\n.git-inlay/team.paths\nCLAUDE.local.md\npackages/api/.claude/settings.json"
	if got := f.inlayOK(f.root, "team", "ls-files"); got != want {
		t.Fatalf("team ls-files = %q, want %q", got, want)
	}
	if got, want := f.inlayOK(f.root, "research", "ls-files"), ".git-inlay/research.paths\ndocs/research/2026-10-31.md"; got != want {
		t.Fatalf("research ls-files = %q, want %q", got, want)
	}
	f.git(f.root, "add", "-A")
	if got := f.git(f.root, "diff", "--cached", "--name-only"); got != "" {
		t.Fatalf("host staged %q after add -A", got)
	}
	want = ".claude/settings.json\tteam\n.claude/skills/review/SKILL.md\thost\n.claude/skills/internal/release/SKILL.md\tteam\nCLAUDE.md\thost\nCLAUDE.local.md\tteam\ndocs/research/2026-10-31.md\tresearch\npackages/api/.claude/settings.local.json\tteam\n.claude/agents/x.md\tnone"
	if got := f.inlayOK(f.root, "which", ".claude/settings.json", ".claude/skills/review/SKILL.md", ".claude/skills/internal/release/SKILL.md", "CLAUDE.md", "CLAUDE.local.md", "docs/research/2026-10-31.md", "packages/api/.claude/settings.local.json", ".claude/agents/x.md"); got != want {
		t.Fatalf("which = %q, want %q", got, want)
	}

	// Editing a public file shows in the host only.
	f.write(".claude/skills/review/SKILL.md", publicSkill+"More.\n")
	if got := f.git(f.root, "status", "--porcelain"); got != " M .claude/skills/review/SKILL.md" {
		t.Fatalf("host status after editing a public skill = %q", got)
	}
	if got := f.inlayOK(f.root, "team", "status", "--porcelain"); got != "" {
		t.Fatalf("team status after editing a public skill = %q", got)
	}
	f.git(f.root, "checkout", "-q", "--", ".claude/skills/review/SKILL.md")

	// A new file under .claude/ but outside every claim is the host's.
	f.write(".claude/agents/x.md", "x\n")
	if got := f.git(f.root, "status", "--porcelain"); got != "?? .claude/agents/" {
		t.Fatalf("host status after a new unclaimed file = %q", got)
	}
	if got := f.inlayOK(f.root, "team", "status", "--porcelain"); got != "" {
		t.Fatalf("team status after a new unclaimed file = %q", got)
	}
	if err := os.RemoveAll(filepath.Join(f.root, ".claude", "agents")); err != nil {
		t.Fatal(err)
	}

	// New files under claimed directories belong to the inlay.
	f.write(".claude/skills/internal/notes.md", "n\n")
	f.write("packages/api/.claude/hooks.json", "{}\n")
	if got, want := f.inlayOK(f.root, "team", "status", "--porcelain"), "?? .claude/skills/internal/notes.md\n?? packages/api/.claude/hooks.json"; got != want {
		t.Fatalf("team status after new claimed files = %q, want %q", got, want)
	}
	if got := f.git(f.root, "status", "--porcelain"); got != "" {
		t.Fatalf("host status after new claimed files = %q", got)
	}
	f.inlayOK(f.root, "team", "add", "-A")
	f.inlayOK(f.root, "team", "commit", "-q", "-m", "more")
	snap[".claude/skills/internal/notes.md"] = "n\n"
	snap["packages/api/.claude/hooks.json"] = "{}\n"
	f.steady(f.root, "committing new inlay files", snap)

	// Nothing can be claimed inside the submodule.
	contains(t, f.inlayFails(f.root, 1, "claim", "research", "vendor/dep/notes.md"), "is inside the submodule at vendor/dep", "claim inside a submodule")

	excl := f.read(".git/info/exclude")
	for _, line := range []string{"/.git-inlay", "/.claude/settings.json", "/.claude/skills/internal", "/CLAUDE.local.md", "/packages/api/.claude", "/docs/research"} {
		contains(t, excl, line+"\n", "host exclude")
	}
	chain := f.read(".git/inlays/main/team/info/exclude")
	for _, line := range []string{"/*", "!/.claude/", "/.claude/*", "!/.claude/settings.json", "!/.claude/skills/", "/.claude/skills/*", "!/.claude/skills/internal/", "!/packages/api/.claude/"} {
		contains(t, chain, line+"\n", "team exclude chain")
	}
	status := f.inlayOK(f.root, "hooks", "status")
	for _, line := range []string{"pre-commit  installed", "pre-push    installed", "inlay:team  installed", "inlay:research installed"} {
		contains(t, status, line, "hooks status")
	}
	if f.read(".git/hooks/commit-msg") != "#!/bin/sh\nexit 0\n" {
		t.Fatal("the foreign commit-msg hook was touched")
	}
	contains(t, f.inlayOK(f.root, "check"), "ok: 2 inlay(s), 5 claim(s)", "check")
}

// A day of ordinary host work: branches, an upstream pull, rebase, merge,
// cherry-pick, amend, push, old tags and branches from before the agent
// config existed, and a hard reset. None of it touches the inlays.
func TestMonorepoDayOfHostOperations(t *testing.T) {
	f := newFixture(t)
	f.monorepo()
	snap, _ := f.dogfood()
	f.steady(f.root, "setup", snap)

	f.git(f.root, "checkout", "-q", "-b", "feat/api")
	f.write("packages/api/main.go", "package main\n\nfunc main() { println(\"feat\") }\n")
	f.git(f.root, "commit", "-q", "-a", "-m", "feature")
	f.steady(f.root, "commit on a branch", snap)
	f.git(f.root, "checkout", "-q", "main")
	f.steady(f.root, "switch back to main", snap)

	// A teammate without inlays pushes public changes, including to the
	// public half of .claude/.
	peer := filepath.Join(f.base, "peer")
	f.git(f.base, "clone", "-q", f.remote, peer)
	writeFile(t, filepath.Join(peer, "packages", "web", "index.html"), "<!doctype html>\n<title>v2</title>\n")
	writeFile(t, filepath.Join(peer, ".claude", "skills", "review", "SKILL.md"), publicSkill+"Revised upstream.\n")
	f.git(peer, "commit", "-q", "-a", "-m", "upstream work")
	f.git(peer, "push", "-q", "origin", "main")
	f.git(f.root, "pull", "-q", "--rebase", "origin", "main")
	f.steady(f.root, "pull --rebase", snap)
	if f.read(".claude/skills/review/SKILL.md") != publicSkill+"Revised upstream.\n" {
		t.Fatal("the public skill did not update")
	}

	f.git(f.root, "rebase", "-q", "main", "feat/api")
	f.steady(f.root, "rebase onto main", snap)
	f.git(f.root, "checkout", "-q", "main")
	f.git(f.root, "merge", "-q", "--no-ff", "-m", "merge feature", "feat/api")
	f.steady(f.root, "merge --no-ff", snap)
	f.git(f.root, "cherry-pick", "release/1.x")
	f.steady(f.root, "cherry-pick", snap)
	f.write("CHANGELOG.md", "# Changelog\n")
	f.git(f.root, "add", "CHANGELOG.md")
	f.git(f.root, "commit", "-q", "--amend", "--no-edit")
	f.steady(f.root, "commit --amend", snap)
	f.git(f.root, "push", "-q", "origin", "main")
	f.steady(f.root, "push", snap)

	// A tag from before .claude/ or CLAUDE.md existed in the host: the
	// public half disappears, the private half stays.
	f.git(f.root, "checkout", "-q", "v1.0.0")
	f.steady(f.root, "checkout of an old tag", snap)
	if f.exists(".claude/skills/review") || f.exists("CLAUDE.md") {
		t.Fatal("checkout of v1.0.0 kept files that commit does not have")
	}
	// A branch whose .gitignore lacks the local-file rules: the managed
	// block still covers every claim.
	f.git(f.root, "checkout", "-q", "release/1.x")
	f.steady(f.root, "checkout of the release branch", snap)
	f.git(f.root, "checkout", "-q", "main")
	f.steady(f.root, "checkout of main", snap)
	f.git(f.root, "reset", "-q", "--hard", "v1.0.0")
	f.steady(f.root, "reset --hard to an old tag", snap)
	f.git(f.root, "reset", "-q", "--hard", "origin/main")
	f.steady(f.root, "reset --hard back to origin/main", snap)
}

// Stash and clean in their ordinary forms leave the inlays alone. The forms
// that take ignored files along, stash --all and clean -x, remove the
// inlay's files from the tree; stash pop and the inlay's own checkout
// bring them back.
func TestMonorepoStashAndClean(t *testing.T) {
	f := newFixture(t)
	f.monorepo()
	snap, _ := f.dogfood()
	// What each inlay must report once its files have been swept away.
	missing := map[string]string{"team": " D .claude/settings.json", "research": " D docs/research/2026-10-31.md"}

	f.write("packages/api/main.go", "dirty\n")
	f.write("scratch.txt", "untracked\n")
	f.git(f.root, "stash", "-q", "-u")
	if f.exists("scratch.txt") {
		t.Fatal("stash -u left the untracked file")
	}
	f.steady(f.root, "stash -u", snap)
	f.git(f.root, "stash", "pop", "-q")
	if f.read("packages/api/main.go") != "dirty\n" || !f.exists("scratch.txt") {
		t.Fatal("stash pop did not restore the host's changes")
	}
	f.git(f.root, "checkout", "-q", "--", "packages/api/main.go")
	f.git(f.root, "clean", "-q", "-fd")
	f.steady(f.root, "clean -fd", snap)

	f.write("packages/api/main.go", "dirty\n")
	f.git(f.root, "stash", "-q", "--all")
	if f.exists(".claude/settings.json") || f.exists(".git-inlay/team.paths") {
		t.Fatal("stash --all left ignored files in place; the scenario no longer tests what it should")
	}
	// The manifests are gone too; the inlays fall back to HEAD's and still
	// report what is missing.
	for in, want := range missing {
		contains(t, f.inlayOK(f.root, in, "status", "--porcelain"), want, "inlay "+in+" status after stash --all")
	}
	f.git(f.root, "stash", "pop", "-q")
	f.git(f.root, "checkout", "-q", "--", "packages/api/main.go")
	f.steady(f.root, "stash --all and pop", snap)

	f.git(f.root, "clean", "-q", "-fdx")
	if f.exists(".claude/settings.json") || f.exists("docs/research") {
		t.Fatal("clean -fdx left ignored files in place; the scenario no longer tests what it should")
	}
	for in, want := range missing {
		contains(t, f.inlayOK(f.root, in, "status", "--porcelain"), want, "inlay "+in+" status after clean -x")
	}
	for _, in := range inlays {
		f.inlayOK(f.root, in, "checkout", "-q", "--", ".")
	}
	f.steady(f.root, "clean -fdx and inlay checkout", snap)
}

// An upstream commit that adds a claimed path is the one host operation
// that does reach the inlay: git overwrites ignored files on checkout.
// check reports it, the fix the guards prescribe is pushable, and the
// path's presence in history is flagged on later claims.
func TestMonorepoUpstreamCommitsClaimedPath(t *testing.T) {
	f := newFixture(t)
	f.monorepo()
	snap, _ := f.dogfood()

	peer := filepath.Join(f.base, "peer")
	f.git(f.base, "clone", "-q", f.remote, peer)
	writeFile(t, filepath.Join(peer, ".claude", "settings.json"), "{\"leaked\":true}\n")
	f.git(peer, "add", ".claude/settings.json")
	f.git(peer, "commit", "-q", "-m", "commit settings by mistake")
	f.git(peer, "push", "-q", "origin", "main")

	f.git(f.root, "pull", "-q", "--no-rebase", "origin", "main")
	if f.read(".claude/settings.json") != "{\"leaked\":true}\n" {
		t.Fatal("git kept the ignored file; the scenario no longer tests what it should")
	}
	if got := f.git(f.root, "status", "--porcelain"); got != "" {
		t.Fatalf("host status after the pull = %q", got)
	}
	contains(t, f.inlayFails(f.root, 1, "check"), ".claude/settings.json: tracked by the host but claimed by inlay team", "check after the pull")
	if got := f.inlayOK(f.root, "team", "status", "--porcelain"); got != " M .claude/settings.json" {
		t.Fatalf("team status after the pull = %q", got)
	}

	f.git(f.root, "rm", "-q", "--cached", ".claude/settings.json")
	f.git(f.root, "commit", "-q", "-m", "untrack settings")
	f.git(f.root, "push", "-q", "origin", "main")
	f.inlayOK(f.root, "team", "checkout", "-q", "--", ".claude/settings.json")
	f.steady(f.root, "recovery", snap)

	f.inlayOK(f.root, "unclaim", "team", ".claude/settings.json")
	_, errOut, code := f.inlay(f.root, "claim", "team", ".claude/settings.json")
	if code != 0 {
		t.Fatalf("reclaim: exit %d\n%s", code, errOut)
	}
	contains(t, errOut, ".claude/settings.json exists in the host's history", "reclaim warning")
	f.steady(f.root, "reclaim", snap)
}

// A teammate's fresh clone gets the private files back as regular files in
// place, with nothing but private paths on the private remotes, and edits
// flow between machines through the inlays alone.
func TestMonorepoFreshCloneByTeammate(t *testing.T) {
	f := newFixture(t)
	f.monorepo()
	snap, _ := f.dogfood()
	teamGit := filepath.Join(f.base, "team.git")
	researchGit := filepath.Join(f.base, "research.git")

	host2 := filepath.Join(f.base, "host2")
	f.git(f.base, "clone", "-q", f.remote, host2)
	out, errOut, code := f.inlay(host2, "clone", teamGit)
	if code != 0 {
		t.Fatalf("clone team: exit %d\n%s", code, errOut)
	}
	contains(t, out, "attached inlay team", "clone output")
	contains(t, errOut, "CLAUDE.local.md is ignored by .gitignore", "clone warning")
	f.inlayOK(host2, "clone", researchGit)
	f.inlayOK(host2, "hooks", "install")
	f.steady(host2, "fresh clone", snap)
	for rel := range snap {
		fi, err := os.Lstat(filepath.Join(host2, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("%s on host2: %v", rel, err)
		}
		if !fi.Mode().IsRegular() {
			t.Fatalf("%s on host2 has mode %v; Claude Code needs a regular file", rel, fi.Mode())
		}
	}
	if a, b := f.read(".git/info/exclude"), readAt(t, host2, ".git/info/exclude"); a != b {
		t.Fatalf("exclude blocks differ:\n%s\n---\n%s", a, b)
	}

	writeFile(t, filepath.Join(host2, ".claude", "settings.json"), "{\"permissions\":{\"allow\":[\"Bash(go test:*)\",\"Bash(go vet:*)\"]}}\n")
	f.inlayOK(host2, "team", "commit", "-q", "-a", "-m", "allow go vet")
	f.inlayOK(host2, "team", "push", "-q")
	f.inlayOK(f.root, "team", "pull", "-q")
	snap[".claude/settings.json"] = "{\"permissions\":{\"allow\":[\"Bash(go test:*)\",\"Bash(go vet:*)\"]}}\n"
	f.steady(f.root, "pull in the inlay", snap)
	f.steady(host2, "push from the teammate", snap)

	if got, want := f.git(teamGit, "ls-tree", "-r", "--name-only", "main"), ".claude/settings.json\n.claude/skills/internal/release/SKILL.md\n.git-inlay/team.paths\nCLAUDE.local.md\npackages/api/.claude/settings.json"; got != want {
		t.Fatalf("team remote holds %q, want %q", got, want)
	}
	if got := f.git(f.remote, "ls-tree", "-r", "--name-only", "main"); strings.Contains(got, "settings.json") || strings.Contains(got, "research") {
		t.Fatalf("host remote holds private paths:\n%s", got)
	}
}

// A linked worktree binds the claims before anything is attached there,
// through the common dir's exclude file and hooks; attach --all then brings
// every inlay over, and prune cleans up once the worktree is gone.
func TestMonorepoLinkedWorktree(t *testing.T) {
	f := newFixture(t)
	f.monorepo()
	snap, _ := f.dogfood()
	wt := filepath.Join(f.base, "wt")
	f.git(f.root, "worktree", "add", "-q", wt, "-b", "wt-branch")
	contains(t, f.inlayOK(wt, "ls"), "no inlays are attached", "ls in a linked worktree")
	if _, _, code := f.run(wt, "git", "check-ignore", "-q", ".claude/settings.json"); code != 0 {
		t.Fatal("the linked worktree does not ignore a claimed path")
	}
	writeFile(t, filepath.Join(wt, ".claude", "settings.json"), "{}\n")
	f.git(wt, "add", "-f", ".claude/settings.json")
	contains(t, f.gitFails(wt, "commit", "-q", "-m", "leak from a worktree"), ".claude/settings.json: belongs to inlay team", "pre-commit in a linked worktree")
	f.git(wt, "reset", "-q")

	// The stray file would be overwritten, so team is refused; research,
	// attached first, stays attached.
	contains(t, f.inlayFails(wt, 1, "attach", "--all"), "would be overwritten: .claude/settings.json", "attach over a stray file")
	contains(t, f.inlayOK(wt, "ls"), "research\tdocs/research/", "research attached before the refusal")
	if err := os.Remove(filepath.Join(wt, ".claude", "settings.json")); err != nil {
		t.Fatal(err)
	}
	contains(t, f.inlayOK(wt, "attach", "--all"), "attached inlay team from worktree main (branch main, 4 claims)", "attach --all")
	f.steady(wt, "attach --all", snap)
	f.steady(f.root, "attach --all in the worktree", snap)

	f.git(f.root, "worktree", "remove", "--force", wt)
	contains(t, f.inlayFails(f.root, 1, "check"), "no longer exists", "check after worktree remove")
	out := f.inlayOK(f.root, "prune")
	contains(t, out, "wt/wt\tresearch\tremoved", "prune")
	contains(t, out, "wt/wt\tteam\tremoved", "prune")
	f.steady(f.root, "prune", snap)
}

// Claude Code reads hooks from a settings file the host ignores, which is
// the contract git-inlay exists to keep. claude --init-only runs the
// project's SessionStart hooks without a conversation or credentials, so a
// hook that writes a marker proves the inlay-delivered settings were
// loaded; --setting-sources user is the negative control.
func TestClaudeCodeLoadsInlaySettings(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the claude binary; skipped in short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the marker hook is a POSIX shell command")
	}
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude is not on PATH")
	}
	f := newFixture(t)
	f.monorepo()
	marker := filepath.Join(f.base, "session-start.marker")
	settings, err := json.Marshal(map[string]any{
		"permissions": map[string]any{"allow": []string{"Bash(go test:*)"}},
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{
				"hooks": []any{map[string]any{"type": "command", "command": "echo loaded > '" + marker + "'"}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.inlayOK(f.root, "init", "team")
	f.write(".claude/settings.json", string(settings)+"\n")
	f.inlayOK(f.root, "claim", "team", ".claude/settings.json")
	f.inlayOK(f.root, "team", "commit", "-q", "-m", "hooks")
	if _, _, code := f.run(f.root, "git", "check-ignore", "-q", ".claude/settings.json"); code != 0 {
		t.Fatal("the host does not ignore the settings file")
	}

	cfg := filepath.Join(f.base, "claude-config")
	claude := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, claudeBin, append([]string{"--init-only"}, args...)...)
		cmd.Dir = f.root
		cmd.Env = append(slices.Clone(f.env), "CLAUDE_CONFIG_DIR="+cfg,
			"DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
		if sh := os.Getenv("SHELL"); sh != "" {
			cmd.Env = append(cmd.Env, "SHELL="+sh)
		}
		var errOut bytes.Buffer
		cmd.Stderr = &errOut
		cmd.WaitDelay = 10 * time.Second // a lingering child must not hold stderr past the timeout
		if err := cmd.Run(); err != nil {
			t.Fatalf("claude --init-only %s: %v\n%s", strings.Join(args, " "), err, errOut.String())
		}
	}
	claude()
	if b, err := os.ReadFile(marker); err != nil || strings.TrimSpace(string(b)) != "loaded" {
		t.Fatalf("the SessionStart hook from the inlay's settings did not run: %q, %v", b, err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	claude("--setting-sources", "user")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the hook ran with project settings disabled, so the marker proves nothing")
	}
	if got := f.git(f.root, "status", "--porcelain"); got != "" {
		t.Fatalf("host status after running claude = %q", got)
	}
}

func readAt(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
