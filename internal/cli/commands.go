package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/acycl/git-inlay/internal/gitx"
	"github.com/acycl/git-inlay/internal/guard"
	"github.com/acycl/git-inlay/internal/host"
	"github.com/acycl/git-inlay/internal/inlay"
	"github.com/acycl/git-inlay/internal/manifest"
	"github.com/acycl/git-inlay/internal/remote"
)

// init creates an empty inlay for the current worktree.
func (c *cli) init(args []string) error {
	fs := c.flagSet("init")
	remoteURL := fs.String("remote", "", "remote URL for the new inlay")
	noHooks := fs.Bool("no-hooks", false, "do not install the host's guards")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: git inlay init <name> [--remote <url>] [--no-hooks]")
	}
	name := pos[0]
	if err := reservedName(name, true); err != nil {
		return usageError{err.Error()}
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
		// Never leave a half-made inlay behind: it would look attached, and
		// its manifest would sit in the tree with nothing to hide it.
		os.RemoveAll(in.GitDir)
		os.Remove(in.ManifestFile())
		os.Remove(filepath.Dir(in.ManifestFile()))
		return err
	}
	if err := s.refresh(); err != nil {
		return err
	}
	s.warnUnexcluded() // the manifest is a claim too
	c.printf("initialized inlay %s at %s\n", name, in.GitDir)
	c.printf("next: git inlay claim %s <path>...\n", name)
	s.noHooks = *noHooks
	s.guardHost()
	return nil
}

// claim gives paths to an inlay.
func (c *cli) claim(args []string) error {
	pos, err := parseArgs(c.flagSet("claim"), args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return usagef("usage: git inlay claim <name> [--] <path>...")
	}
	name, paths := pos[0], pos[1:]
	s, err := c.open()
	if err != nil {
		return err
	}
	e, err := s.entry(name)
	if err != nil {
		return err
	}
	m := e.Manifest
	claims := make([]manifest.Claim, 0, len(paths))
	var guessed []manifest.Claim
	var resolveErr error
	for _, p := range paths {
		cl, guess, err := s.resolveClaim(p)
		if err != nil {
			resolveErr = err
			break
		}
		claims = append(claims, cl)
		if guess {
			guessed = append(guessed, cl)
		}
	}
	// Report the first path that fails, whether resolving or vetting.
	if err := s.vet(name, claims); err != nil {
		return err
	}
	if resolveErr != nil {
		return resolveErr
	}
	var added []manifest.Claim
	for _, cl := range claims {
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
		// The files go on stdin: a claimed directory may hold more than a
		// command line carries, a few hundred on Windows.
		if _, err := e.Inlay.Git().OutputZWithInput(literalSpecs(files),
			"add", "-f", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return err
		}
	}
	if err := s.refresh(); err != nil {
		return err
	}
	s.warnUnexcluded()
	for _, cl := range added {
		c.printf("claimed %s for inlay %s\n", cl, name)
	}
	c.printf("staged %d file(s) in inlay %s; commit with: git inlay %s commit -m \"...\"\n", len(files), name, name)
	for _, cl := range added {
		if slices.Contains(guessed, cl) {
			c.warnf("%s does not exist yet and is claimed as a file; if it is to be a directory, claim it with a trailing slash: git inlay claim %s %s", cl, name, guard.Quote(":(top)"+cl.Path+"/"))
		}
	}
	s.warnIgnored(e.Inlay, added)
	s.warnHistory(added)
	return nil
}

// unclaim takes paths back from an inlay. The files stay on disk.
func (c *cli) unclaim(args []string) error {
	pos, err := parseArgs(c.flagSet("unclaim"), args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return usagef("usage: git inlay unclaim <name> [--] <path>...")
	}
	name, paths := pos[0], pos[1:]
	s, err := c.open()
	if err != nil {
		return err
	}
	e, err := s.entry(name)
	if err != nil {
		return err
	}
	m := e.Manifest
	icase, err := s.host.IgnoreCase()
	if err != nil {
		return err
	}
	var removed []string
	for _, p := range paths {
		rel, err := s.host.Rel(c.o.Dir, p)
		if err != nil {
			return err
		}
		if icase {
			// As claim spells a path the way the disk does, a claim is
			// named here in any case, even with its file gone.
			for _, cl := range m.Claims {
				if host.Fold(cl.Path) == host.Fold(strings.TrimSuffix(rel, "/")) {
					rel = cl.Path
				}
			}
		}
		if !m.Remove(rel) {
			if cl, ok := m.Owner(rel); ok && cl != m.Self() {
				return fmt.Errorf("inlay %s has no claim at %s; the claim %s covers it and can only be given up whole: git inlay unclaim %s %s", name, rel, cl, name, guard.Quote(":(top)"+cl.String()))
			}
			return fmt.Errorf("inlay %s has no claim at %s", name, rel)
		}
		removed = append(removed, ":(literal)"+rel)
	}
	// -f: git rm refuses, even with --cached, a file staged with content
	// that differs from both HEAD and the tree, which a claimed file edited
	// before its first commit is. Only index entries go; the files stay.
	rm := append([]string{"rm", "-r", "-q", "-f", "--cached", "--ignore-unmatch", "--"}, removed...)
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
	s.warnUnexcluded()
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
	// It prints one path<TAB>owner line per argument, fit for scripts.
	fs := c.flagSet("which")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usagef("usage: git inlay which <path>...")
	}
	s, err := c.open()
	if err != nil {
		return err
	}
	x, err := s.guards()
	if err != nil {
		return err
	}
	// The host is asked once for every path no inlay owns, not once each.
	rels := make([]string, len(pos))
	owners := make([]string, len(pos))
	var specs []string
	for i, p := range pos {
		if rels[i], err = s.host.Rel(c.o.Dir, p); err != nil {
			return err
		}
		if o, ok := x.Owner(rels[i]); ok {
			owners[i] = o.Inlay
		} else {
			specs = append(specs, ":(literal)"+rels[i])
		}
	}
	tracked, err := s.host.Tracked(specs...)
	if err != nil {
		return err
	}
	for i, p := range pos {
		if owners[i] == "" {
			owners[i] = "none"
			// A literal pathspec matches the path and what lies beneath it.
			under := manifest.Claim{Path: rels[i], Dir: true}
			if slices.ContainsFunc(tracked, func(t string) bool { return rels[i] == "." || x.Owns(under, t) }) {
				owners[i] = "host"
			}
		}
		c.printf("%s\t%s\n", p, owners[i])
	}
	return nil
}

// check verifies every invariant and exits non-zero on a violation.
func (c *cli) check(args []string) error {
	fs := c.flagSet("check")
	staged := fs.Bool("staged", false, "check only the host's staged paths")
	fix := fs.Bool("fix", false, "regenerate the exclude files and repair the inlays' config first")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: git inlay check [--staged] [--fix]")
	}
	// Read only: check reports what is on disk rather than repairing it
	// first, and works on a read-only .git. With --fix it runs the sync
	// step first, as every other verb does, which regenerates the exclude
	// files and repairs the inlays' config, and then reports what is left.
	open := c.load
	if *fix {
		open = c.open
	}
	s, err := open()
	if err != nil {
		return err
	}
	x, err := s.guards()
	if err != nil {
		return err
	}
	var vs []guard.Violation
	if *staged {
		vs, err = guard.Staged(s.host, x, s.attachedNames())
		if err != nil {
			return err
		}
	} else {
		vs = append(vs, x.Overlaps(s.entries)...)
		hv, err := guard.HostTracked(s.host, x)
		if err != nil {
			return err
		}
		vs = append(vs, hv...)
		hostRemotes, err := remote.Configured(s.host.Git())
		if err != nil {
			return err
		}
		for _, e := range s.mine() {
			// check skips the sync step that repairs a moved checkout or
			// an upgraded push guard, and that step trusts its stamp in
			// any case, so read the config itself.
			if ok, err := e.Inlay.Configured(); err != nil {
				return err
			} else if !ok {
				vs = append(vs, guard.Violation{Path: e.Name, Inlay: e.Name,
					Reason: "has a config that does not name this host, so its push guard may not run or may check the wrong host",
					Fix:    "git inlay hooks install"})
			}
			cv, err := guard.Containment(e.Inlay, e.Manifest)
			if err != nil {
				return err
			}
			vs = append(vs, cv...)
			vs = append(vs, s.kindViolations(e)...)
			// Nothing in git-inlay's hands fixes this, so it is a warning
			// and not a problem that fails the check.
			if _, exposed, err := untracked(e); err != nil {
				return err
			} else if len(exposed) > 0 {
				c.warnf("inlay %s does not ignore %d host file(s) (%s): a .gitignore pattern re-includes them and outranks the inlay's exclude file. They show as untracked in git inlay %s status; keep them out of git inlay %s add, and expect clean and stash -u to be refused",
					e.Name, len(exposed), summarize(exposed), e.Name, e.Name)
			}
			rs, err := e.Inlay.Remotes()
			if err != nil {
				return err
			}
			vs = append(vs, guard.RemoteConflicts(hostRemotes, e.Name, rs)...)
		}
		for _, e := range s.entries {
			if e.Gone {
				vs = append(vs, guard.Violation{Path: e.Name, Inlay: e.Name,
					Reason: fmt.Sprintf("is attached to worktree %s, which no longer exists, so its claims still bind every worktree", e.Key),
					Fix:    "git inlay prune"})
			}
		}
		hv, warnings, err := s.hookViolations()
		if err != nil {
			return err
		}
		vs = append(vs, hv...)
		for _, w := range warnings {
			c.warnf("%s", w)
		}
		stale, hostStale, err := s.staleExcludes()
		if err != nil {
			return err
		}
		vs = append(vs, stale...)
		// Whether the host ignores each claim depends on the block, so
		// that test waits until the block is current.
		var missing []manifest.Claim
		if !hostStale {
			if missing, err = s.unexcluded(); err != nil {
				return err
			}
		}
		for _, cl := range missing {
			v := guard.Violation{Path: cl.String(), Reason: "is not ignored by the host: a .gitignore pattern outranks the managed exclude block",
				Fix: "remove the pattern that re-includes it from .gitignore"}
			if o, ok := x.Owner(cl.Path); ok {
				v.Inlay = o.Inlay
				v.Fix += fmt.Sprintf("   or   git inlay unclaim %s %s", o.Inlay, guard.Quote(":(top)"+cl.String()))
			}
			vs = append(vs, v)
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

// clone attaches an inlay from its remote (docs/design.md section 7.2).
func (c *cli) clone(args []string) error {
	fs := c.flagSet("clone")
	noHooks := fs.Bool("no-hooks", false, "do not install the host's guards")
	args, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(args) < 1 || len(args) > 2 {
		return usagef("usage: git inlay clone <url> [<name>] [--no-hooks]")
	}
	url := args[0]
	s, err := c.open()
	if err != nil {
		return err
	}
	s.noHooks = *noHooks
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
	// With the terminal attached, so credential prompts work, and with its
	// signals held, so an interrupted fetch still gets its staging
	// directory removed.
	fetch := t.Command("fetch", "-q", "origin")
	fetch.Stdin, fetch.Stderr = c.o.Stdin, c.o.Stderr
	release := gitx.HoldSignals()
	err = fetch.Run()
	release()
	if err != nil {
		cleanup()
		if sig, ok := gitx.Signaled(err); ok {
			return exitError{gitx.Reraise(sig)}
		}
		return &gitx.Error{Args: []string{"fetch", "-q", "origin"}, Err: fmt.Errorf("fetch from %s failed: %w", url, err)}
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
	s.warnUnexcluded()
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
	if err := s.vet(name, m.Claims); err != nil {
		return nil, err
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
	s.guardHost()
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
	branches, err := t.Lines("for-each-ref", "--format=%(refname:short)", "refs/remotes/origin/")
	if err != nil {
		return "", err
	}
	if len(branches) == 0 {
		return "", errors.New("the remote has no branches")
	}
	if slices.Contains(branches, "origin/main") {
		return "main", nil
	}
	return strings.TrimPrefix(branches[0], "origin/"), nil
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
	// Attached under a verb's name, no passthrough command could reach it.
	if err := reservedName(name, false); err != nil {
		return "", fmt.Errorf("%s: %w", url, err)
	}
	return name, nil
}

// passthrough runs git against an inlay with the terminal attached, then
// runs the sync step again: a pull, merge, checkout or reset can change a
// manifest, and without it the host would show newly claimed files until
// the next command. Claims that changed are vetted as check would, and
// paths the inlay has come to track outside its claims are reported.
func (c *cli) passthrough(name string, args []string) error {
	if len(args) == 0 {
		return usagef("usage: git inlay %s <git command> [args]", name)
	}
	s, err := c.open()
	var broken *inlay.ManifestError
	if errors.As(err, &broken) && broken.Name == name {
		return c.passthroughUnsynced(name, args, broken)
	}
	if err != nil {
		return err
	}
	e, err := s.entry(name)
	if err != nil {
		return err
	}
	run := e.Inlay.GitAt(c.o.Dir)
	expanded := resolveAliases(run, args)
	if err := refuseTreeWide(e.Inlay, args, expanded); err != nil {
		return err
	}
	if verb, ok := untrackedWide(expanded); ok {
		_, exposed, err := untracked(e)
		if err != nil {
			return err
		}
		if len(exposed) > 0 {
			return fmt.Errorf("refusing to run git %s against inlay %s: it would %s %d host file(s) the inlay does not ignore (%s), because a .gitignore pattern re-includes them and outranks the inlay's exclude file; run it deliberately with: git --git-dir=%s --work-tree=%s %s",
				strings.Join(args, " "), name, verb, len(exposed), summarize(exposed), e.Inlay.GitDir, s.host.Root, strings.Join(args, " "))
		}
	}
	before := e.Manifest.Claims
	tracked, listed := trackedPaths(e)
	code, killed, err := run.Passthrough(c.o.Stdin, c.o.Stdout, c.o.Stderr, args...)
	if err != nil {
		return err
	}
	if killed != 0 {
		// Git died of a signal from the terminal, which this process held
		// off to get this far. With the sync done it ends the same way,
		// so that a shell abandons the loop or script it ran in; code is
		// already what a shell reports for the signal, should that fail.
		defer gitx.Reraise(killed)
	}
	// Even a failed command may have changed a manifest, as a merge that
	// stops on conflicts does. A failed command's own exit status wins,
	// since scripts read it (diff --quiet answers 1 for a difference).
	syncErr := s.refresh()
	if syncErr == nil {
		if after, err := s.entry(name); err == nil {
			if !slices.Equal(before, after.Manifest.Claims) {
				s.warnChangedClaims(after)
			}
			if listed {
				s.warnNewStrays(after, tracked)
			}
		}
	}
	if code != 0 {
		if syncErr != nil {
			c.warnf("the exclude files were not brought up to date: %v", syncErr)
		}
		return exitError{code}
	}
	if syncErr != nil {
		return fmt.Errorf("git succeeded, but the exclude files were not brought up to date: %w", syncErr)
	}
	return nil
}

// passthroughUnsynced is passthrough for an inlay whose manifest does not
// parse, as after a pull that brought a bad line or a merge that left
// conflict markers. Every other command stops there, but git run against
// the inlay is how the manifest is put right (a checkout of the file, a
// reset, a merge --abort), so it runs, without the sync before it. The
// forms that act on untracked files are refused, since without claims
// nothing tells the inlay's files from the host's.
func (c *cli) passthroughUnsynced(name string, args []string, cause error) error {
	h, err := host.Discover(c.o.Dir, c.o.Env)
	if err != nil {
		return err
	}
	in, err := inlay.Open(h, name)
	if err != nil {
		return err
	}
	run := in.GitAt(c.o.Dir)
	expanded := resolveAliases(run, args)
	if err := refuseTreeWide(in, args, expanded); err != nil {
		return err
	}
	if _, ok := untrackedWide(expanded); ok {
		return fmt.Errorf("%w; refusing to run git %s against it until the manifest parses", cause, strings.Join(args, " "))
	}
	c.warnf("%v; running git all the same, so that the manifest can be repaired", cause)
	code, killed, err := run.Passthrough(c.o.Stdin, c.o.Stdout, c.o.Stderr, args...)
	if err != nil {
		return err
	}
	if killed != 0 {
		defer gitx.Reraise(killed)
	}
	// The sync, if the command repaired the manifest.
	if _, err := c.open(); err != nil {
		c.warnf("the exclude files were not brought up to date: %v", err)
	}
	if code != 0 {
		return exitError{code}
	}
	return nil
}

// refuseTreeWide refuses the passthrough command args, which is expanded
// once its aliases are resolved, when it would act on the whole host tree
// (see treeWide).
func refuseTreeWide(in *inlay.Inlay, args, expanded []string) error {
	verb, ok := treeWide(expanded)
	if !ok {
		return nil
	}
	what := "git " + strings.Join(args, " ")
	if !slices.Equal(expanded, args) {
		what += ", an alias of git " + strings.Join(expanded, " ") + ","
	}
	return fmt.Errorf("refusing to run %s against inlay %s: it would %s every file the inlay ignores, which is the whole host tree; run it in the host instead, or deliberately with: git --git-dir=%s --work-tree=%s %s",
		what, in.Name, verb, in.GitDir, in.Host.Root, strings.Join(args, " "))
}

// trackedPaths returns the paths inlay e tracks, or ok false when they
// cannot be listed.
func trackedPaths(e inlay.Entry) (paths map[string]bool, ok bool) {
	files, err := e.Inlay.Tracked()
	if err != nil {
		return nil, false
	}
	paths = make(map[string]bool, len(files))
	for _, p := range files {
		paths[p] = true
	}
	return paths, true
}

// warnNewStrays warns, after a passthrough command, about the paths
// outside its claims (I3) that inlay e has come to track, before being
// what it tracked when the command began. A pull or checkout of a commit
// that carries such a path writes it into the tree, over the host's file
// if there is one, since from the inlay's side every host file is an
// ignored file and git overwrites those without a word; clone vets the
// incoming tree for this, and nothing can before a pull, whose commits are
// unknown until git has run. A path the inlay already tracked does not
// repeat, nor does one a changed manifest has left outside the claims,
// which no command wrote: check reports those.
func (s *session) warnNewStrays(e inlay.Entry, before map[string]bool) {
	vs, err := guard.Containment(e.Inlay, e.Manifest)
	if err != nil {
		return
	}
	vs = slices.DeleteFunc(vs, func(v guard.Violation) bool { return before[v.Path] })
	if len(vs) == 0 {
		return
	}
	s.c.warnf("inlay %s now tracks %d path(s) outside its claims; if this command wrote them into the tree, a host file at such a path was overwritten, and the host's git status shows it:", e.Name, len(vs))
	for _, v := range vs[:min(len(vs), 10)] {
		fmt.Fprintf(s.c.o.Stderr, "  %s\n", v)
	}
	if n := len(vs) - 10; n > 0 {
		fmt.Fprintf(s.c.o.Stderr, "  and %d more; see them with: git inlay check\n", n)
	}
}

// treeWide recognizes the passthrough forms that act on ignored files, and
// says what they would do to them. From the inlay's side everything outside
// its claims is ignored, bar what untrackedWide is for, so git clean -x
// would delete the whole host tree,
// git clean -e '!pattern' could un-ignore it, and git stash --all would
// stash it.
func treeWide(args []string) (verb string, ok bool) {
	i := gitSubcommand(args)
	if i < 0 {
		return "", false
	}
	switch args[i] {
	case "clean":
		return "remove", cleanTreeWide(args[i+1:])
	case "stash":
		return "stash", stashTreeWide(args[i+1:])
	}
	return "", false
}

// untrackedWide recognizes the passthrough forms that act on every
// untracked file, and says what they would do to them: a clean that is not
// a dry run, and a stash that includes untracked files. They are safe as
// long as the inlay's untracked files are all its own, which passthrough
// verifies (see untracked).
func untrackedWide(args []string) (verb string, ok bool) {
	i := gitSubcommand(args)
	if i < 0 {
		return "", false
	}
	switch args[i] {
	case "clean":
		opts, _ := gitArgs(args[i+1:], "e", "--exclude")
		dry := slices.ContainsFunc(opts, func(o gitOption) bool {
			return o.name == "n" || abbreviates(o.name, "--dry-run")
		})
		return "remove", !dry
	case "stash":
		opts, pos := gitArgs(args[i+1:], "m", "--message")
		if len(pos) > 0 && pos[0] != "push" && pos[0] != "save" {
			return "", false
		}
		return "stash", slices.ContainsFunc(opts, func(o gitOption) bool {
			return o.name == "u" || abbreviates(o.name, "--include-untracked")
		})
	}
	return "", false
}

// resolveAliases replaces an alias at the command position with what git
// would run in its place, repeatedly, so that treeWide sees the command
// behind it. It follows git's rules: configuration given with -c on the
// command line counts, and a name that is a command is never an alias. A
// shell alias cannot be read and stays, as does a lookup that fails or
// loops, which git then reports itself.
func resolveAliases(run gitx.Runner, args []string) []string {
	seen := map[string]bool{}
	var commands []string // what git runs before it looks for an alias
	for {
		i := gitSubcommand(args)
		if i < 0 || seen[args[i]] {
			return args
		}
		verb := args[i]
		seen[verb] = true
		if verb == "clean" || verb == "stash" {
			return args // builtins, so this is what runs
		}
		cfg := configOptions(args[:i])
		value, err := run.Output(slices.Concat(cfg, []string{"config", "--get", "alias." + verb})...)
		if err != nil || strings.HasPrefix(value, "!") {
			return args
		}
		if commands == nil {
			commands, err = run.Lines(slices.Concat(cfg, []string{"--list-cmds=builtins,main,others"})...)
			if err != nil {
				return args
			}
		}
		if slices.Contains(commands, verb) {
			return args // git ignores an alias that hides a command
		}
		words, ok := splitAlias(value)
		if !ok || len(words) == 0 {
			return args
		}
		args = slices.Concat(args[:i], words, args[i+1:])
	}
}

// configOptions returns the -c and --config-env options among git's global
// options, which set configuration for that one command.
func configOptions(globals []string) []string {
	var out []string
	for i := 0; i < len(globals); i++ {
		switch a := globals[i]; {
		case a == "-c" || a == "--config-env":
			if i+1 < len(globals) {
				out = append(out, a, globals[i+1])
				i++
			}
		case strings.HasPrefix(a, "--config-env="):
			out = append(out, a)
		}
	}
	return out
}

// splitAlias splits an alias value into words the way git does: on
// whitespace outside quotes, with single and double quotes grouping and a
// backslash escaping the next character except inside single quotes. It
// reports false for an unterminated quote or a trailing backslash, which
// git rejects too.
func splitAlias(s string) ([]string, bool) {
	var words []string
	var word []byte
	var quote byte
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == 0 && strings.ContainsRune(" \t\n\r\v\f", rune(c)):
			if inWord {
				words = append(words, string(word))
				word, inWord = word[:0], false
			}
		case quote == 0 && (c == '\'' || c == '"'):
			quote, inWord = c, true
		case c == quote:
			quote = 0
		default:
			if c == '\\' && quote != '\'' {
				i++
				if i == len(s) {
					return nil, false
				}
				c = s[i]
			}
			word, inWord = append(word, c), true
		}
	}
	if quote != 0 {
		return nil, false
	}
	if inWord {
		words = append(words, string(word))
	}
	return words, true
}

// gitSubcommand returns the index of the command word in args, after
// git's own options, which may precede it; -1 when there is none.
func gitSubcommand(args []string) int {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return i
		}
		switch a {
		case "-c", "-C", "--git-dir", "--work-tree", "--namespace", "--shallow-file", "--config-env", "--attr-source":
			i++ // the option's value
		}
	}
	return -1
}

// cleanTreeWide reports whether a clean would reach ignored files: -x or
// -X asks for them, and a command-line exclude that starts with "!"
// outranks every ignore file, so it can un-ignore the whole tree.
func cleanTreeWide(args []string) bool {
	opts, _ := gitArgs(args, "e", "--exclude")
	for _, o := range opts {
		switch {
		case o.name == "x" || o.name == "X":
			return true
		case (o.name == "e" || abbreviates(o.name, "--exclude")) && strings.HasPrefix(o.value, "!"):
			return true
		}
	}
	return false
}

// stashTreeWide reports whether a stash would take ignored files along.
// Only push, spelled out or implied, has that meaning for --all; show and
// list have their own -a and --all.
func stashTreeWide(args []string) bool {
	opts, pos := gitArgs(args, "m", "--message")
	if len(pos) > 0 && pos[0] != "push" && pos[0] != "save" {
		return false
	}
	for _, o := range opts {
		if o.name == "a" || abbreviates(o.name, "--all") {
			return true
		}
	}
	return false
}

// abbreviates reports whether name is the long option full or a prefix of
// it, which git accepts whenever the prefix is unambiguous.
func abbreviates(name, full string) bool {
	return len(name) > len("--") && strings.HasPrefix(full, name)
}

// gitOption is one option as git's parser sees it: a short letter or a
// long name, with its value when it takes one.
type gitOption struct {
	name  string // "x" for -x, "--all" for --all
	value string
}

// gitArgs splits a command's arguments, up to "--", into options and
// positionals the way git's parser does. The letters in shortValued and
// the names in longValued, or abbreviations of them, take a required
// value, attached (-mfoo, --message=foo) or as the next argument; a letter
// that does so ends its cluster.
func gitArgs(args []string, shortValued string, longValued ...string) (opts []gitOption, pos []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return opts, pos
		case strings.HasPrefix(a, "--"):
			name, value, has := strings.Cut(a, "=")
			valued := slices.ContainsFunc(longValued, func(full string) bool { return abbreviates(name, full) })
			if !has && valued && i+1 < len(args) {
				i++
				value = args[i]
			}
			opts = append(opts, gitOption{name, value})
		case strings.HasPrefix(a, "-") && len(a) > 1:
			cluster := a[1:]
			for j := range len(cluster) {
				o := gitOption{name: cluster[j : j+1]}
				if strings.ContainsRune(shortValued, rune(cluster[j])) {
					o.value = cluster[j+1:]
					if o.value == "" && i+1 < len(args) {
						i++
						o.value = args[i]
					}
					opts = append(opts, o)
					break
				}
				opts = append(opts, o)
			}
		default:
			pos = append(pos, a)
		}
	}
	return opts, pos
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
