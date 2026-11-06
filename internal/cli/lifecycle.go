package cli

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/acycl/git-inlay/internal/host"
	"github.com/acycl/git-inlay/internal/inlay"
	"github.com/acycl/git-inlay/internal/manifest"
)

// restore re-checks out tracked inlay files that are missing from the
// tree: the documented answer to git clean -x and git stash --all in the
// host (PLAN.md section 6). Files that are present, modified or not, are
// left alone.
func (c *cli) restore(args []string) error {
	if len(args) > 1 {
		return usagef("usage: git inlay restore [<name>]")
	}
	s, err := c.open()
	if err != nil {
		return err
	}
	entries := s.mine()
	if len(args) == 1 {
		e, err := s.entry(args[0])
		if err != nil {
			return err
		}
		entries = []inlay.Entry{e}
	}
	total := 0
	for _, e := range entries {
		files, err := e.Inlay.Tracked()
		if err != nil {
			return err
		}
		var missing []string
		for _, p := range files {
			if _, err := os.Lstat(filepath.Join(s.host.Root, filepath.FromSlash(p))); errors.Is(err, os.ErrNotExist) {
				missing = append(missing, p)
			}
		}
		if len(missing) == 0 {
			continue
		}
		var input strings.Builder
		for _, p := range missing {
			input.WriteString(":(literal)" + p)
			input.WriteByte(0)
		}
		if _, err := e.Inlay.Git().OutputZWithInput([]byte(input.String()),
			"checkout", "-q", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return err
		}
		c.printf("restored %d file(s) in inlay %s\n", len(missing), e.Name)
		total += len(missing)
	}
	if total == 0 {
		c.printf("nothing to restore\n")
	}
	return nil
}

// detach removes an inlay from this worktree. Uncommitted or unpushed work
// stops it unless forced; the inlay's tracked files are deleted from the
// tree unless kept, in which case the host sees them as untracked.
func (c *cli) detach(args []string) error {
	fs := c.flagSet("detach")
	force := fs.Bool("force", false, "detach even with uncommitted or unpushed work")
	keep := fs.Bool("keep-files", false, "leave the inlay's files in the tree")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: git inlay detach <name> [--force] [--keep-files]")
	}
	name := pos[0]
	s, err := c.open()
	if err != nil {
		return err
	}
	e, err := s.entry(name)
	if err != nil {
		return err
	}
	in := e.Inlay
	if !*force {
		dirty, err := in.Git().Output("status", "--porcelain")
		if err != nil {
			return err
		}
		if dirty != "" {
			return fmt.Errorf("inlay %s has uncommitted changes; see them with: git inlay %s status, then commit them or use --force", name, name)
		}
		if st, err := stashes(in.Git()); err != nil {
			return err
		} else if st > 0 {
			return fmt.Errorf("inlay %s has %d stash(es); apply or drop them with: git inlay %s stash pop, or use --force to lose them", name, st, name)
		}
		n, err := unpushed(in.Git())
		if err != nil {
			return err
		}
		remotes, err := in.RemoteNames()
		if err != nil {
			return err
		}
		switch {
		case n > 0 && len(remotes) == 0:
			return fmt.Errorf("inlay %s has no remote, so its %d commit(s) exist nowhere else; add one with: git inlay %s remote add origin <url>, then push, or use --force to lose them", name, n, name)
		case n > 0:
			return fmt.Errorf("inlay %s has %d commit(s) on no remote; push them with: git inlay %s push, or use --force to lose them", name, n, name)
		}
	}
	files, err := in.Tracked()
	if err != nil {
		return err
	}
	removed := 0
	var shared []string
	if !*keep {
		// A file the host also tracks (I3 broken by an upstream commit, say)
		// is the host's to delete, not ours.
		specs := make([]string, len(files))
		for i, p := range files {
			specs[i] = ":(literal)" + p
		}
		shared, err = s.host.Tracked(specs...)
		if err != nil {
			return err
		}
		// The paths come from git and lie inside the tree; os.Root keeps a
		// symlink that has appeared since from redirecting a delete outside it.
		root, err := os.OpenRoot(s.host.Root)
		if err != nil {
			return err
		}
		defer root.Close()
		// On a case-insensitive host the host's spelling of a shared
		// file may differ from the inlay's; it is the same file.
		icase, err := s.host.IgnoreCase()
		if err != nil {
			return err
		}
		key := func(p string) string {
			if icase {
				return host.Fold(p)
			}
			return p
		}
		hostOwned := map[string]bool{}
		for _, p := range shared {
			hostOwned[key(p)] = true
		}
		for _, p := range files {
			if hostOwned[key(p)] {
				continue
			}
			err := root.Remove(filepath.FromSlash(p))
			if err == nil {
				removed++
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		removeEmptyParents(root, files)
	}
	if err := os.RemoveAll(in.GitDir); err != nil {
		return err
	}
	if err := s.refresh(); err != nil {
		return err
	}
	if *keep {
		c.printf("detached inlay %s; %d file(s) kept\n", name, len(files))
		c.warnf("the files stay on disk and are now visible to the host as untracked")
		return nil
	}
	c.printf("detached inlay %s; %d file(s) removed\n", name, removed)
	if len(shared) > 0 {
		c.warnf("left %d file(s) the host also tracks: %s", len(shared), summarize(shared))
	}
	return nil
}

// removeEmptyParents removes the directories that deleting files left
// empty, deepest first, never touching root itself. A directory that still
// holds anything, such as the host's own files, stays.
func removeEmptyParents(root *os.Root, files []string) {
	dirs := map[string]bool{}
	for _, f := range files {
		for _, a := range (manifest.Claim{Path: f}).Ancestors() {
			dirs[a] = true
		}
	}
	sorted := slices.SortedFunc(maps.Keys(dirs), func(a, b string) int {
		return cmp.Compare(strings.Count(b, "/"), strings.Count(a, "/"))
	})
	for _, d := range sorted {
		root.Remove(filepath.FromSlash(d))
	}
}

// inlayState is one inlay's short status.
type inlayState struct {
	branch    string // branch name, or "detached"
	upstream  string // tracking ref, or empty
	hasRemote bool
	ahead     int
	behind    int
	changed   int
	untracked int
}

// sync renders the relation to the upstream.
func (st inlayState) sync() string {
	switch {
	case !st.hasRemote:
		return "no remote"
	case st.upstream == "":
		return "no upstream"
	case st.ahead == 0 && st.behind == 0:
		return "up to date"
	}
	var parts []string
	if st.ahead > 0 {
		parts = append(parts, fmt.Sprintf("ahead %d", st.ahead))
	}
	if st.behind > 0 {
		parts = append(parts, fmt.Sprintf("behind %d", st.behind))
	}
	return strings.Join(parts, ", ")
}

// tree renders the working tree state.
func (st inlayState) tree() string {
	var parts []string
	if st.changed > 0 {
		parts = append(parts, fmt.Sprintf("%d changed", st.changed))
	}
	if st.untracked > 0 {
		parts = append(parts, fmt.Sprintf("%d untracked", st.untracked))
	}
	if len(parts) == 0 {
		return "clean"
	}
	return strings.Join(parts, ", ")
}

// status prints a short status of every inlay in this worktree.
func (c *cli) status(args []string) error {
	fs := c.flagSet("status")
	fetch := fs.Bool("fetch", false, "fetch each inlay's remote first")
	porcelain := fs.Bool("porcelain", false, "machine-readable output: name, branch, upstream, ahead, behind, changed, untracked")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: git inlay status [--fetch] [--porcelain]")
	}
	s, err := c.open()
	if err != nil {
		return err
	}
	entries := s.mine()
	if len(entries) == 0 && !*porcelain {
		c.printf("no inlays are attached to this worktree\n")
		return nil
	}
	for _, e := range entries {
		st, err := c.inlayStatus(e.Inlay, *fetch)
		if err != nil {
			return err
		}
		if *porcelain {
			c.printf("%s\t%s\t%s\t%d\t%d\t%d\t%d\n", e.Name, st.branch, st.upstream, st.ahead, st.behind, st.changed, st.untracked)
			continue
		}
		c.printf("%s\t%s\t%s\t%s\n", e.Name, st.branch, st.sync(), st.tree())
	}
	return nil
}

// inlayStatus gathers one inlay's state, fetching first when asked.
func (c *cli) inlayStatus(in *inlay.Inlay, fetch bool) (inlayState, error) {
	g := in.Git()
	var st inlayState
	st.branch = "detached"
	if b, err := g.Output("symbolic-ref", "-q", "--short", "HEAD"); err == nil && b != "" {
		st.branch = b
	}
	remotes, err := in.RemoteNames()
	if err != nil {
		return st, err
	}
	st.hasRemote = len(remotes) > 0
	if fetch && st.hasRemote {
		// With the terminal attached, so credential prompts work.
		cmd := g.Command("fetch", "-q")
		cmd.Stdin, cmd.Stderr = c.o.Stdin, c.o.Stderr
		if err := cmd.Run(); err != nil {
			c.warnf("fetch for inlay %s failed; showing the last known state", in.Name)
		}
	}
	if up, err := g.Output("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil && up != "" {
		st.upstream = up
		counts, err := g.Output("rev-list", "--left-right", "--count", "@{upstream}...HEAD", "--")
		if err != nil {
			return st, err
		}
		behind, ahead, _ := strings.Cut(counts, "\t")
		st.behind, _ = strconv.Atoi(behind)
		st.ahead, _ = strconv.Atoi(ahead)
	}
	out, err := g.Output("status", "--porcelain")
	if err != nil {
		return st, err
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case line == "":
		case strings.HasPrefix(line, "??"):
			st.untracked++
		default:
			st.changed++
		}
	}
	return st, nil
}
