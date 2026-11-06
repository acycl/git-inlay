// Package hooks installs the shims that call git-inlay's guards from the
// host's hooks and from each inlay's own hooks (PLAN.md section 7.3).
package hooks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/acycl/git-inlay/internal/atomicfile"
	"github.com/acycl/git-inlay/internal/host"
)

// Markers delimit shim content so it can be recognized and removed.
const (
	Begin = "# >>> git-inlay >>>"
	End   = "# <<< git-inlay <<<"
)

// names lists the hooks installed in the host repository.
var names = []string{"pre-commit", "pre-push"}

// State describes one hook file.
type State string

// Hook states.
const (
	Installed State = "installed" // our shim
	Managed   State = "managed"   // core.hooksPath is set, so a hook manager owns hooks
	Foreign   State = "foreign"   // a hook exists that is not ours
	Absent    State = "absent"
)

// Status is the state of one host hook.
type Status struct {
	Hook  string
	Path  string
	State State
}

// Shim returns the hook script that delegates to a git-inlay hook
// subcommand. It looks for git-inlay on PATH, as git itself does for git
// inlay, and falls back to the binary that wrote the shim, for a hook run
// by a GUI client that does not see the user's PATH. With neither in
// place it fails closed.
func Shim(sub string) string {
	hint := "Run 'git inlay hooks uninstall' in the host repository to remove this hook."
	if sub == "inlay-pre-push" {
		hint = "Reinstall git-inlay, or delete this hook file to push without the guard."
	}
	fallback := ""
	if bin := binary(); bin != "" {
		fallback = " || bin=" + shellQuote(bin)
	}
	return fmt.Sprintf(`#!/bin/sh
%s
bin=$(command -v git-inlay)%s
[ -x "$bin" ] || { echo "git-inlay: binary not on PATH${bin:+ or at $bin}; refusing. %s" >&2; exit 1; }
exec "$bin" hook %s "$@"
%s
`, Begin, fallback, hint, sub, End)
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

// shellQuote wraps s in single quotes for sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Statuses reports the state of every host hook, and core.hooksPath when a
// hook manager has set it.
func Statuses(h *host.Host) ([]Status, string, error) {
	hooksPath, err := h.HooksPath()
	if err != nil {
		return nil, "", err
	}
	if hooksPath != "" {
		var out []Status
		for _, name := range names {
			out = append(out, Status{Hook: name, Path: filepath.Join(hooksPath, name), State: Managed})
		}
		return out, hooksPath, nil
	}
	dir, err := h.HooksDir()
	if err != nil {
		return nil, "", err
	}
	var out []Status
	for _, name := range names {
		path := filepath.Join(dir, name)
		out = append(out, Status{Hook: name, Path: path, State: state(path)})
	}
	return out, "", nil
}

// state classifies one hook file.
func state(path string) State {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Absent
	case err != nil:
		return Foreign
	case strings.Contains(string(data), Begin):
		return Installed
	}
	return Foreign
}

// Install writes the shims where no other hook is in the way. With a hook
// manager in charge it writes nothing and returns core.hooksPath.
func Install(h *host.Host) ([]Status, string, error) {
	statuses, hooksPath, err := Statuses(h)
	if err != nil || hooksPath != "" {
		return statuses, hooksPath, err
	}
	for i, s := range statuses {
		if s.State == Foreign {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
			return nil, "", err
		}
		if err := atomicfile.WriteFile(s.Path, []byte(Shim(s.Hook)), 0o755); err != nil {
			return nil, "", err
		}
		statuses[i].State = Installed
	}
	return statuses, "", nil
}

// Uninstall removes the shims it recognizes and leaves everything else.
func Uninstall(h *host.Host) ([]Status, error) {
	statuses, _, err := Statuses(h)
	if err != nil {
		return nil, err
	}
	for i, s := range statuses {
		if s.State != Installed {
			continue
		}
		if err := os.Remove(s.Path); err != nil {
			return nil, err
		}
		statuses[i].State = Absent
	}
	return statuses, nil
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

// Snippet explains how to call the guards from a hook manager.
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
        run: git-inlay hook pre-push
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
`
	}
	return `hooks there are not managed by git-inlay. From them:
  pre-commit: run   git-inlay hook pre-commit
  pre-push:   run   git-inlay hook pre-push "$@"   with git's stdin forwarded
`
}
