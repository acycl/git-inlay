// Package inlay manages the private repositories attached to a host
// (PLAN.md sections 4.1, 4.2 and 7.2).
package inlay

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/acycl/git-inlay/internal/exclude"
	"github.com/acycl/git-inlay/internal/gitx"
	"github.com/acycl/git-inlay/internal/hooks"
	"github.com/acycl/git-inlay/internal/host"
	"github.com/acycl/git-inlay/internal/manifest"
	"github.com/acycl/git-inlay/internal/remote"
)

// Inlay is one private repository attached to one host worktree.
type Inlay struct {
	Name   string
	Key    string
	GitDir string
	Host   *host.Host
}

// Dir returns where the git dir of inlay name lives for a worktree key.
func Dir(h *host.Host, key, name string) string {
	return filepath.Join(h.InlaysDir(), filepath.FromSlash(key), name)
}

// isRepo reports whether dir looks like a git dir.
func isRepo(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, "HEAD"))
	return err == nil && !fi.IsDir()
}

// Open returns the inlay attached to the host's current worktree.
func Open(h *host.Host, name string) (*Inlay, error) {
	if err := manifest.ValidName(name); err != nil {
		return nil, err
	}
	dir := Dir(h, h.Key, name)
	if !isRepo(dir) {
		return nil, fmt.Errorf("no inlay named %q is attached to this worktree", name)
	}
	return &Inlay{Name: name, Key: h.Key, GitDir: dir, Host: h}, nil
}

// Create initializes an empty inlay for the host's current worktree.
func Create(h *host.Host, name string) (*Inlay, error) {
	if err := manifest.ValidName(name); err != nil {
		return nil, err
	}
	dir := Dir(h, h.Key, name)
	if isRepo(dir) {
		return nil, fmt.Errorf("inlay %q is already attached to this worktree", name)
	}
	if err := h.Git().Run("init", "-q", "--bare", "-b", "main", dir); err != nil {
		return nil, err
	}
	return Adopt(h, name, dir)
}

// Adopt configures an existing bare git dir as an inlay of the host's
// current worktree and installs its push guard.
func Adopt(h *host.Host, name, dir string) (*Inlay, error) {
	in := &Inlay{Name: name, Key: h.Key, GitDir: dir, Host: h}
	run := in.Git()
	for _, kv := range [][2]string{
		{"core.bare", "false"},
		{"core.worktree", h.Root},
		{"inlay.name", name},
		{"inlay.host", h.CommonDir},
	} {
		if err := run.Run("config", kv[0], kv[1]); err != nil {
			return nil, err
		}
	}
	if err := in.InstallGuard(); err != nil {
		return nil, err
	}
	return in, nil
}

// InstallGuard writes the inlay's pre-push hook and points the inlay's own
// core.hooksPath at it, so a global core.hooksPath cannot bypass the guard.
func (i *Inlay) InstallGuard() error {
	hooksDir := filepath.Join(i.GitDir, "hooks")
	if err := i.Git().Run("config", "core.hooksPath", hooksDir); err != nil {
		return err
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(hooks.Shim("inlay-pre-push")), 0o755)
}

// GuardInstalled reports whether the push guard is in place and git will
// run it.
func (i *Inlay) GuardInstalled() (bool, error) {
	hooksDir := filepath.Join(i.GitDir, "hooks")
	got, _, err := i.Git().Config("core.hooksPath")
	if err != nil {
		return false, err
	}
	if filepath.Clean(got) != hooksDir {
		return false, nil
	}
	data, err := os.ReadFile(filepath.Join(hooksDir, "pre-push"))
	if err != nil {
		return false, nil
	}
	return strings.Contains(string(data), hooks.Begin), nil
}

// Git returns a runner pinned to the inlay, running at the host root.
func (i *Inlay) Git() gitx.Runner { return i.GitAt(i.Host.Root) }

// GitAt returns a runner pinned to the inlay, running in dir, so relative
// paths on a passthrough command resolve as the user expects.
func (i *Inlay) GitAt(dir string) gitx.Runner {
	return gitx.Runner{GitDir: i.GitDir, WorkTree: i.Host.Root, Dir: dir, Env: i.Host.Env}
}

// ManifestFile is the absolute path of the manifest in the working tree.
func (i *Inlay) ManifestFile() string {
	return filepath.Join(i.Host.Root, filepath.FromSlash(manifest.File(i.Name)))
}

// ReadManifest returns the manifest from the working tree, falling back to
// HEAD when the file is missing and to an empty manifest before any commit.
func (i *Inlay) ReadManifest() (*manifest.Manifest, error) {
	data, err := os.ReadFile(i.ManifestFile())
	if errors.Is(err, os.ErrNotExist) {
		return ManifestAt(i.Git(), i.Name, "HEAD")
	}
	if err != nil {
		return nil, err
	}
	return manifest.Parse(i.Name, data)
}

// ManifestAt reads the manifest committed at rev through run. A missing
// commit or file yields an empty manifest; any other failure is an error,
// so a broken inlay never silently loses its claims.
func ManifestAt(run gitx.Runner, name, rev string) (*manifest.Manifest, error) {
	ok, err := run.Succeeds("rev-parse", "--verify", "-q", rev+"^{commit}")
	if err != nil {
		return nil, err
	}
	if !ok {
		return manifest.New(name), nil
	}
	entries, err := run.OutputZ("ls-tree", "-z", "--name-only", rev, "--", manifest.File(name))
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return manifest.New(name), nil
	}
	out, err := run.Output("show", rev+":"+manifest.File(name))
	if err != nil {
		return nil, err
	}
	return manifest.Parse(name, []byte(out))
}

// WriteManifest writes m to the working tree and stages it in the inlay.
func (i *Inlay) WriteManifest(m *manifest.Manifest) error {
	path := i.ManifestFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, m.Format(), 0o644); err != nil {
		return err
	}
	// -f: a host .gitignore (".*", say) may cover the manifest directory.
	return i.Git().Run("add", "-f", "--", manifest.File(i.Name))
}

// WriteExclude regenerates the inlay's info/exclude from m.
func (i *Inlay) WriteExclude(m *manifest.Manifest) error {
	path := filepath.Join(i.GitDir, "info", "exclude")
	content := []byte(exclude.Inlay(m))
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, content) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

// Remotes returns the inlay's configured remotes.
func (i *Inlay) Remotes() ([]remote.Remote, error) { return remote.Configured(i.Git()) }

// RemoteNames returns the inlay's remote names.
func (i *Inlay) RemoteNames() ([]string, error) {
	out, err := i.Git().Output("remote")
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// Tracked returns every path the inlay tracks.
func (i *Inlay) Tracked() ([]string, error) { return i.Git().OutputZ("ls-files", "-z") }

// Entry is an inlay attached to any worktree of the host, with its manifest.
type Entry struct {
	Key      string
	Name     string
	GitDir   string
	Manifest *manifest.Manifest
	Inlay    *Inlay // set when attached to the current worktree
}

// All lists every inlay attached to any worktree of the host. Manifests of
// inlays in other worktrees are read from their HEAD.
func All(h *host.Host) ([]Entry, error) {
	var entries []Entry
	scan := func(key, dir string) error {
		names, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, d := range names {
			gitDir := filepath.Join(dir, d.Name())
			if !d.IsDir() || !isRepo(gitDir) || manifest.ValidName(d.Name()) != nil {
				continue
			}
			e := Entry{Key: key, Name: d.Name(), GitDir: gitDir}
			if key == h.Key {
				e.Inlay = &Inlay{Name: e.Name, Key: key, GitDir: gitDir, Host: h}
				e.Manifest, err = e.Inlay.ReadManifest()
			} else {
				e.Manifest, err = ManifestAt(gitx.Runner{GitDir: gitDir, Env: h.Env}, e.Name, "HEAD")
			}
			if err != nil {
				return fmt.Errorf("inlay %s in worktree %s: %w", e.Name, key, err)
			}
			entries = append(entries, e)
		}
		return nil
	}
	root := h.InlaysDir()
	if err := scan("main", filepath.Join(root, "main")); err != nil {
		return nil, err
	}
	wts, err := os.ReadDir(filepath.Join(root, "wt"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, w := range wts {
		if !w.IsDir() {
			continue
		}
		if err := scan("wt/"+w.Name(), filepath.Join(root, "wt", w.Name())); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(entries, func(a, b Entry) int {
		if c := strings.Compare(a.Key, b.Key); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return entries, nil
}
