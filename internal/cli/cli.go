// Package cli implements the git-inlay command line (PLAN.md section 5).
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/acycl/git-inlay/internal/exclude"
	"github.com/acycl/git-inlay/internal/gitx"
	"github.com/acycl/git-inlay/internal/guard"
	"github.com/acycl/git-inlay/internal/hooks"
	"github.com/acycl/git-inlay/internal/host"
	"github.com/acycl/git-inlay/internal/inlay"
	"github.com/acycl/git-inlay/internal/manifest"
)

// Options is the process environment a command runs in. Tests supply their
// own so nothing global is touched.
type Options struct {
	Dir     string   // working directory; empty means the process's
	Env     []string // environment; nil means the process's
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Version string
}

// Exit statuses from PLAN.md section 7.5.
const (
	exitOK        = 0
	exitViolation = 1
	exitUsage     = 2
	exitGit       = 3
)

// reserved lists every verb, present and planned, so none can name an inlay.
var reserved = map[string]bool{
	"init": true, "clone": true, "attach": true, "detach": true, "claim": true,
	"unclaim": true, "ls": true, "which": true, "status": true, "check": true,
	"restore": true, "prune": true, "hooks": true, "hook": true, "version": true,
	"help": true,
}

const usage = `usage: git inlay <command> [args]

Keep private sets of files tracked in their own git repositories while they
live inside this repository's working tree.

  init <name> [--remote <url>]    create an empty inlay for this worktree
  clone <url> [<name>]            attach an inlay from its remote
  claim <name> <path>...          give paths to an inlay
  unclaim <name> <path>...        take paths back from an inlay
  ls [<name>]                     list claims
  which <path>...                 report who owns each path
  check [--staged]                verify every invariant
  hooks install|uninstall|status  manage the host's leak guards
  <name> <git command> [args]     run git against an inlay
  version                         print versions

Inlay state lives under .git/inlays. Nothing is ever added to the host's
commits: the host ignores every claimed path through .git/info/exclude.
`

// usageError is a command-line mistake; it exits 2.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// exitError carries a status for a refusal whose details were already
// printed.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

type cli struct{ o Options }

// Run executes args and returns the process exit status.
func Run(args []string, o Options) int {
	if o.Dir == "" {
		if wd, err := os.Getwd(); err == nil {
			o.Dir = wd
		}
	}
	if o.Stdin == nil {
		o.Stdin = strings.NewReader("")
	}
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	c := &cli{o: o}
	err := c.dispatch(args)
	if err == nil {
		return exitOK
	}
	var ue usageError
	var ee exitError
	var ge *gitx.Error
	switch {
	case errors.As(err, &ee):
		return ee.code
	case errors.As(err, &ue):
		fmt.Fprintf(o.Stderr, "git-inlay: %s\n", ue.msg)
		return exitUsage
	case errors.As(err, &ge):
		fmt.Fprintf(o.Stderr, "git-inlay: %v\n", err)
		return exitGit
	}
	fmt.Fprintf(o.Stderr, "git-inlay: %v\n", err)
	return exitViolation
}

// dispatch routes to a verb, or to passthrough when the first word names
// an inlay.
func (c *cli) dispatch(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(c.o.Stderr, usage)
		return exitError{exitUsage}
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "help", "-h", "--help":
		fmt.Fprint(c.o.Stdout, usage)
		return nil
	case "init":
		return c.init(rest)
	case "clone":
		return c.clone(rest)
	case "claim":
		return c.claim(rest)
	case "unclaim":
		return c.unclaim(rest)
	case "ls":
		return c.ls(rest)
	case "which":
		return c.which(rest)
	case "check":
		return c.check(rest)
	case "hooks":
		return c.hooks(rest)
	case "hook":
		return c.hook(rest)
	case "version":
		return c.version()
	}
	if reserved[verb] {
		return usagef("%s is not implemented yet", verb)
	}
	if err := manifest.ValidName(verb); err != nil {
		return usagef("unknown command %q", verb)
	}
	return c.passthrough(verb, rest)
}

// flagSet returns a quiet flag set; its errors surface as usage errors.
func (c *cli) flagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parseArgs parses flags wherever they appear and returns the positionals.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, usageError{err.Error()}
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func (c *cli) printf(format string, a ...any) { fmt.Fprintf(c.o.Stdout, format, a...) }

func (c *cli) warnf(format string, a ...any) {
	fmt.Fprintf(c.o.Stderr, "git-inlay: warning: "+format+"\n", a...)
}

// env looks up a variable in the command's environment.
func (c *cli) env(key string) string {
	if c.o.Env == nil {
		return os.Getenv(key)
	}
	for _, kv := range c.o.Env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// session is a discovered host with every inlay's manifest loaded.
type session struct {
	c       *cli
	host    *host.Host
	entries []inlay.Entry
	index   *guard.Index
}

// open discovers the host and runs the sync step from PLAN.md section 5:
// manifests are read and the generated exclude files brought up to date.
func (c *cli) open() (*session, error) {
	h, err := host.Discover(c.o.Dir, c.o.Env)
	if err != nil {
		return nil, err
	}
	s := &session{c: c, host: h}
	return s, s.refresh()
}

// load discovers the host and reads manifests without writing anything,
// which is what hooks need.
func (c *cli) load() (*session, error) {
	h, err := host.Discover(c.o.Dir, c.o.Env)
	if err != nil {
		return nil, err
	}
	s := &session{c: c, host: h}
	return s, s.reload()
}

// reload rereads every manifest.
func (s *session) reload() error {
	entries, err := inlay.All(s.host)
	if err != nil {
		return err
	}
	s.entries = entries
	s.index = guard.NewIndex(entries)
	return nil
}

// refresh rereads every manifest and regenerates the exclude files.
func (s *session) refresh() error {
	if err := s.reload(); err != nil {
		return err
	}
	if len(s.entries) == 0 {
		return nil
	}
	if err := exclude.WriteHost(s.host.ExcludeFile(), s.index.Claims()); err != nil {
		return err
	}
	for _, e := range s.entries {
		if e.Inlay == nil {
			continue
		}
		if err := e.Inlay.WriteExclude(e.Manifest); err != nil {
			return err
		}
		// The push guard's hooksPath is absolute, so a moved checkout loses
		// it; put it back on every command rather than only on check.
		ok, err := e.Inlay.GuardInstalled()
		if err != nil {
			return err
		}
		if !ok {
			if err := e.Inlay.InstallGuard(); err != nil {
				return err
			}
		}
	}
	s.verifyExcluded()
	return nil
}

// mine returns the entries attached to the current worktree.
func (s *session) mine() []inlay.Entry {
	var out []inlay.Entry
	for _, e := range s.entries {
		if e.Inlay != nil {
			out = append(out, e)
		}
	}
	return out
}

// entry returns the named inlay attached to the current worktree.
func (s *session) entry(name string) (inlay.Entry, error) {
	for _, e := range s.entries {
		if e.Inlay != nil && e.Name == name {
			return e, nil
		}
	}
	if err := manifest.ValidName(name); err != nil {
		return inlay.Entry{}, err
	}
	return inlay.Entry{}, fmt.Errorf("no inlay named %q is attached to this worktree", name)
}

// refuseSparse rejects hosts with sparse checkout, which is unsupported.
func (s *session) refuseSparse() error {
	sparse, err := s.host.SparseCheckout()
	if err != nil {
		return err
	}
	if sparse {
		return errors.New("the host uses sparse checkout, which git-inlay does not support")
	}
	return nil
}

// resolveClaim turns a command-line path into a claim, consulting the
// filesystem to decide whether it is a directory.
func (s *session) resolveClaim(p string) (manifest.Claim, error) {
	rel, err := s.host.Rel(s.c.o.Dir, p)
	if err != nil {
		return manifest.Claim{}, err
	}
	if icase, err := s.host.IgnoreCase(); err == nil && icase {
		rel = canonicalCase(s.host.Root, rel)
	}
	dir := strings.HasSuffix(p, "/") || strings.HasSuffix(p, string(os.PathSeparator))
	if fi, err := os.Stat(filepath.Join(s.host.Root, filepath.FromSlash(rel))); err == nil {
		dir = fi.IsDir()
	}
	if dir {
		rel += "/"
	}
	return manifest.ParseClaim(rel)
}

// vet checks a prospective claim for name against the host and every
// other inlay (invariant I1 and the submodule rule).
func (s *session) vet(name string, cl manifest.Claim) error {
	if o, ok := s.index.Owner(cl.Path); ok && o.Inlay != name {
		return fmt.Errorf("%s is already owned by inlay %s through its claim %s", cl, o.Inlay, o.Claim)
	}
	for _, e := range s.entries {
		if e.Name == name {
			continue
		}
		for _, other := range e.Manifest.Claims {
			if cl.Owns(other.Path) {
				return fmt.Errorf("%s would contain %s, which inlay %s claims", cl, other, e.Name)
			}
		}
	}
	// Before the tracked check: a path under a submodule matches the
	// gitlink in ls-files, and "git rm --cached" would be the wrong advice.
	subs, err := s.host.Submodules(append(cl.Ancestors(), cl.Path)...)
	if err != nil {
		return err
	}
	for p := range subs {
		return fmt.Errorf("%s is inside the submodule at %s", cl, p)
	}
	tracked, err := s.host.Tracked(cl.Pathspec())
	if err != nil {
		return err
	}
	if len(tracked) > 0 {
		shown := tracked
		if len(shown) > 5 {
			shown = shown[:5]
		}
		return fmt.Errorf("%s is tracked by the host (%s%s); remove it from the host first with git rm --cached",
			cl, strings.Join(shown, ", "), more(len(tracked)-len(shown)))
	}
	// A .gitignore negation outranks the managed block, so the host could
	// never be made to ignore the path.
	if src, pattern, ok := ignoreMatch(s.host.Git(), cl.Path); ok &&
		strings.HasPrefix(pattern, "!") && !strings.HasSuffix(filepath.ToSlash(src), "info/exclude") {
		return fmt.Errorf("%s is re-included by %s (pattern %s), which outranks the exclude file git-inlay manages, so the host cannot be made to ignore it; remove that pattern or choose another path", cl, src, pattern)
	}
	return nil
}

// more renders an "and N more" suffix for truncated lists.
func more(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf(" and %d more", n)
}

// hooksReminder warns when the host's leak guards are not installed.
func (s *session) hooksReminder() {
	statuses, hooksPath, err := hooks.Statuses(s.host)
	if err != nil || hooksPath != "" {
		return
	}
	for _, st := range statuses {
		if st.State != hooks.Installed {
			s.c.warnf("host leak guards are not installed; run: git inlay hooks install")
			return
		}
	}
}
