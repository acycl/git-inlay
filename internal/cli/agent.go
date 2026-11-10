package cli

import (
	"fmt"
	"strings"

	"github.com/acycl/git-inlay/internal/host"
)

// briefClaims caps the claims the session-start briefing lists per inlay;
// git inlay ls lists the rest.
const briefClaims = 20

// hookSessionStart prints a briefing on this worktree's inlays for a coding
// agent's session-start hook, which adds the hook's output to the agent's
// context. Claimed paths are invisible to the host's git status, diff and
// add, and to search that honours git's ignore rules, so an agent that is
// not told about them cannot see where its edits went or how to record
// them. Outside a git working tree, or on a host without inlays, it prints
// nothing and succeeds, so one hook can serve every project. Like the
// guards it reads manifests and writes nothing.
func (c *cli) hookSessionStart() error {
	h, err := host.Discover(c.o.Dir, c.o.Env)
	if err != nil {
		// A plain directory, a bare repository, or no usable git: there is
		// no worktree to brief on.
		return nil
	}
	s := &session{c: c, host: h}
	if err := s.reload(); err != nil {
		return err
	}
	mine, elsewhere := s.mine(), s.siblingNames()
	if len(mine) == 0 {
		if len(elsewhere) > 0 {
			c.printf("git-inlay: other worktrees of this repository hold inlays that this one does not (%s), and the paths they claim are ignored here too. git inlay attach --all copies them here.\n",
				strings.Join(elsewhere, ", "))
		}
		return nil
	}

	var b strings.Builder
	b.WriteString("git-inlay: this worktree holds inlays, private git repositories whose files sit in this tree but are never committed to this repository. Claims by inlay:\n\n")
	for _, e := range mine {
		claims := e.Manifest.Claims
		shown := make([]string, 0, min(len(claims), briefClaims))
		for _, cl := range claims[:min(len(claims), briefClaims)] {
			shown = append(shown, cl.String())
		}
		list := strings.Join(shown, ", ") + more(len(claims)-len(shown))
		if len(claims) == 0 {
			list = "(no claims yet)"
		}
		fmt.Fprintf(&b, "  %s: %s\n", e.Name, list)
	}
	b.WriteString(`
- This repository ignores those paths through .git/info/exclude, so changes to them never show in its git status, git diff or git add -A, and tools that honour git's ignore rules may skip them. Open them by path. git inlay status summarizes each inlay's changes; git inlay <name> status and git inlay <name> diff show them.
- Record changes through the owning inlay: git inlay <name> add <path>, git inlay <name> commit, git inlay <name> push. A new file under a claimed directory belongs to that inlay; git inlay which <path> names a path's owner.
- Never stage a claimed path in this repository with git add -f, or commit one with --no-verify; git-inlay's hooks refuse the commit or the push.
- git clean -x or -X in this repository deletes these files, and git inlay restore brings back only what an inlay has committed; use git clean -fd instead. git stash --all here stashes them with everything else, and only git stash pop brings back their uncommitted changes, so do not run git inlay restore before it.
`)
	if len(elsewhere) > 0 {
		fmt.Fprintf(&b, "- Other worktrees hold inlays that this one does not (%s); git inlay attach --all copies them here.\n",
			strings.Join(elsewhere, ", "))
	}
	if vs, _, err := s.hookViolations(nil); err == nil && len(vs) > 0 {
		inactive := make([]string, len(vs))
		for i, v := range vs {
			inactive[i] = v.Path
		}
		fmt.Fprintf(&b, "- Not every git-inlay guard is active in this repository (%s), so a claimed path could be committed or pushed by mistake; suggest that the user run git inlay hooks install.\n",
			strings.Join(inactive, ", "))
	}
	c.printf("%s", b.String())
	return nil
}
