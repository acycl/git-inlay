package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/acycl/git-inlay/internal/gitx"
	"github.com/acycl/git-inlay/internal/guard"
	"github.com/acycl/git-inlay/internal/inlay"
	"github.com/acycl/git-inlay/internal/manifest"
	"github.com/acycl/git-inlay/internal/remote"
)

// init creates an empty inlay for the current worktree.
func (c *cli) init(args []string) error {
	fs := c.flagSet("init")
	remoteURL := fs.String("remote", "", "remote URL for the new inlay")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: git inlay init <name> [--remote <url>]")
	}
	name := pos[0]
	if reserved[name] {
		return usagef("%q is a git-inlay command and cannot name an inlay", name)
	}
	if err := manifest.ValidName(name); err != nil {
		return usageError{err.Error()}
	}
	s, err := c.open()
	if err != nil {
		return err
	}
	if err := s.refuseSparse(); err != nil {
		return err
	}
	if *remoteURL != "" {
		if err := s.checkRemote(*remoteURL); err != nil {
			return err
		}
	}
	in, err := inlay.Create(s.host, name)
	if err != nil {
		return err
	}
	m := manifest.New(name)
	err = in.WriteExclude(m)
	if err == nil {
		err = in.WriteManifest(m)
	}
	if err == nil {
		err = in.Git().Run("commit", "-q", "-m", "git-inlay: create inlay "+name)
	}
	if err == nil && *remoteURL != "" {
		err = in.Git().Run("remote", "add", "origin", *remoteURL)
	}
	if err != nil {
		// Never leave a half-made inlay behind: it would look attached.
		os.RemoveAll(in.GitDir)
		return err
	}
	if err := s.refresh(); err != nil {
		return err
	}
	c.printf("initialized inlay %s at %s\n", name, in.GitDir)
	c.printf("next: git inlay claim %s <path>...\n", name)
	s.hooksReminder()
	return nil
}

// claim gives paths to an inlay.
func (c *cli) claim(args []string) error {
	if len(args) < 2 {
		return usagef("usage: git inlay claim <name> <path>...")
	}
	name, paths := args[0], args[1:]
	s, err := c.open()
	if err != nil {
		return err
	}
	e, err := s.entry(name)
	if err != nil {
		return err
	}
	m := e.Manifest
	var added []manifest.Claim
	for _, p := range paths {
		cl, err := s.resolveClaim(p)
		if err != nil {
			return err
		}
		if err := s.vet(name, cl); err != nil {
			return err
		}
		absorbed, err := m.Add(cl)
		if errors.Is(err, manifest.ErrRedundant) {
			c.warnf("%s is already covered by a claim of inlay %s", cl, name)
			continue
		}
		if err != nil {
			return err
		}
		for _, a := range absorbed {
			c.warnf("%s now covers the earlier claim %s", cl, a)
		}
		added = append(added, cl)
	}
	if len(added) == 0 {
		return nil
	}
	if err := e.Inlay.WriteExclude(m); err != nil {
		return err
	}
	if err := e.Inlay.WriteManifest(m); err != nil {
		return err
	}
	files, err := s.stageable(e.Inlay, added)
	if err != nil {
		return err
	}
	if len(files) > 0 {
		// -f because a host .gitignore may cover the claim; stageable has
		// already applied the inlay's own rules and the global excludes.
		specs := make([]string, len(files))
		for i, f := range files {
			specs[i] = ":(literal)" + f
		}
		if err := e.Inlay.Git().Run(append([]string{"add", "-f", "--"}, specs...)...); err != nil {
			return err
		}
	}
	if err := s.refresh(); err != nil {
		return err
	}
	for _, cl := range added {
		c.printf("claimed %s for inlay %s\n", cl, name)
	}
	c.printf("staged %d file(s) in inlay %s; commit with: git inlay %s commit -m \"...\"\n", len(files), name, name)
	s.warnIgnored(e.Inlay, added)
	s.warnHistory(added)
	return nil
}

// unclaim takes paths back from an inlay. The files stay on disk.
func (c *cli) unclaim(args []string) error {
	if len(args) < 2 {
		return usagef("usage: git inlay unclaim <name> <path>...")
	}
	name, paths := args[0], args[1:]
	s, err := c.open()
	if err != nil {
		return err
	}
	e, err := s.entry(name)
	if err != nil {
		return err
	}
	m := e.Manifest
	var removed []string
	for _, p := range paths {
		rel, err := s.host.Rel(c.o.Dir, p)
		if err != nil {
			return err
		}
		if !m.Remove(rel) {
			return fmt.Errorf("inlay %s has no claim at %s", name, rel)
		}
		removed = append(removed, ":(literal)"+rel)
	}
	rm := append([]string{"rm", "-r", "-q", "--cached", "--ignore-unmatch", "--"}, removed...)
	if err := e.Inlay.Git().Run(rm...); err != nil {
		return err
	}
	if err := e.Inlay.WriteManifest(m); err != nil {
		return err
	}
	if err := e.Inlay.WriteExclude(m); err != nil {
		return err
	}
	if err := s.refresh(); err != nil {
		return err
	}
	for _, r := range removed {
		c.printf("unclaimed %s from inlay %s\n", strings.TrimPrefix(r, ":(literal)"), name)
	}
	c.warnf("the files stay on disk and are now visible to the host as untracked; commit the inlay to record the change")
	return nil
}

// ls lists claims.
func (c *cli) ls(args []string) error {
	fs := c.flagSet("ls")
	porcelain := fs.Bool("porcelain", false, "machine-readable output: one name<TAB>claim line per claim")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usagef("usage: git inlay ls [<name>] [--porcelain]")
	}
	s, err := c.open()
	if err != nil {
		return err
	}
	entries := s.mine()
	if len(pos) == 1 {
		e, err := s.entry(pos[0])
		if err != nil {
			return err
		}
		entries = []inlay.Entry{e}
	}
	if len(entries) == 0 {
		if *porcelain {
			return nil
		}
		c.printf("no inlays are attached to this worktree\n")
		if names := s.siblingNames(); len(names) > 0 {
			c.printf("attached in other worktrees: %s; copy them here with: git inlay attach --all\n", strings.Join(names, ", "))
		}
		return nil
	}
	for _, e := range entries {
		if len(e.Manifest.Claims) == 0 && !*porcelain {
			c.printf("%s\t(no claims)\n", e.Name)
		}
		for _, cl := range e.Manifest.Claims {
			c.printf("%s\t%s\n", e.Name, cl)
		}
	}
	return nil
}

// which reports who owns each path: an inlay, the host, or nobody.
func (c *cli) which(args []string) error {
	// --porcelain is accepted for symmetry; the output is already one
	// path<TAB>owner line per argument.
	fs := c.flagSet("which")
	fs.Bool("porcelain", false, "machine-readable output")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usagef("usage: git inlay which <path>... [--porcelain]")
	}
	s, err := c.open()
	if err != nil {
		return err
	}
	for _, p := range pos {
		rel, err := s.host.Rel(c.o.Dir, p)
		if err != nil {
			return err
		}
		owner := "none"
		if o, ok := s.index.Owner(rel); ok {
			owner = o.Inlay
		} else if tracked, err := s.host.Tracked(":(literal)" + rel); err != nil {
			return err
		} else if len(tracked) > 0 {
			owner = "host"
		}
		c.printf("%s\t%s\n", p, owner)
	}
	return nil
}

// check verifies every invariant and exits non-zero on a violation.
func (c *cli) check(args []string) error {
	fs := c.flagSet("check")
	staged := fs.Bool("staged", false, "check only the host's staged paths")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: git inlay check [--staged]")
	}
	s, err := c.open()
	if err != nil {
		return err
	}
	var vs []guard.Violation
	if *staged {
		vs, err = guard.Staged(s.host, s.index)
		if err != nil {
			return err
		}
	} else {
		vs = append(vs, guard.Overlaps(s.entries)...)
		hv, err := guard.HostTracked(s.host, s.index)
		if err != nil {
			return err
		}
		vs = append(vs, hv...)
		hostRemotes, err := remote.Configured(s.host.Git())
		if err != nil {
			return err
		}
		for _, e := range s.mine() {
			cv, err := guard.Containment(e.Inlay, e.Manifest)
			if err != nil {
				return err
			}
			vs = append(vs, cv...)
			rs, err := e.Inlay.Remotes()
			if err != nil {
				return err
			}
			vs = append(vs, guard.RemoteConflicts(hostRemotes, e.Name, rs)...)
			ok, err := e.Inlay.GuardInstalled()
			if err != nil {
				return err
			}
			if !ok {
				vs = append(vs, guard.Violation{Path: e.Name, Inlay: e.Name,
					Reason: "the inlay's push guard is missing or its core.hooksPath does not point at it",
					Fix:    "git inlay hooks install"})
			}
		}
		for _, e := range s.entries {
			gone, err := s.worktreeGone(e.Key)
			if err != nil {
				return err
			}
			if gone {
				vs = append(vs, guard.Violation{Path: e.Name, Inlay: e.Name,
					Reason: fmt.Sprintf("is attached to worktree %s, which no longer exists, so its claims still bind every worktree", e.Key),
					Fix:    "git inlay prune"})
			}
		}
		for _, cl := range s.index.Claims() {
			ok, err := s.host.Git().Succeeds("check-ignore", "-q", "--no-index", "--", cl.Path)
			if err != nil {
				return err
			}
			if !ok {
				vs = append(vs, guard.Violation{Path: cl.String(), Reason: "is not excluded in the host",
					Fix: "rerun any git inlay command to regenerate .git/info/exclude"})
			}
		}
	}
	if len(vs) > 0 {
		fmt.Fprintf(c.o.Stderr, "git-inlay: %d problem(s)\n", len(vs))
		for _, v := range vs {
			fmt.Fprintf(c.o.Stderr, "  %s\n", v)
		}
		return exitError{exitViolation}
	}
	claims := 0
	for _, e := range s.mine() {
		claims += len(e.Manifest.Claims)
	}
	c.printf("ok: %d inlay(s), %d claim(s), nothing out of place\n", len(s.mine()), claims)
	return nil
}

// clone attaches an inlay from its remote (PLAN.md section 7.2).
func (c *cli) clone(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return usagef("usage: git inlay clone <url> [<name>]")
	}
	url := args[0]
	s, err := c.open()
	if err != nil {
		return err
	}
	if err := s.refuseSparse(); err != nil {
		return err
	}
	if err := s.checkRemote(url); err != nil {
		return err
	}
	tmp, t, cleanup, err := s.stage()
	if err != nil {
		return err
	}
	if err := t.Run("remote", "add", "origin", url); err != nil {
		cleanup()
		return err
	}
	fetch := t.Command("fetch", "-q", "origin")
	fetch.Stdin, fetch.Stderr = c.o.Stdin, c.o.Stderr
	if err := fetch.Run(); err != nil {
		cleanup()
		return fmt.Errorf("fetch from %s failed", url)
	}
	branch, err := defaultBranch(t)
	if err != nil {
		cleanup()
		return err
	}
	name, err := manifestName(t, url, "origin/"+branch)
	if err != nil {
		cleanup()
		return err
	}
	if len(args) == 2 && args[1] != name {
		cleanup()
		return fmt.Errorf("the repository at %s is the inlay %q, not %q", url, name, args[1])
	}
	m, err := s.vetTree(t, name, "origin/"+branch, url)
	if err != nil {
		cleanup()
		return err
	}
	in, err := s.place(tmp, name, m, "checkout", "-q", "-b", branch, "--track", "origin/"+branch)
	if err != nil {
		cleanup()
		return err
	}
	if err := s.refresh(); err != nil {
		return err
	}
	c.printf("attached inlay %s from %s (branch %s, %d claims)\n", name, url, branch, len(m.Claims))
	s.attached(in, m)
	return nil
}

// checkRemote applies I4 to a candidate inlay remote URL.
func (s *session) checkRemote(url string) error {
	hostRemotes, err := remote.Configured(s.host.Git())
	if err != nil {
		return err
	}
	return guard.CheckRemoteURL(hostRemotes, s.resolveURL(url))
}

// stage creates the temporary bare repository that clone and attach fetch
// into. It lives under the inlays directory, so placing it is a rename.
func (s *session) stage() (tmp string, t gitx.Runner, cleanup func(), err error) {
	parent := filepath.Join(s.host.InlaysDir(), filepath.FromSlash(s.host.Key))
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", gitx.Runner{}, nil, err
	}
	tmp, err = os.MkdirTemp(parent, ".clone-")
	if err != nil {
		return "", gitx.Runner{}, nil, err
	}
	cleanup = func() { os.RemoveAll(tmp) }
	if err := s.host.Git().Run("init", "-q", "--bare", "-b", "main", tmp); err != nil {
		cleanup()
		return "", gitx.Runner{}, nil, err
	}
	return tmp, gitx.Runner{GitDir: tmp, Dir: s.host.Root, Env: s.host.Env}, cleanup, nil
}

// vetTree reads the manifest of inlay name at rev in the staged repository
// t and checks the tree there against this worktree: no inlay may already
// be attached under the name, every claim must pass vet, every tracked
// path must be under a claim, and nothing may sit where checkout would
// write. source says where the tree came from, for messages.
func (s *session) vetTree(t gitx.Runner, name, rev, source string) (*manifest.Manifest, error) {
	if _, err := os.Stat(inlay.Dir(s.host, s.host.Key, name)); err == nil {
		return nil, fmt.Errorf("inlay %q is already attached to this worktree", name)
	}
	m, err := inlay.ManifestAt(t, name, rev)
	if err != nil {
		return nil, err
	}
	for _, cl := range m.Claims {
		if err := s.vet(name, cl); err != nil {
			return nil, err
		}
	}
	// Checkout silently overwrites ignored files, and a host .gitignore may
	// well cover claimed paths, so refuse collisions here instead.
	files, err := t.OutputZ("ls-tree", "-r", "-z", "--name-only", rev)
	if err != nil {
		return nil, err
	}
	var outside, existing, blocking []string
	checkedAncestor := map[string]bool{}
	for _, p := range files {
		if _, ok := m.Owner(p); !ok {
			outside = append(outside, p)
		}
		if _, err := os.Lstat(filepath.Join(s.host.Root, filepath.FromSlash(p))); err == nil {
			existing = append(existing, p)
		}
		// A file where a directory must go would be deleted by checkout.
		for _, a := range (manifest.Claim{Path: p}).Ancestors() {
			if checkedAncestor[a] {
				continue
			}
			checkedAncestor[a] = true
			if fi, err := os.Lstat(filepath.Join(s.host.Root, filepath.FromSlash(a))); err == nil && !fi.IsDir() {
				blocking = append(blocking, a)
			}
		}
	}
	switch {
	case len(outside) > 0:
		return nil, fmt.Errorf("%s tracks paths outside its claims (%s); fix that inlay before attaching it", source, summarize(outside))
	case len(existing) > 0:
		return nil, fmt.Errorf("these files already exist in the working tree and would be overwritten: %s; move them aside and try again", summarize(existing))
	case len(blocking) > 0:
		return nil, fmt.Errorf("these files are in the way of the inlay's directories and would be deleted: %s; move them aside and try again", summarize(blocking))
	}
	return m, nil
}

// place moves the staged repository to its final location, adopts it as
// inlay name and runs the checkout command that populates the tree. Any
// failure removes the inlay again, so a half-attached inlay never remains.
func (s *session) place(tmp, name string, m *manifest.Manifest, checkout ...string) (*inlay.Inlay, error) {
	final := inlay.Dir(s.host, s.host.Key, name)
	if err := os.Rename(tmp, final); err != nil {
		return nil, err
	}
	in, err := inlay.Adopt(s.host, name, final)
	if err == nil {
		err = in.WriteExclude(m)
	}
	if err == nil {
		if err = in.Git().Run(checkout...); err != nil {
			err = fmt.Errorf("%w\nmove the files it lists aside and try again", err)
		}
	}
	if err != nil {
		os.RemoveAll(final)
		return nil, err
	}
	return in, nil
}

// attached prints the claims of a newly attached inlay and the warnings
// that apply to them.
func (s *session) attached(in *inlay.Inlay, m *manifest.Manifest) {
	for _, cl := range m.Claims {
		s.c.printf("  %s\n", cl)
	}
	s.warnIgnored(in, m.Claims)
	s.warnHistory(m.Claims)
	s.hooksReminder()
}

// defaultBranch asks the remote which branch its HEAD points at, falling
// back to main or the first branch fetched.
func defaultBranch(t gitx.Runner) (string, error) {
	if out, err := t.Output("ls-remote", "--symref", "origin", "HEAD"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if ref, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
				name, _, _ := strings.Cut(ref, "\t")
				return name, nil
			}
		}
	}
	branches, err := t.Output("for-each-ref", "--format=%(refname:short)", "refs/remotes/origin/")
	if err != nil {
		return "", err
	}
	if branches == "" {
		return "", errors.New("the remote has no branches")
	}
	for _, b := range strings.Split(branches, "\n") {
		if b == "origin/main" {
			return "main", nil
		}
	}
	first, _, _ := strings.Cut(branches, "\n")
	return strings.TrimPrefix(first, "origin/"), nil
}

// manifestName finds the single inlay manifest committed at rev and
// returns the inlay name it encodes.
func manifestName(t gitx.Runner, url, rev string) (string, error) {
	names, err := t.OutputZ("ls-tree", "-z", "--name-only", rev, "--", manifest.Dir+"/")
	if err != nil {
		return "", err
	}
	name := ""
	for _, n := range names {
		if !strings.HasSuffix(n, ".paths") {
			continue
		}
		if name != "" {
			return "", fmt.Errorf("%s holds manifests for several inlays; it is not a single inlay repository", url)
		}
		name = strings.TrimSuffix(strings.TrimPrefix(n, manifest.Dir+"/"), ".paths")
	}
	if name == "" {
		return "", fmt.Errorf("%s is not an inlay repository: no %s/<name>.paths at %s", url, manifest.Dir, rev)
	}
	if err := manifest.ValidName(name); err != nil {
		return "", err
	}
	return name, nil
}

// passthrough runs git against an inlay with the terminal attached.
func (c *cli) passthrough(name string, args []string) error {
	if len(args) == 0 {
		return usagef("usage: git inlay %s <git command> [args]", name)
	}
	s, err := c.open()
	if err != nil {
		return err
	}
	e, err := s.entry(name)
	if err != nil {
		return err
	}
	code, err := e.Inlay.GitAt(c.o.Dir).Passthrough(c.o.Stdin, c.o.Stdout, c.o.Stderr, args...)
	if err != nil {
		return err
	}
	if code != 0 {
		return exitError{code}
	}
	return nil
}

// version prints the tool and git versions.
func (c *cli) version() error {
	v := c.o.Version
	if v == "" {
		v = "dev"
	}
	gitVersion, _ := gitx.Runner{Dir: c.o.Dir, Env: c.o.Env}.Output("--version")
	c.printf("git-inlay %s\n%s\n", v, gitVersion)
	return nil
}
