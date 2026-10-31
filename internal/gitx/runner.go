package gitx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
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

// Passthrough runs git connected to the given streams and returns its exit
// status. The user's locale is kept so messages read as they normally would.
func (r Runner) Passthrough(stdin io.Reader, stdout, stderr io.Writer, args ...string) (int, error) {
	cmd := r.Command(args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), nil
	case err != nil:
		return -1, &Error{Args: args, Err: err}
	}
	return 0, nil
}

// capture runs git for machine consumption: LC_ALL=C for stable output and
// stderr collected into the error.
func (r Runner) capture(stdin []byte, args []string) ([]byte, error) {
	cmd := r.Command(args...)
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
