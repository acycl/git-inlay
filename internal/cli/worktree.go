package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/acycl/git-inlay/internal/gitx"
	"github.com/acycl/git-inlay/internal/inlay"
	"github.com/acycl/git-inlay/internal/manifest"
)

// attach copies inlays attached in another worktree of the host into this
// one (PLAN.md section 7.2): the sibling's current branch is checked out
// and its remote reused, so the new inlay pushes to the real remote.
func (c *cli) attach(args []string) error {
	fs := c.flagSet("attach")
	all := fs.Bool("all", false, "attach every inlay found in another worktree")
	names, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if (len(names) == 0 && !*all) || (len(names) > 0 && *all) {
		return usagef("usage: git inlay attach <name>... | --all")
	}
	s, err := c.open()
	if err != nil {
		return err
	}
	if err := s.refuseSparse(); err != nil {
		return err
	}
	if *all {
		names = s.siblingNames()
		if len(names) == 0 {
			c.printf("no inlays are attached to other worktrees of this host\n")
			return nil
		}
	}
	for _, name := range names {
		if err := s.attachOne(name); err != nil {
			return err
		}
	}
	return nil
}

// attachOne attaches inlay name from its sibling in another worktree.
func (s *session) attachOne(name string) error {
	if err := manifest.ValidName(name); err != nil {
		return usageError{err.Error()}
	}
	e, ok := s.sibling(name)
	if !ok {
		if _, err := s.entry(name); err == nil {
			return fmt.Errorf("inlay %q is already attached to this worktree", name)
		}
		return fmt.Errorf("no inlay named %q is attached to another worktree of this host", name)
	}
	sib := inlay.Elsewhere(s.host, e.GitDir)
	branch, err := sib.Output("symbolic-ref", "-q", "--short", "HEAD")
	if err != nil || branch == "" {
		return fmt.Errorf("inlay %s in worktree %s is not on a branch; check one out there first", name, e.Key)
	}
	// Only commits travel. The sibling's worktree may itself be gone, in
	// which case attach is how its history is recovered, so a failed status
	// is not an error.
	if wt, ok, _ := sib.Config("core.worktree"); ok {
		dirty := gitx.Runner{GitDir: e.GitDir, WorkTree: wt, Dir: wt, Env: s.host.Env}
		if out, err := dirty.Output("status", "--porcelain"); err == nil && out != "" {
			s.c.warnf("inlay %s in worktree %s has uncommitted changes, which attach does not copy", name, e.Key)
		}
	}
	url, hasRemote, err := sib.Config("remote.origin.url")
	if err != nil {
		return err
	}
	if hasRemote {
		if err := s.checkRemote(url); err != nil {
			return err
		}
	}
	tmp, t, cleanup, err := s.stage()
	if err != nil {
		return err
	}
	// Branches land in a scratch namespace so the checkout below can create
	// the local branch from an unborn HEAD; remote-tracking refs and tags
	// are copied as they are, minus the symbolic origin/HEAD.
	if err := t.Run("fetch", "-q", e.GitDir,
		"+refs/heads/*:refs/sibling/*", "+refs/remotes/*:refs/remotes/*",
		"^refs/remotes/origin/HEAD", "+refs/tags/*:refs/tags/*"); err != nil {
		cleanup()
		return err
	}
	if hasRemote {
		if err := t.Run("remote", "add", "origin", url); err != nil {
			cleanup()
			return err
		}
		if push, ok, _ := sib.Config("remote.origin.pushurl"); ok {
			if err := t.Run("config", "remote.origin.pushurl", push); err != nil {
				cleanup()
				return err
			}
		}
	}
	rev := "refs/sibling/" + branch
	m, err := s.vetTree(t, name, rev, fmt.Sprintf("inlay %s in worktree %s", name, e.Key))
	if err != nil {
		cleanup()
		return err
	}
	in, err := s.place(tmp, name, m, "checkout", "-q", "-b", branch, rev)
	if err != nil {
		cleanup()
		return err
	}
	g := in.Git()
	if ok, _ := g.Succeeds("rev-parse", "--verify", "-q", "refs/remotes/origin/"+branch); ok {
		if err := g.Run("branch", "-q", "--set-upstream-to=origin/"+branch, branch); err != nil {
			return err
		}
	}
	if refs, err := g.Output("for-each-ref", "--format=%(refname)", "refs/sibling/"); err == nil {
		for _, r := range strings.Split(refs, "\n") {
			if r != "" {
				if err := g.Run("update-ref", "-d", r); err != nil {
					return err
				}
			}
		}
	}
	if err := s.refresh(); err != nil {
		return err
	}
	s.c.printf("attached inlay %s from worktree %s (branch %s, %d claims)\n", name, e.Key, branch, len(m.Claims))
	if !hasRemote {
		s.c.warnf("inlay %s has no remote; add one with: git inlay %s remote add origin <url>", name, name)
	}
	s.attached(in, m)
	return nil
}

// sibling finds inlay name in another worktree. Entries are sorted by key,
// so the main worktree's copy wins when several exist.
func (s *session) sibling(name string) (inlay.Entry, bool) {
	for _, e := range s.entries {
		if e.Name == name && e.Key != s.host.Key {
			return e, true
		}
	}
	return inlay.Entry{}, false
}

// siblingNames lists the inlays attached elsewhere but not here.
func (s *session) siblingNames() []string {
	var names []string
	for _, e := range s.entries {
		if e.Key == s.host.Key || slices.Contains(names, e.Name) {
			continue
		}
		if _, err := s.entry(e.Name); err == nil {
			continue
		}
		names = append(names, e.Name)
	}
	return names
}

// prune removes inlays whose host worktree is gone (PLAN.md section 7.4).
// An inlay with commits on no remote is kept unless forced, since removing
// it would lose work.
func (c *cli) prune(args []string) error {
	fs := c.flagSet("prune")
	dryRun := fs.Bool("dry-run", false, "report only; remove nothing")
	force := fs.Bool("force", false, "remove inlays with unpushed commits too")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: git inlay prune [--dry-run] [--force]")
	}
	s, err := c.open()
	if err != nil {
		return err
	}
	var stale []inlay.Entry
	for _, e := range s.entries {
		gone, err := s.worktreeGone(e.Key)
		if err != nil {
			return err
		}
		if gone {
			stale = append(stale, e)
		}
	}
	if len(stale) == 0 {
		c.printf("nothing to prune\n")
		return nil
	}
	var kept []inlay.Entry
	for _, e := range stale {
		n, err := unpushed(inlay.Elsewhere(s.host, e.GitDir))
		if err != nil {
			return err
		}
		remove := n == 0 || *force
		action := "removed"
		switch {
		case *dryRun && remove:
			action = "would remove"
		case *dryRun:
			action = "would keep"
		case !remove:
			action = "kept"
		}
		state := ""
		if n > 0 {
			state = fmt.Sprintf(" (%d unpushed commit(s))", n)
		}
		c.printf("%s\t%s\t%s%s\n", e.Key, e.Name, action, state)
		if *dryRun {
			continue
		}
		if !remove {
			kept = append(kept, e)
			continue
		}
		if err := os.RemoveAll(e.GitDir); err != nil {
			return err
		}
		// Tidy the worktree's directory and the wt/ level once they are empty.
		os.Remove(filepath.Dir(e.GitDir))
		os.Remove(filepath.Dir(filepath.Dir(e.GitDir)))
	}
	if *dryRun {
		return nil
	}
	if err := s.rewriteHost(); err != nil {
		return err
	}
	if len(kept) == 0 {
		return nil
	}
	fmt.Fprintf(c.o.Stderr, "git-inlay: kept %d inlay(s) whose commits are on no remote\n", len(kept))
	for _, e := range kept {
		run := inlay.Elsewhere(s.host, e.GitDir)
		// --work-tree keeps git from resolving the inlay's missing worktree.
		raw := fmt.Sprintf("git --git-dir=%s --work-tree=%s", e.GitDir, s.host.CommonDir)
		if _, hasRemote, _ := run.Config("remote.origin.url"); !hasRemote {
			fmt.Fprintf(c.o.Stderr, "  %s has no remote; add one with: %s remote add origin <url>\n", e.Name, raw)
			continue
		}
		branch, _ := run.Output("symbolic-ref", "-q", "--short", "HEAD")
		if branch == "" {
			branch = "<branch>"
		}
		fmt.Fprintf(c.o.Stderr, "  %s: push with: %s push origin %s\n", e.Name, raw, branch)
	}
	fmt.Fprintf(c.o.Stderr, "or remove them anyway with: git inlay prune --force\n")
	return exitError{exitViolation}
}

// worktreeGone reports whether the linked worktree behind key has been
// removed, or had its directory deleted without git worktree remove. A
// locked worktree is never gone: git itself keeps those through an
// unmounted volume, and so does prune.
func (s *session) worktreeGone(key string) (bool, error) {
	id, ok := strings.CutPrefix(key, "wt/")
	if !ok {
		return false, nil
	}
	dir := filepath.Join(s.host.CommonDir, "worktrees", id)
	data, err := os.ReadFile(filepath.Join(dir, "gitdir"))
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(filepath.Join(dir, "locked")); err == nil {
		return false, nil
	}
	_, err = os.Stat(strings.TrimSpace(string(data)))
	return errors.Is(err, os.ErrNotExist), nil
}

// unpushed counts commits on the inlay's branches, and on a detached HEAD,
// that no remote has. With no remote configured, every commit counts.
func unpushed(run gitx.Runner) (int, error) {
	// The trailing -- matters: the runner sits in the common dir, where a
	// file named HEAD makes the revision ambiguous otherwise.
	args := []string{"rev-list", "--count", "--branches", "--not", "--remotes", "--"}
	if ok, err := run.Succeeds("rev-parse", "--verify", "-q", "HEAD"); err != nil {
		return 0, err
	} else if ok {
		args = slices.Insert(args, 2, "HEAD")
	}
	out, err := run.Output(args...)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}
