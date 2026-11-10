// Package hooks installs the shims that call git-inlay's guards from the
// host's hooks and from each inlay's own hooks (docs/design.md section 7.3).
package hooks

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/acycl/git-inlay/internal/atomicfile"
	"github.com/acycl/git-inlay/internal/gitx"
	"github.com/acycl/git-inlay/internal/host"
)

// Markers open and close a shim, which is how a hook file is recognized
// as one git-inlay wrote.
const (
	Begin = "# >>> git-inlay >>>"
	End   = "# <<< git-inlay <<<"
)

// guards lists the hooks that carry the host's leak guards.
var guards = []string{"pre-commit", "pre-push"}

// AttachHook is the host hook that attaches the repository's inlays to a
// new linked worktree (git runs post-checkout at the end of git worktree
// add). It is a convenience, not a guard: it is installed only on request,
// no check counts its absence, and it never fails a checkout.
const AttachHook = "post-checkout"

// configSince is the first git 2 minor release that runs hooks defined in
// config (hook.<name>.command and hook.<name>.event), which it does
// whatever core.hooksPath says.
const configSince = 54

// State describes one host hook.
type State string

// Hook states.
const (
	Installed  State = "installed"  // our shim in the hooks directory
	Inert      State = "inert"      // our shim without its executable bit, which git skips
	Configured State = "configured" // our hook.git-inlay-<hook> section in the host's config
	Disabled   State = "disabled"   // that section with enabled = false
	Managed    State = "managed"    // core.hooksPath is set, so a hook manager owns hooks
	Foreign    State = "foreign"    // a hook exists that is not ours
	Absent     State = "absent"
)

// Status is the state of one host hook.
type Status struct {
	Hook  string
	Path  string // the hook file, or the config section of a config-defined hook
	State State

	slot string // the hook file when the hooks directory is free or holds our shim
}

// Active reports whether git runs the guard for this hook.
func (s Status) Active() bool { return s.State == Installed || s.State == Configured }

// Report is the state of the host's guards.
type Report struct {
	Statuses  []Status
	HooksPath string // core.hooksPath, when a hook manager has set it
	Config    bool   // git runs config-defined hooks, so a guard can sit beside any other hook
}

// Fixable reports whether "git inlay hooks install" would make s active.
// A hook manager or a foreign hook stands in the way of a shim, so below
// git 2.54 only the user can wire the guard in there.
func (r *Report) Fixable(s Status) bool {
	switch s.State {
	case Absent, Disabled, Inert:
		return true
	case Managed, Foreign:
		return r.Config
	}
	return false
}

// Shim returns the hook script that delegates to a git-inlay hook
// subcommand. It looks for git-inlay on PATH, as git itself does for git
// inlay, and falls back to the binary that wrote the shim, for a hook run
// by a GUI client that does not see the user's PATH. With neither in
// place it fails closed.
func Shim(sub string) string {
	return fmt.Sprintf("#!/bin/sh\n%s\n%s \"$@\"\n%s\n", Begin, strings.Join(body(sub), "\n"), End)
}

// Command returns the host shim as one shell line, for a config-defined
// hook. Git runs such a command through the shell and appends "$@" when
// the hook has arguments, as it does for aliases, so the line ends
// without them.
func Command(sub string) string {
	return strings.Join(body(sub), "; ")
}

// body returns the lines shared by Shim and Command; each completes the
// final exec with the hook's arguments.
func body(sub string) []string {
	// With the binary gone, hooks uninstall cannot run either, so the hint
	// names what to remove by hand: the shim's own file, or the config
	// section when git runs this line from there.
	hint := "Reinstall git-inlay, or remove this guard: the " + sub + " file in the hooks directory, or the hook.git-inlay-" + sub + " section of .git/config."
	if sub == "inlay-pre-push" {
		hint = "Reinstall git-inlay, or delete this hook file to push without the guard."
	}
	fallback := ""
	if bin := binary(); bin != "" {
		fallback = " || bin=" + ShellQuote(bin)
	}
	missing := fmt.Sprintf(`{ echo "git-inlay: binary not on PATH${bin:+ or at $bin}; refusing. %s" >&2; exit 1; }`, hint)
	if sub == AttachHook {
		// The hook's exit status becomes that of the checkout, which has
		// done its work; a missing binary only means nothing is attached.
		missing = "exit 0"
	}
	return []string{
		"bin=$(command -v git-inlay)" + fallback,
		`[ -x "$bin" ] || ` + missing,
		`exec "$bin" hook ` + sub,
	}
}

// binary returns the path of the running git-inlay, with forward slashes
// so that sh on Windows reads it, or "" when it cannot be found.
func binary() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.ToSlash(exe)
}

// ShellQuote wraps s in single quotes for sh, which also runs the
// commands git starts through the shell, such as --upload-pack.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sectionPrefix starts the config section of every git-inlay
// config-defined hook.
const sectionPrefix = "hook.git-inlay-"

// section returns the config section of the config-defined hook for name.
func section(name string) string { return sectionPrefix + name }

// sections returns the keys of git-inlay's config-defined hooks, by hook
// name and then by key without the section prefix. With local set it
// reads the repository config alone, which is what hooks install writes
// and removes; otherwise it reads every scope and include, as git does
// when it runs hooks, with the last value of a key winning. Extra
// arguments go to git config, such as --type=bool.
func sections(run gitx.Runner, local bool, pattern string, extra ...string) (map[string]map[string]string, error) {
	args := []string{"config", "-z"}
	if local {
		args = append(args, "--local")
	}
	args = append(append(args, extra...), "--get-regexp", pattern)
	entries, err := run.OutputZ(args...)
	if err != nil {
		var gerr *gitx.Error
		if errors.As(err, &gerr) && gerr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	out := map[string]map[string]string{}
	for _, e := range entries {
		k, v, ok := strings.Cut(e, "\n")
		if !ok {
			v = "true" // a key with no value is a true boolean
		}
		name, key, ok := strings.Cut(strings.TrimPrefix(k, sectionPrefix), ".")
		if !ok {
			continue
		}
		if out[name] == nil {
			out[name] = map[string]string{}
		}
		out[name][key] = v
	}
	return out, nil
}

// allSections is the pattern that matches every key of every git-inlay
// config-defined hook.
const allSections = `^hook\.git-inlay-`

// ours reports whether cfg is a config-defined hook that runs the guard
// for name: one hooks install wrote, whatever binary it recorded, and not
// a copy cut short or edited by hand.
func ours(cfg map[string]string, name string) bool {
	cmd := cfg["command"]
	return cfg["event"] == name && strings.HasPrefix(cmd, "bin=$(command -v git-inlay)") &&
		strings.HasSuffix(cmd, `exec "$bin" hook `+name)
}

// Statuses reports the state of every host hook. Our shim comes first, as
// the form every git version runs; a config-defined hook counts where git
// runs it, judged by the config git reads when it does; otherwise the
// hooks directory decides.
func Statuses(h *host.Host) (*Report, error) { return statuses(h, guards) }

// AttachStatus reports the state of the hook that attaches inlays to a new
// worktree, as Statuses does for the guards.
func AttachStatus(h *host.Host) (*Report, error) { return statuses(h, []string{AttachHook}) }

// statuses is Statuses for the hooks named.
func statuses(h *host.Host, names []string) (*Report, error) {
	run := h.Git()
	hooksPath, err := h.HooksPath()
	if err != nil {
		return nil, err
	}
	supported, err := h.GitAtLeast(2, configSince)
	if err != nil {
		return nil, err
	}
	dir := hooksPath
	if dir == "" {
		dir = h.HooksDir()
	}
	r := &Report{HooksPath: hooksPath, Config: supported}
	var cfg map[string]map[string]string
	off := map[string]bool{}
	if supported {
		if cfg, err = sections(run, false, allSections); err != nil {
			return nil, err
		}
		for name, c := range cfg {
			if v, ok := c["enabled"]; ok && v != "true" {
				off[name] = true
			}
		}
		// Let git parse the booleans it will act on, which take forms
		// such as "0k" that a hand-written parser would miss. Git refuses
		// a value it cannot parse at all, and would fail running the hook
		// on it too, so such a value stays off for hooks install to
		// rewrite.
		if len(off) > 0 {
			if typed, err := sections(run, false, `^hook\.git-inlay-[^.]*\.enabled$`, "--type=bool"); err == nil {
				for name := range off {
					off[name] = typed[name]["enabled"] == "false"
				}
			}
		}
	}
	for _, name := range names {
		s := Status{Hook: name, Path: filepath.Join(dir, name)}
		file := Managed
		if hooksPath == "" {
			file = state(s.Path)
		}
		if file == Absent || file == Installed || file == Inert {
			s.slot = s.Path
		}
		switch {
		case file == Installed:
			s.State = Installed
		case ours(cfg[name], name):
			s.Path, s.State = section(name), Configured
			if off[name] {
				s.State = Disabled
			}
		default:
			s.State = file
		}
		r.Statuses = append(r.Statuses, s)
	}
	return r, nil
}

// state classifies one hook file.
func state(path string) State {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Absent
	case err != nil:
		return Foreign
	case !shim(string(data)):
		return Foreign
	case !Runnable(path):
		return Inert
	}
	return Installed
}

// shim reports whether a hook file is one Shim wrote and nothing more. A
// file that holds the markers among lines of the user's own is theirs:
// install would otherwise write over those lines and uninstall delete
// them.
func shim(data string) bool {
	return strings.HasPrefix(data, "#!/bin/sh\n"+Begin+"\n") &&
		strings.HasSuffix(strings.TrimRight(data, "\r\n"), End)
}

// Runnable reports whether git would run the hook file at path: on every
// system but Windows, which has no such bit, it skips a hook that is not
// executable, with no more than a hint.
func Runnable(path string) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	fi, err := os.Stat(path)
	return err == nil && fi.Mode()&0o111 != 0
}

// WriteShim writes the shim for sub at path, executable. An existing file
// keeps its mode through an atomic replacement, so the bit is set again
// after it: that is what repairs a shim that lost it.
func WriteShim(path, sub string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(path, []byte(Shim(sub)), 0o755); err != nil {
		return err
	}
	if Runnable(path) {
		return nil
	}
	return os.Chmod(path, 0o755)
}

// Install puts a guard in place for every hook: a shim where the hooks
// directory has room for one, since every git version runs it, dropping
// a config-defined hook that would run the guard twice; otherwise, on git
// 2.54 or newer, a config-defined hook that runs beside the hook manager's
// or the foreign hook. What it cannot place it leaves. It reports the
// state that results, read back, so a guard that config read after the
// repository's still disables shows as such.
func Install(h *host.Host) (*Report, error) {
	_, r, err := install(h, guards, true)
	return r, err
}

// InstallAttach puts the hook that attaches inlays to a new worktree in
// place, as Install does a guard: a shim where the hooks directory has
// room, otherwise a config-defined hook on git 2.54 or newer, otherwise
// nothing, which the state it reports shows.
func InstallAttach(h *host.Host) (*Report, error) {
	_, r, err := install(h, []string{AttachHook}, true)
	return r, err
}

// InstallFree is the part of Install that asks nothing of the host: a
// shim for each hook whose place in the hooks directory is free, and
// nothing where a hook manager or a foreign hook stands, not even a
// config-defined hook beside it. It is what init, clone and attach do
// unasked, so that a new inlay is guarded from its first commit. It
// returns the hooks it placed a shim for that had none before, and the
// state that results.
func InstallFree(h *host.Host) (placed []string, after *Report, err error) {
	return install(h, guards, false)
}

// install is Install for the hooks named, with config-defined hooks only
// when config is set. It also returns the hooks whose shim it wrote where
// none was active.
func install(h *host.Host, names []string, config bool) (placed []string, after *Report, err error) {
	r, err := statuses(h, names)
	if err != nil {
		return nil, nil, err
	}
	run := h.Git()
	local, err := sections(run, true, allSections)
	if err != nil {
		return nil, nil, err
	}
	configChanged := false
	for i, s := range r.Statuses {
		switch {
		case s.slot != "":
			if err := WriteShim(s.slot, s.Hook); err != nil {
				return nil, nil, err
			}
			if s.State != Installed {
				placed = append(placed, s.Hook)
			}
			if len(local[s.Hook]) > 0 {
				if err := dropSection(run, s.Hook); err != nil {
					return nil, nil, err
				}
				configChanged = true
			}
			// A shim just written is installed, whatever stood there.
			r.Statuses[i].Path, r.Statuses[i].State = s.slot, Installed
		case config && r.Config:
			if err := configure(run, s.Hook, local[s.Hook], s.State != Configured); err != nil {
				return nil, nil, err
			}
			configChanged = true
		}
	}
	if !configChanged {
		// Only files were written, and their state is known: reading it
		// all back would cost the processes of a second look for nothing.
		return placed, r, nil
	}
	after, err = statuses(h, names)
	return placed, after, err
}

// dropSection removes the config-defined hook that a shim in this
// worktree's hooks directory makes redundant. The section is shared by
// every worktree, though, and with extensions.worktreeConfig set another
// worktree may point core.hooksPath elsewhere and rely on it, so then it
// stays; the guard running twice here is the lesser cost.
func dropSection(run gitx.Runner, name string) error {
	if perWorktree, err := run.Bool("extensions.worktreeConfig"); err != nil || perWorktree {
		return err
	}
	return run.Run("config", "--local", "--remove-section", section(name))
}

// configure makes the hook's section in the repository config, got, hold
// exactly the current command and event, enabled, leaving the config file
// alone when it already does and the section is in effect. The explicit
// enabled = true outranks a broader scope's enabled = false, which would
// otherwise switch the guard off unseen by anyone looking at the
// repository. A section that holds the right keys but is not in effect,
// rewrite set, is overridden by an include later in the file, so writing
// it afresh, at the end, puts it after the include.
func configure(run gitx.Runner, name string, got map[string]string, rewrite bool) error {
	want := map[string]string{"command": Command(name), "event": name, "enabled": "true"}
	if !rewrite && maps.Equal(got, want) {
		return nil
	}
	if len(got) > 0 {
		if err := run.Run("config", "--local", "--remove-section", section(name)); err != nil {
			return err
		}
	}
	for _, k := range []string{"command", "event", "enabled"} {
		if err := run.Run("config", "--local", section(name)+"."+k, want[k]); err != nil {
			return err
		}
	}
	return nil
}

// Uninstall removes the shims and config sections it recognizes, the
// attach hook's among them, and leaves everything else. It reports the
// guards.
func Uninstall(h *host.Host) (*Report, error) {
	run := h.Git()
	local, err := sections(run, true, allSections)
	if err != nil {
		return nil, err
	}
	all := append(slices.Clone(guards), AttachHook)
	for _, name := range all {
		if len(local[name]) == 0 {
			continue
		}
		if err := run.Run("config", "--local", "--remove-section", section(name)); err != nil {
			return nil, err
		}
	}
	r, err := statuses(h, all)
	if err != nil {
		return nil, err
	}
	for i, s := range r.Statuses {
		if s.State != Installed && s.State != Inert {
			continue
		}
		if err := os.Remove(s.Path); err != nil {
			return nil, err
		}
		r.Statuses[i].State = Absent
	}
	r.Statuses = r.Statuses[:len(guards)]
	return r, nil
}

// Manager guesses which hook manager the host uses, to pick a snippet.
func Manager(root string) string {
	for _, c := range []struct{ marker, name string }{
		{".husky", "husky"},
		{"lefthook.yml", "lefthook"},
		{"lefthook.yaml", "lefthook"},
		{".lefthook.yml", "lefthook"},
		{".pre-commit-config.yaml", "pre-commit"},
	} {
		if _, err := os.Stat(filepath.Join(root, c.marker)); err == nil {
			return c.name
		}
	}
	return ""
}

// Snippet explains how to call the guards from a hook manager. The
// pre-push guard needs what git gives the hook: the remote's name as the
// first argument, without which it cannot leave out the commits the remote
// already has, and the pushed refs on stdin. Husky scripts get both;
// lefthook passes the arguments as {0} and stdin on request; the
// pre-commit framework passes neither and sets PRE_COMMIT_* variables.
func Snippet(manager string) string {
	switch manager {
	case "husky":
		return `husky manages the hooks. Add these lines:
  .husky/pre-commit:  git-inlay hook pre-commit
  .husky/pre-push:    git-inlay hook pre-push "$@"
`
	case "lefthook":
		return `lefthook manages the hooks. Add to lefthook.yml:
  pre-commit:
    commands:
      git-inlay:
        run: git-inlay hook pre-commit
  pre-push:
    commands:
      git-inlay:
        run: git-inlay hook pre-push {0}
        use_stdin: true
`
	case "pre-commit":
		return `the pre-commit framework manages the hooks. Add to .pre-commit-config.yaml:
  - repo: local
    hooks:
      - id: git-inlay-pre-commit
        name: git-inlay leak guard
        entry: git-inlay hook pre-commit
        language: system
        pass_filenames: false
        always_run: true
        stages: [pre-commit]
      - id: git-inlay-pre-push
        name: git-inlay leak guard
        entry: git-inlay hook pre-push
        language: system
        pass_filenames: false
        always_run: true
        stages: [pre-push]
and install its pre-push hook, which pre-commit install alone leaves out:
  pre-commit install --hook-type pre-commit --hook-type pre-push
`
	}
	return `hooks there are not managed by git-inlay. From them:
  pre-commit: run   git-inlay hook pre-commit
  pre-push:   run   git-inlay hook pre-push "$@"   with git's stdin forwarded
`
}
