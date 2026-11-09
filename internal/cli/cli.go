// Package cli implements the git-inlay command line (PLAN.md section 5).
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
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

// implemented lists every verb dispatch routes, so none can name an inlay.
var implemented = map[string]bool{
	"init": true, "clone": true, "attach": true, "detach": true, "claim": true,
	"unclaim": true, "ls": true, "which": true, "status": true, "check": true,
	"restore": true, "prune": true, "hooks": true, "hook": true, "version": true,
	"help": true,
}

// held lists names kept back for verbs a later release may add. A new
// inlay cannot take one, so that such a verb never hides an inlay; one
// that already has the name keeps working, since dispatch routes only the
// implemented verbs.
var held = map[string]bool{
	"all": true, "completion": true, "doctor": true, "each": true, "exec": true,
	"fetch": true, "list": true, "mv": true, "pull": true, "push": true,
	"repair": true, "show": true, "sync": true,
}

// reservedName refuses a name that is, or may become, a verb.
func reservedName(name string) error {
	switch {
	case implemented[name]:
		return fmt.Errorf("%q is a git-inlay command and cannot name an inlay", name)
	case held[name]:
		return fmt.Errorf("%q is reserved for a future git-inlay command and cannot name an inlay", name)
	}
	return nil
}

const usage = `usage: git inlay <command> [args]

Keep private sets of files tracked in their own git repositories while they
live inside this repository's working tree.

  init <name> [--remote <url>]            create an empty inlay for this worktree
  clone <url> [<name>]                    attach an inlay from its remote
  attach <name>... | --all                copy inlays from another worktree
                                          (these three install the host's guards
                                          where they fit, unless given --no-hooks)
  detach <name> [--force] [--keep-files]  remove an inlay from this worktree
  claim <name> <path>...                  give paths to an inlay
  unclaim <name> <path>...                take paths back from an inlay
  ls [<name>] [--porcelain]               list claims
  which <path>...                         report who owns each path
  status [--fetch] [--porcelain]          short status of every inlay here
  check [--staged] [--fix]                verify every invariant
  restore [<name>]                        re-check out missing inlay files
  prune [--dry-run] [--force]             remove inlays whose worktree is gone
  hooks install|uninstall|status          manage the host's leak guards
  <name> <git command> [args]             run git against an inlay
  version, --version                      print versions

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
	// Before the verb reads its arguments, so that no verb takes --help
	// for a path or an inlay; the hook family passes its arguments on.
	if implemented[verb] && verb != "hook" && wantsHelp(rest) {
		fmt.Fprint(c.o.Stdout, usage)
		return nil
	}
	switch verb {
	case "help", "-h", "--help":
		fmt.Fprint(c.o.Stdout, usage)
		return nil
	case "init":
		return c.init(rest)
	case "clone":
		return c.clone(rest)
	case "attach":
		return c.attach(rest)
	case "prune":
		return c.prune(rest)
	case "detach":
		return c.detach(rest)
	case "status":
		return c.status(rest)
	case "restore":
		return c.restore(rest)
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
	case "version", "--version":
		return c.version()
	}
	if err := manifest.ValidName(verb); err != nil {
		return usagef("unknown command %q", verb)
	}
	return c.passthrough(verb, rest)
}

// wantsHelp reports whether args ask for help with -h or --help before any
// "--".
func wantsHelp(args []string) bool {
	for _, a := range args {
		switch a {
		case "--":
			return false
		case "-h", "--help":
			return true
		}
	}
	return false
}

// flagSet returns a quiet flag set; its errors surface as usage errors.
func (c *cli) flagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parseArgs parses flags wherever they appear and returns the positionals.
// After a "--" everything is positional, even what looks like a flag.
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
		if n := len(args) - len(rest); n > 0 && args[n-1] == "--" {
			return append(pos, rest...), nil
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
	noHooks bool // --no-hooks: leave the host's hooks directory alone
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
	s.index = nil
	return nil
}

// guards returns the index that matches host paths against claims. It is
// built on first use: on a host with core.ignoreCase it folds case, and
// only the verbs that match paths need that setting read.
func (s *session) guards() (*guard.Index, error) {
	if s.index == nil {
		icase, err := s.host.IgnoreCase()
		if err != nil {
			return nil, err
		}
		s.index = guard.NewIndex(s.entries, icase)
	}
	return s.index, nil
}

// refresh rereads every manifest and regenerates the exclude files. With
// no inlay left it drops the managed block instead.
func (s *session) refresh() error {
	if err := s.reload(); err != nil {
		return err
	}
	if len(s.entries) == 0 {
		return exclude.RemoveHost(s.host.ExcludeFile())
	}
	if err := exclude.WriteHost(s.host.ExcludeFile(), guard.Union(s.entries)); err != nil {
		return err
	}
	for _, e := range s.mine() {
		if err := e.Inlay.WriteExclude(e.Manifest); err != nil {
			return err
		}
		// The inlay's config holds absolute paths, so a moved checkout
		// loses its push guard and its host; the stamp Configure leaves
		// tells, without a process, when to put them back.
		if !e.Inlay.Stamped() {
			if err := e.Inlay.Configure(); err != nil {
				return err
			}
		}
	}
	return nil
}

// staleExcludes reports exclude files that differ from what the sync step
// would write, for check, which does not run it: the host's managed block,
// whether it is stale, and each attached inlay's info/exclude.
func (s *session) staleExcludes() (vs []guard.Violation, hostStale bool, err error) {
	fix := "git inlay check --fix"
	current, err := exclude.HostCurrent(s.host.ExcludeFile(), guard.Union(s.entries))
	if err != nil {
		return nil, false, err
	}
	if !current {
		vs = append(vs, guard.Violation{Path: s.host.ExcludeFile(),
			Reason: "is missing the managed exclude block or has one that does not match the inlays' claims, so the host may track a claimed path or hide an unclaimed one",
			Fix:    fix})
	}
	for _, e := range s.mine() {
		if !e.Inlay.ExcludeCurrent(e.Manifest) {
			vs = append(vs, guard.Violation{Path: e.Name, Inlay: e.Name,
				Reason: "has a missing or out-of-date info/exclude, so the inlay may miss new files under its claims or show files outside them",
				Fix:    fix})
		}
	}
	return vs, !current, nil
}

// warnUnexcluded warns about claims the host does not ignore despite the
// managed block, which the verbs that change claims call: that is where
// the warning is actionable, and check reports the same as a violation.
func (s *session) warnUnexcluded() { s.warnUnexcludedOf(nil) }

// warnUnexcludedOf is warnUnexcluded for the claims of m alone, or for
// every claim when m is nil.
func (s *session) warnUnexcludedOf(m *manifest.Manifest) {
	missing, err := s.unexcluded()
	if err != nil {
		// A claim beyond a symlink makes check-ignore refuse the whole
		// batch. That is for check to report, not for every verb to fail
		// on, including the unclaim that would resolve it.
		s.c.warnf("could not verify that the host ignores every claim: %v", err)
	}
	for _, cl := range missing {
		if m != nil && !slices.Contains(m.All(), cl) {
			continue
		}
		s.c.warnf("the host does not ignore %s; a .gitignore pattern outranks the managed exclude block, so the host hooks are the only guard for it. Run: git inlay check", cl)
	}
}

// warnChangedClaims warns, after a passthrough command changed the claims
// of inlay e (as a pull of a teammate's claim does), about those of its
// claims that break what the claim verbs enforce: one that overlaps
// another inlay's, or a claimed path the host tracks (I1) or does not
// ignore (I2). Nothing vetted them on the way in, so check would otherwise
// be the first to say. Other inlays' problems are check's to report.
func (s *session) warnChangedClaims(e inlay.Entry) {
	var vs []guard.Violation
	x, err := s.guards()
	if err == nil {
		for _, o := range s.entries {
			if o.Name != e.Name {
				vs = append(vs, x.Overlaps([]inlay.Entry{e, o})...)
			}
		}
		var tracked []guard.Violation
		tracked, err = guard.HostTracked(s.host, x)
		for _, v := range tracked {
			if v.Inlay == e.Name {
				vs = append(vs, v)
			}
		}
	}
	if err != nil {
		s.c.warnf("could not verify the changed claims of inlay %s: %v", e.Name, err)
	}
	if len(vs) > 0 {
		s.c.warnf("the claims of inlay %s changed, and %d path(s) are out of place:", e.Name, len(vs))
		for _, v := range vs {
			fmt.Fprintf(s.c.o.Stderr, "  %s\n", v)
		}
	}
	s.warnUnexcludedOf(e.Manifest)
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

// attachedNames returns the names of the inlays attached to the current
// worktree.
func (s *session) attachedNames() []string {
	var names []string
	for _, e := range s.mine() {
		names = append(names, e.Name)
	}
	return names
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
// filesystem to decide whether it is a directory. A path that does not
// exist is a directory only with a trailing slash; guessed reports that
// its kind, a file, is such a guess.
func (s *session) resolveClaim(p string) (cl manifest.Claim, guessed bool, err error) {
	rel, err := s.host.Rel(s.c.o.Dir, p)
	if err != nil {
		return manifest.Claim{}, false, err
	}
	if icase, err := s.host.IgnoreCase(); err == nil && icase {
		rel = canonicalCase(s.host.Root, rel)
	}
	dir := strings.HasSuffix(p, "/") || strings.HasSuffix(p, string(os.PathSeparator))
	if fi, err := os.Stat(filepath.Join(s.host.Root, filepath.FromSlash(rel))); err == nil {
		dir = fi.IsDir()
	} else {
		guessed = !dir
	}
	if dir {
		rel += "/"
	}
	cl, err = manifest.ParseClaim(rel)
	return cl, guessed, err
}

// kindViolations reports the claims of e whose kind disagrees with the
// disk. The kind is fixed when the claim is made, so a file claim on a path
// that has since become a directory owns none of the files in it, though
// the host ignores them, and a directory claim on what is now a file is one
// the inlay's exclude file hides from it.
func (s *session) kindViolations(e inlay.Entry) []guard.Violation {
	var vs []guard.Violation
	for _, cl := range e.Manifest.Claims {
		fi, err := os.Stat(filepath.Join(s.host.Root, filepath.FromSlash(cl.Path)))
		if err != nil || fi.IsDir() == cl.Dir {
			continue
		}
		v := guard.Violation{Path: cl.String(), Inlay: e.Name,
			Reason: "is claimed as a file but is a directory, so the files in it are under no claim",
			Fix:    fmt.Sprintf("git inlay claim %s %q", e.Name, cl.Path+"/")}
		if cl.Dir {
			v.Reason = "is claimed as a directory but is a file, which inlay " + e.Name + " then ignores"
			v.Fix = fmt.Sprintf("git inlay claim %s %q", e.Name, cl.Path)
		}
		vs = append(vs, v)
	}
	return vs
}

// vet checks prospective claims for name against the host and every
// other inlay (invariant I1 and the submodule rule), and reports the first
// claim that fails. It asks git once per kind of check for the whole
// batch, not once per claim.
func (s *session) vet(name string, claims []manifest.Claim) error {
	if len(claims) == 0 {
		return nil
	}
	x, err := s.guards()
	if err != nil {
		return err
	}
	var probes, specs []string
	for _, cl := range claims {
		probes = append(append(probes, cl.Ancestors()...), cl.Path)
		specs = append(specs, cl.Pathspec())
	}
	// Claims under one directory share its ancestors.
	slices.Sort(probes)
	subs, err := s.host.Submodules(slices.Compact(probes)...)
	if err != nil {
		return err
	}
	tracked, err := s.host.Tracked(specs...)
	if err != nil {
		return err
	}
	rules := ignoreRules(s.host.Git(), claimPaths(claims))
	for _, cl := range claims {
		if o, ok := x.Owner(cl.Path); ok && o.Inlay != name {
			return fmt.Errorf("%s is already owned by inlay %s through its claim %s", cl, o.Inlay, o.Claim)
		}
		for _, e := range s.entries {
			if e.Name == name {
				continue
			}
			for _, other := range e.Manifest.Claims {
				if x.Owns(cl, other.Path) {
					return fmt.Errorf("%s would contain %s, which inlay %s claims", cl, other, e.Name)
				}
			}
		}
		// Before the tracked check: a path under a submodule matches the
		// gitlink in ls-files, and "git rm --cached" would be the wrong
		// advice.
		for _, p := range append(cl.Ancestors(), cl.Path) {
			if subs[p] {
				return fmt.Errorf("%s is inside the submodule at %s", cl, p)
			}
		}
		// A literal pathspec matches beneath a path even when the claim
		// names a file, so attribute hits as a directory claim would.
		var hits []string
		for _, p := range tracked {
			if x.Owns(manifest.Claim{Path: cl.Path, Dir: true}, p) {
				hits = append(hits, p)
			}
		}
		if len(hits) > 0 {
			shown := hits[:min(len(hits), 5)]
			return fmt.Errorf("%s is tracked by the host (%s%s); remove it from the host first with git rm --cached",
				cl, strings.Join(shown, ", "), more(len(hits)-len(shown)))
		}
		// A .gitignore negation outranks the managed block, so the host
		// could never be made to ignore the path.
		if r, ok := rules[cl.Path]; ok && strings.HasPrefix(r.pattern, "!") &&
			!strings.HasSuffix(filepath.ToSlash(r.src), "info/exclude") {
			return fmt.Errorf("%s is re-included by %s (pattern %s), which outranks the exclude file git-inlay manages, so the host cannot be made to ignore it; remove that pattern or choose another path", cl, r.src, r.pattern)
		}
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

// guardHost is what init, clone and attach do for the host's guards once
// an inlay is in place: unless the user declined with --no-hooks, install
// the shims that fit without touching anything of the host's (a free
// place in the hooks directory), then warn about a guard still not
// active. A new inlay is thereby guarded from its first commit, and a
// host with a hook manager or hooks of its own is left for the user to
// decide on with hooks install.
func (s *session) guardHost() {
	if !s.noHooks {
		placed, err := hooks.InstallFree(s.host)
		switch {
		case err != nil:
			s.c.warnf("could not install the host leak guards: %v", err)
		case len(placed) > 0:
			s.c.printf("installed the host's %s guard(s); git inlay hooks uninstall removes them\n", strings.Join(placed, " and "))
		}
	}
	s.hooksReminder()
}

// hooksReminder warns when a host leak guard is not active.
func (s *session) hooksReminder() {
	vs, warnings, err := s.hookViolations()
	if err != nil {
		return
	}
	if len(vs) > 0 {
		s.c.warnf("host leak guards are not active; run: git inlay hooks install")
	}
	for _, w := range warnings {
		s.c.warnf("%s", w)
	}
}

// hookViolations reports the host hooks that run no guard while hooks
// install could put one there. A hook that git-inlay can neither place a
// guard beside nor read, under a hook manager on git older than 2.54,
// comes back as a warning instead.
func (s *session) hookViolations() ([]guard.Violation, []string, error) {
	if len(s.entries) == 0 {
		return nil, nil, nil
	}
	r, err := hooks.Statuses(s.host)
	if err != nil {
		return nil, nil, err
	}
	var vs []guard.Violation
	var warnings []string
	for _, st := range r.Statuses {
		action := strings.TrimPrefix(st.Hook, "pre-")
		var why string
		switch {
		case st.Active():
			continue
		case !r.Fixable(st):
			warnings = append(warnings, fmt.Sprintf("cannot verify that %s runs the %s guard; it must call: git-inlay hook %s", st.Path, st.Hook, st.Hook))
			continue
		case st.State == hooks.Absent:
			why = "is not installed"
		case st.State == hooks.Inert:
			why = "is not executable (" + st.Path + "), so git skips it"
		case st.State == hooks.Disabled:
			why = "is disabled by " + st.Path + ".enabled"
		default:
			why = fmt.Sprintf("is %s (%s), so git-inlay cannot verify that it runs the guard", st.State, st.Path)
		}
		vs = append(vs, guard.Violation{
			Path:   "host " + st.Hook + " hook",
			Reason: why + ", so nothing refuses a " + action + " that carries a claimed path",
			Fix:    "git inlay hooks install",
		})
	}
	return vs, warnings, nil
}
