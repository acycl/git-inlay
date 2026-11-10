package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/acycl/git-inlay/internal/gitx"
	"github.com/acycl/git-inlay/internal/hooks"
	"github.com/acycl/git-inlay/internal/inlay"
	"github.com/acycl/git-inlay/internal/manifest"
)

// attach copies inlays attached in another worktree of the host into this
// one (docs/design.md section 7.2): the sibling's current branch is checked out
// and its remote reused, so the new inlay pushes to the real remote.
func (c *cli) attach(args []string) error {
	fs := c.flagSet("attach")
	all := fs.Bool("all", false, "attach every inlay found in another worktree")
	noHooks := fs.Bool("no-hooks", false, "do not install the host's guards")
	names, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if (len(names) == 0 && !*all) || (len(names) > 0 && *all) {
		return misuse("attach")
	}
	s, err := c.open()
	if err != nil {
		return err
	}
	s.noHooks = *noHooks
	if err := s.refuseSparse(); err != nil {
		return err
	}
	if *all {
		names = s.siblingNames()
		if len(names) == 0 && len(s.mine()) > 0 {
			c.printf("every inlay of this host is already attached to this worktree; if their files are missing, run: git inlay restore\n")
			return nil
		}
		if len(names) == 0 {
			c.printf("no inlays are attached to other worktrees of this host\n")
			return nil
		}
	}
	for i, name := range names {
		if err := s.attachOne(name); err != nil {
			// The inlays attached before this one stay attached.
			if i > 0 {
				s.warnUnexcluded()
			}
			return err
		}
	}
	s.warnUnexcluded()
	return nil
}

// attachOne attaches inlay name from its sibling in another worktree.
func (s *session) attachOne(name string) error {
	if err := manifest.ValidName(name); err != nil {
		return usageError{err.Error()}
	}
	if err := reservedName(name, false); err != nil {
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
	// are copied as they are, minus the symbolic origin/HEAD. The sibling's
	// core.worktree may name a worktree whose parent directory is gone,
	// which makes git refuse to start in it at all, so upload-pack and the
	// pack-objects it starts run with the work tree pointed at the common
	// dir, as inlay.Elsewhere does. Only the environment outranks it: git
	// reads core.worktree from the repository's own config file, so a -c
	// would not.
	uploadPack := "GIT_WORK_TREE=" + hooks.ShellQuote(filepath.ToSlash(s.host.CommonDir)) + " git upload-pack"
	if err := t.Run("fetch", "-q", "--upload-pack="+uploadPack, e.GitDir,
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
	// The inlay is attached and its files are in the tree, so the sync
	// comes first: nothing below may leave them showing in the host. What
	// follows is tidying, and a failure of it is not a failure to attach.
	if err := s.refresh(); err != nil {
		return err
	}
	g := in.Git()
	if ok, _ := g.Succeeds("rev-parse", "--verify", "-q", "refs/remotes/origin/"+branch); ok {
		if err := g.Run("branch", "-q", "--set-upstream-to=origin/"+branch, branch); err != nil {
			s.c.warnf("could not set the upstream of branch %s in inlay %s: %v", branch, name, err)
		}
	}
	refs, err := g.Lines("for-each-ref", "--format=%(refname)", "refs/sibling/")
	if err != nil {
		s.c.warnf("could not remove the refs that attach fetched into inlay %s under refs/sibling: %v", name, err)
	}
	for _, r := range refs {
		if err := g.Run("update-ref", "-d", r); err != nil {
			s.c.warnf("could not remove %s from inlay %s: %v", r, name, err)
		}
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

// prune removes inlays whose host worktree is gone (docs/design.md section
// 7.4). An inlay with commits on no remote is kept unless forced, since
// removing it would lose work.
func (c *cli) prune(args []string) error {
	fs := c.flagSet("prune")
	dryRun := fs.Bool("dry-run", false, "report only; remove nothing")
	force := fs.Bool("force", false, "remove inlays with unpushed commits too")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return misuse("prune")
	}
	s, err := c.open()
	if err != nil {
		return err
	}
	var stale []inlay.Entry
	for _, e := range s.entries {
		if e.Gone {
			stale = append(stale, e)
		}
	}
	clones, err := s.abandonedClones()
	if err != nil {
		return err
	}
	if len(stale) == 0 && len(clones) == 0 {
		c.printf("nothing to prune\n")
		return nil
	}
	// A clone that cannot be removed is reported, and prune carries on
	// with the rest and the stale inlays before it fails.
	failed := false
	for _, dir := range clones {
		key, err := filepath.Rel(s.host.InlaysDir(), filepath.Dir(dir))
		if err != nil {
			return err
		}
		action := "removed"
		if *dryRun {
			action = "would remove"
		} else if err := os.RemoveAll(dir); err != nil {
			action, failed = "not removed: "+err.Error(), true
		}
		c.printf("%s\t%s\t%s (abandoned clone)\n", filepath.ToSlash(key), filepath.Base(dir), action)
	}
	if len(stale) == 0 {
		if failed {
			return exitError{exitViolation}
		}
		return nil
	}
	var kept []inlay.Entry
	for _, e := range stale {
		run := inlay.Elsewhere(s.host, e.GitDir)
		n, err := unpushed(run)
		if err != nil {
			return err
		}
		st, err := stashes(run)
		if err != nil {
			return err
		}
		remove := (n == 0 && st == 0) || *force
		action := "removed"
		switch {
		case *dryRun && remove:
			action = "would remove"
		case *dryRun:
			action = "would keep"
		case !remove:
			action = "kept"
		}
		var state []string
		if n > 0 {
			state = append(state, fmt.Sprintf("%d unpushed commit(s)", n))
		}
		if st > 0 {
			state = append(state, fmt.Sprintf("%d stash(es)", st))
		}
		if len(state) > 0 {
			action += " (" + strings.Join(state, ", ") + ")"
		}
		c.printf("%s\t%s\t%s\n", e.Key, e.Name, action)
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
	if err := s.refresh(); err != nil {
		return err
	}
	if len(kept) == 0 {
		if failed {
			return exitError{exitViolation}
		}
		return nil
	}
	fmt.Fprintf(c.o.Stderr, "git-inlay: kept %d inlay(s) with work that exists nowhere else\n", len(kept))
	for _, e := range kept {
		run := inlay.Elsewhere(s.host, e.GitDir)
		// --work-tree keeps git from resolving the inlay's missing worktree.
		raw := fmt.Sprintf("git --git-dir=%s --work-tree=%s", e.GitDir, s.host.CommonDir)
		_, hasRemote, _ := run.Config("remote.origin.url")
		if !hasRemote {
			fmt.Fprintf(c.o.Stderr, "  %s has no remote; add one with: %s remote add origin <url>\n", e.Name, raw)
			continue
		}
		branch, _ := run.Output("symbolic-ref", "-q", "--short", "HEAD")
		if branch == "" {
			branch = "<branch>"
		}
		if n, _ := unpushed(run); n > 0 {
			fmt.Fprintf(c.o.Stderr, "  %s: push with: %s push origin %s\n", e.Name, raw, branch)
		}
		if st, _ := stashes(run); st > 0 {
			fmt.Fprintf(c.o.Stderr, "  %s has %d stash(es), which push does not carry; save them with: %s push origin refs/stash:refs/heads/%s-stash\n", e.Name, st, raw, branch)
		}
	}
	fmt.Fprintf(c.o.Stderr, "or remove them anyway with: git inlay prune --force\n")
	return exitError{exitViolation}
}

// abandonedClones lists the staging directories an interrupted clone or
// attach left under $C/inlays: stage names them .clone-*, which no verb
// takes for an inlay, so nothing else would ever remove one. One with
// anything in it touched within the last day may belong to a clone still
// running, its fetch writing a pack or waiting at a credential prompt, so
// it is left for a later prune.
func (s *session) abandonedClones() ([]string, error) {
	root := s.host.InlaysDir()
	parents := []string{filepath.Join(root, "main")}
	wts, err := os.ReadDir(filepath.Join(root, "wt"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, w := range wts {
		if w.IsDir() {
			parents = append(parents, filepath.Join(root, "wt", w.Name()))
		}
	}
	var out []string
	for _, p := range parents {
		entries, err := os.ReadDir(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), ".clone-") {
				continue
			}
			dir := filepath.Join(p, e.Name())
			if time.Since(newest(dir)) >= 24*time.Hour {
				out = append(out, dir)
			}
		}
	}
	return out, nil
}

// newest returns the latest modification time of anything under dir, dir
// included. A walk error counts as now, so an unreadable directory is
// never taken for abandoned.
func newest(dir string) time.Time {
	var latest time.Time
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})
	if err != nil {
		return time.Now()
	}
	return latest
}

// stashes counts the inlay's stash entries, which no push carries.
func stashes(run gitx.Runner) (int, error) {
	entries, err := run.Lines("stash", "list")
	return len(entries), err
}

// unpushed counts commits on the inlay's branches and tags, and on a
// detached HEAD, that no remote has. With no remote configured, every
// commit counts.
func unpushed(run gitx.Runner) (int, error) {
	// The trailing -- matters: the runner sits in the common dir, where a
	// file named HEAD makes the revision ambiguous otherwise.
	args := []string{"rev-list", "--count", "--branches", "--tags", "--not", "--remotes", "--"}
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
