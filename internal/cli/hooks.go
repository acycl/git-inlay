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
	"github.com/acycl/git-inlay/internal/hooks"
	"github.com/acycl/git-inlay/internal/host"
	"github.com/acycl/git-inlay/internal/inlay"
	"github.com/acycl/git-inlay/internal/remote"
)

// hooks manages the host's hook shims.
func (c *cli) hooks(args []string) error {
	if len(args) != 1 {
		return usagef("usage: git inlay hooks install|uninstall|status")
	}
	s, err := c.open()
	if err != nil {
		return err
	}
	var r *hooks.Report
	switch args[0] {
	case "install":
		r, err = hooks.Install(s.host)
		for _, e := range s.mine() {
			if err == nil {
				err = e.Inlay.Configure()
			}
		}
	case "uninstall":
		r, err = hooks.Uninstall(s.host)
	case "status":
		r, err = hooks.Statuses(s.host)
	default:
		return usagef("usage: git inlay hooks install|uninstall|status")
	}
	if err != nil {
		return err
	}
	for _, st := range r.Statuses {
		c.printf("%-11s %-10s %s\n", st.Hook, st.State, st.Path)
	}
	for _, e := range s.mine() {
		ok, err := e.Inlay.Configured()
		if err != nil {
			return err
		}
		state := "missing"
		if ok {
			state = "installed"
		}
		c.printf("%-11s %-10s %s\n", "inlay:"+e.Name, state, filepath.Join(e.Inlay.GitDir, "hooks", "pre-push"))
	}
	snippet := false
	for _, st := range r.Statuses {
		switch {
		case args[0] == "uninstall":
		case st.State == hooks.Disabled && args[0] == "install":
			c.printf("\n%s.enabled is still false in config that git reads after the repository's, so git skips the %s guard; find the setting with:\n  git config --show-scope --show-origin --get-all %s.enabled\n", st.Path, st.Hook, st.Path)
		case st.State == hooks.Disabled:
			c.printf("\n%s.enabled is false, so git skips the %s guard; run: git inlay hooks install\n", st.Path, st.Hook)
		case r.Fixable(st) && st.State != hooks.Absent:
			c.printf("\n%s is not managed by git-inlay; run git inlay hooks install to add the guard beside it through git config\n", st.Path)
		case st.State == hooks.Managed && !snippet:
			snippet = true
			c.printf("\ncore.hooksPath is %s, so %s", r.HooksPath, hooks.Snippet(hooks.Manager(s.host.Root)))
		case st.State == hooks.Foreign:
			c.printf("\n%s exists and is not managed by git-inlay; add to it:\n  git-inlay hook %s \"$@\"\n", st.Path, st.Hook)
		}
	}
	return nil
}

// hook runs one of the guards on behalf of a hook shim, or the briefing on
// behalf of a coding agent's session-start hook.
func (c *cli) hook(args []string) error {
	if len(args) == 0 {
		return usagef("usage: git inlay hook pre-commit|pre-push|inlay-pre-push|session-start [args]")
	}
	switch args[0] {
	case "pre-commit":
		return c.hookPreCommit()
	case "pre-push":
		return c.hookPrePush(args[1:])
	case "inlay-pre-push":
		return c.hookInlayPrePush(args[1:])
	case "session-start":
		if len(args) > 1 {
			return usagef("usage: git inlay hook session-start")
		}
		return c.hookSessionStart()
	}
	return usagef("unknown hook %q", args[0])
}

// hookPreCommit refuses a host commit that stages a claimed path.
func (c *cli) hookPreCommit() error {
	s, err := c.load()
	if err != nil {
		return err
	}
	// Git may be committing from a temporary index (commit -a, commit <path>);
	// inspect that one rather than .git/index.
	s.host.IndexFile = c.env("GIT_INDEX_FILE")
	x, err := s.guards()
	if err != nil {
		return err
	}
	vs, err := guard.Staged(s.host, x, s.attachedNames())
	if err != nil {
		return err
	}
	return c.refuse("commit", vs)
}

// hookPrePush refuses a host push whose commits touch a claimed path.
func (c *cli) hookPrePush(args []string) error {
	pos, err := parseArgs(c.flagSet("pre-push"), args)
	if err != nil {
		return err
	}
	remoteName := c.env("PRE_COMMIT_REMOTE_NAME")
	if len(pos) > 0 {
		remoteName = pos[0]
	}
	updates, err := c.refUpdates()
	if err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}
	s, err := c.load()
	if err != nil {
		return err
	}
	x, err := s.guards()
	if err != nil {
		return err
	}
	vs, err := guard.Outgoing(s.host, x, updates, remoteName)
	if err != nil {
		return err
	}
	return c.refuse("push", vs)
}

// refUpdates collects what is being pushed: from the pre-commit
// framework's variables when it set them, otherwise from git's stdin.
func (c *cli) refUpdates() ([]guard.RefUpdate, error) {
	to, from := c.env("PRE_COMMIT_TO_REF"), c.env("PRE_COMMIT_FROM_REF")
	if to != "" {
		if from == "" {
			from = strings.Repeat("0", 40)
		}
		return []guard.RefUpdate{{LocalRef: "HEAD", LocalOID: to, RemoteRef: "HEAD", RemoteOID: from}}, nil
	}
	return guard.ParseRefUpdates(c.o.Stdin)
}

// hookInlayPrePush runs inside an inlay's own pre-push hook: it refuses a
// push to any host remote and a push of paths outside the inlay's claims.
func (c *cli) hookInlayPrePush(args []string) error {
	gitDir := c.env("GIT_DIR")
	if gitDir == "" {
		return errors.New("GIT_DIR is not set; this hook must be run by git")
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(c.o.Dir, gitDir)
	}
	// Any existing directory serves as the work tree for reading config;
	// without one git would resolve core.worktree and refuse to start if
	// its parent directory is gone.
	r := gitx.Runner{GitDir: gitDir, WorkTree: c.o.Dir, Dir: c.o.Dir, Env: c.o.Env}
	name, ok, err := r.Config("inlay.name")
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("this repository is not an inlay")
	}
	worktree, _, err := r.Config("core.worktree")
	if err != nil {
		return err
	}
	if !filepath.IsAbs(worktree) {
		worktree = filepath.Join(gitDir, worktree)
	}
	common, hasHost, _ := r.Config("inlay.host")
	h, err := host.Discover(worktree, c.o.Env)
	if err == nil && hasHost && !sameDir(common, h.CommonDir) {
		// Another repository now sits where the worktree was; its remotes
		// and tree say nothing about this inlay.
		err = fmt.Errorf("the repository at %s is not the host of inlay %s", worktree, name)
	}
	if err != nil {
		// A removed linked worktree leaves its inlay behind. The host's
		// config and the inlay's own history still allow both checks, so
		// the push that prune advises for such an inlay can go through.
		if !hasHost {
			return err
		}
		if _, statErr := os.Stat(common); statErr != nil {
			return err
		}
		h = host.Detached(common, c.o.Env)
	}
	in := &inlay.Inlay{Name: name, Key: h.Key, GitDir: gitDir, Host: h}

	remoteName, url := "", ""
	if len(args) > 0 {
		remoteName = args[0]
	}
	if len(args) > 1 {
		url = args[1]
	}
	hostRemotes, err := remote.Configured(h.Git())
	if err != nil {
		return err
	}
	if url != "" {
		if err := guard.CheckRemoteURL(hostRemotes, url); err != nil {
			fmt.Fprintf(c.o.Stderr, "git-inlay: refusing to push inlay %s: %v\n", name, err)
			return exitError{exitViolation}
		}
	}

	updates, err := guard.ParseRefUpdates(c.o.Stdin)
	if err != nil {
		return err
	}
	names, err := in.RemoteNames()
	if err != nil {
		return err
	}
	named := slices.Contains(names, remoteName)
	var vs []guard.Violation
	seen := map[string]bool{}
	for _, u := range updates {
		if guard.IsZero(u.LocalOID) {
			continue
		}
		// The receiving side sees the manifest committed at the pushed tip,
		// so that is what the outgoing paths must satisfy (I3), whatever an
		// uncommitted edit of the working-tree manifest says.
		m, err := inlay.ManifestAt(in.Git(), name, u.LocalOID)
		if err != nil {
			return err
		}
		paths, err := guard.OutgoingPaths(in.Git(), []guard.RefUpdate{u}, remoteName, named)
		if err != nil {
			return err
		}
		for _, p := range paths {
			if _, ok := m.Owner(p); ok || seen[p] {
				continue
			}
			seen[p] = true
			vs = append(vs, guard.Violation{
				Path:   p,
				Inlay:  name,
				Reason: "is in the commits being pushed but under no claim of inlay " + name,
				Fix:    fmt.Sprintf("git inlay claim %s %q and commit, or remove it from the commits", name, p),
			})
		}
	}
	return c.refuse("push inlay "+name, vs)
}

// sameDir reports whether two paths name the same directory, allowing for
// symlinks such as macOS's /var.
func sameDir(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// refuse prints violations and returns the violation exit status.
func (c *cli) refuse(action string, vs []guard.Violation) error {
	if len(vs) == 0 {
		return nil
	}
	fmt.Fprintf(c.o.Stderr, "git-inlay: refusing to %s: %d path(s) out of place\n", action, len(vs))
	for _, v := range vs {
		fmt.Fprintf(c.o.Stderr, "  %s\n", v)
	}
	return exitError{exitViolation}
}
