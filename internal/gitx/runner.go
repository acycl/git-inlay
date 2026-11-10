// Package gitx runs git as a subprocess with explicit repository locations
// (docs/design.md section 7.1). The mechanics tests beside it pin down the git
// behaviours the design depends on.
package gitx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Runner executes git against one repository with explicit locations.
//
// The zero value runs git in the current directory with git's own discovery.
// Setting GitDir and WorkTree pins the repository regardless of where the
// process runs, which is how inlays are driven.
type Runner struct {
	GitDir   string   // absolute git directory; empty lets git discover it
	WorkTree string   // absolute working tree; empty lets git decide
	Dir      string   // working directory for the process; empty means the caller's
	Env      []string // base environment; nil means os.Environ()
	ExtraEnv []string // appended after scrubbing, so it wins; carries a hook's GIT_INDEX_FILE
}

// scrubbed lists the variables removed from every invocation so that an
// inherited repository location can never redirect a command.
var scrubbed = map[string]bool{
	"GIT_DIR":                          true,
	"GIT_WORK_TREE":                    true,
	"GIT_INDEX_FILE":                   true,
	"GIT_OBJECT_DIRECTORY":             true,
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_COMMON_DIR":                   true,
	"GIT_NAMESPACE":                    true,
}

// pathspecModes lists the variables that change how git reads every
// pathspec. They are removed from the commands git-inlay runs for its own
// consumption, whose pathspecs carry their own magic: GIT_LITERAL_PATHSPECS,
// which Magit sets, would turn ":(literal)README.md" into a file of that
// name, so a host-tracked path would look untracked. Passthrough keeps
// them, since there the pathspecs are the user's.
var pathspecModes = map[string]bool{
	"GIT_LITERAL_PATHSPECS": true,
	"GIT_GLOB_PATHSPECS":    true,
	"GIT_NOGLOB_PATHSPECS":  true,
	"GIT_ICASE_PATHSPECS":   true,
}

// Error reports a failed git command together with what it printed.
type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("git %s: %v", strings.Join(e.Args, " "), e.Err)
	if s := strings.TrimSpace(e.Stderr); s != "" {
		msg += ": " + s
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// ExitCode returns git's exit status, or -1 when git did not run.
func (e *Error) ExitCode() int {
	var exitErr *exec.ExitError
	if errors.As(e.Err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// Command builds the git process for args without starting it.
func (r Runner) Command(args ...string) *exec.Cmd {
	full := make([]string, 0, len(args)+2)
	if r.GitDir != "" {
		full = append(full, "--git-dir="+r.GitDir)
	}
	if r.WorkTree != "" {
		full = append(full, "--work-tree="+r.WorkTree)
	}
	full = append(full, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = r.Dir
	cmd.Env = r.environ()
	return cmd
}

// environ returns the base environment with repository variables removed.
func (r Runner) environ() []string {
	base := r.Env
	if base == nil {
		base = os.Environ()
	}
	env := make([]string, 0, len(base)+1)
	for _, kv := range base {
		key, _, _ := strings.Cut(kv, "=")
		if !scrubbed[key] {
			env = append(env, kv)
		}
	}
	return append(env, r.ExtraEnv...)
}

// Run runs git and reports failure with its stderr.
func (r Runner) Run(args ...string) error {
	_, err := r.capture(nil, args)
	return err
}

// Output runs git and returns stdout with trailing newlines removed.
func (r Runner) Output(args ...string) (string, error) {
	out, err := r.capture(nil, args)
	return strings.TrimRight(string(out), "\n"), err
}

// OutputWithInput is Output with the given bytes supplied on stdin.
func (r Runner) OutputWithInput(input []byte, args ...string) (string, error) {
	out, err := r.capture(input, args)
	return strings.TrimRight(string(out), "\n"), err
}

// Lines runs git and returns the lines of its stdout, or nil when it
// printed nothing.
func (r Runner) Lines(args ...string) ([]string, error) {
	out, err := r.Output(args...)
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// OutputZ runs git and splits its NUL-separated stdout into entries.
func (r Runner) OutputZ(args ...string) ([]string, error) {
	out, err := r.capture(nil, args)
	if err != nil {
		return nil, err
	}
	return splitZ(out), nil
}

// OutputZWithInput is OutputZ with the given bytes supplied on stdin.
func (r Runner) OutputZWithInput(input []byte, args ...string) ([]string, error) {
	out, err := r.capture(input, args)
	if err != nil {
		return nil, err
	}
	return splitZ(out), nil
}

// Config returns a configuration value and whether it is set.
func (r Runner) Config(key string) (string, bool, error) {
	out, err := r.Output("config", "--get", key)
	if err != nil {
		var gerr *Error
		if errors.As(err, &gerr) && gerr.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, err
	}
	return out, true, nil
}

// Path returns a configuration value that names a file, as git expands
// it ("~/", "~user/" and "%(prefix)/"), and whether it is set.
func (r Runner) Path(key string) (string, bool, error) {
	out, err := r.Output("config", "--type=path", "--get", key)
	if err != nil {
		var gerr *Error
		if errors.As(err, &gerr) && gerr.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, err
	}
	return out, true, nil
}

// Bool returns a boolean configuration value as git parses it, false when
// unset.
func (r Runner) Bool(key string) (bool, error) {
	out, err := r.Output("config", "--type=bool", "--get", key)
	if err != nil {
		var gerr *Error
		if errors.As(err, &gerr) && gerr.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	return out == "true", nil
}

// AtLeast reports whether git is version major.minor or newer. It parses
// "git version 2.54.0" and the vendor forms "2.47.1.windows.1" and
// "2.39.5 (Apple Git-154)".
func (r Runner) AtLeast(major, minor int) (bool, error) {
	out, err := r.Output("version")
	if err != nil {
		return false, err
	}
	f := strings.Fields(out)
	if len(f) < 3 {
		return false, fmt.Errorf("unexpected git version output %q", out)
	}
	parts := strings.SplitN(f[2], ".", 3)
	if len(parts) < 2 {
		return false, fmt.Errorf("unexpected git version output %q", out)
	}
	gotMajor, err1 := strconv.Atoi(parts[0])
	gotMinor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false, fmt.Errorf("unexpected git version output %q", out)
	}
	return gotMajor > major || gotMajor == major && gotMinor >= minor, nil
}

// Succeeds runs git and reports whether it exited zero. An exit status of 1
// is reported as false without an error, matching commands such as
// check-ignore that use 1 to mean "no".
func (r Runner) Succeeds(args ...string) (bool, error) {
	err := r.Run(args...)
	if err == nil {
		return true, nil
	}
	var gerr *Error
	if errors.As(err, &gerr) && gerr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// HoldSignals makes the process outlive an interrupt or quit from the
// terminal until release is called, for the time a git child owns the
// terminal. The terminal signals the whole foreground group, so git gets
// the signal too and decides what it means: it may be waiting on a pager
// or an editor that handles the key itself, and dying first would hand
// the terminal back to the shell while they still read from it. The
// signals are caught, not ignored, because a child inherits an ignored
// signal and git would then never see one. An interrupt that the process
// was started with ignored stays ignored, for git as well: catching it
// would hand git the default action, and a job put in the background by a
// script would then die of a key meant for the foreground. (The Go runtime
// keeps an inherited disposition only for interrupt and hangup, so a quit
// is always caught.)
//
// When the signal did end the child, the caller finishes its own work and
// then ends with Reraise.
func HoldSignals() (release func()) {
	var held []os.Signal
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGQUIT} {
		if !signal.Ignored(sig) {
			held = append(held, sig)
		}
	}
	if len(held) == 0 {
		return func() {}
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, held...)
	return func() { signal.Stop(ch) }
}

// Signaled returns the signal that ended the command err came from, if one
// did. On Windows no command ends that way.
func Signaled(err error) (syscall.Signal, bool) {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return 0, false
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return 0, false
	}
	return ws.Signal(), true
}

// Reraise ends the process after sig ended a child it held signals for.
// For an interrupt it dies of the signal itself: that, not a non-zero
// exit, is what tells a shell to abandon the loop or script the command
// ran in. For any other signal, and should the interrupt not take, it
// returns the exit status to use: 128 plus the signal, as a shell reports
// it. Only the interrupt is raised again because only there does the Go
// runtime do what the signal does to git: a quit would print a goroutine
// dump, and a broken pipe, the usual end of a git whose pager was closed,
// would be dropped.
func Reraise(sig syscall.Signal) int {
	if sig != syscall.SIGINT {
		return 128 + int(sig)
	}
	signal.Reset(sig)
	if p, err := os.FindProcess(os.Getpid()); err == nil && p.Signal(sig) == nil {
		time.Sleep(time.Second) // the signal is on its way
	}
	return 128 + int(sig)
}

// Passthrough runs git connected to the given streams and returns its exit
// status. The user's locale is kept so messages read as they normally would,
// and terminal signals are left to git (see HoldSignals), so the caller
// still runs when git has been interrupted; killed is then the signal that
// ended git, for Reraise, and zero otherwise.
func (r Runner) Passthrough(stdin io.Reader, stdout, stderr io.Writer, args ...string) (code int, killed syscall.Signal, err error) {
	defer HoldSignals()()
	cmd := r.Command(args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	err = cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		if sig, ok := Signaled(err); ok {
			return 128 + int(sig), sig, nil
		}
		return exitErr.ExitCode(), 0, nil
	case err != nil:
		return -1, 0, &Error{Args: args, Err: err}
	}
	return 0, 0, nil
}

// capture runs git for machine consumption: LC_ALL=C for stable output, the
// pathspec modes of the environment removed, and stderr collected into the
// error.
func (r Runner) capture(stdin []byte, args []string) ([]byte, error) {
	cmd := r.Command(args...)
	cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
		key, _, _ := strings.Cut(kv, "=")
		return pathspecModes[key]
	})
	cmd.Env = append(cmd.Env, "LC_ALL=C")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, &Error{Args: args, Stderr: stderr.String(), Err: err}
	}
	return out, nil
}

// splitZ splits NUL-terminated output into entries.
func splitZ(b []byte) []string {
	b = bytes.TrimSuffix(b, []byte{0})
	if len(b) == 0 {
		return nil
	}
	parts := bytes.Split(b, []byte{0})
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = string(p)
	}
	return out
}
