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

	"github.com/acycl/git-inlay/internal/atomicfile"
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

// Create initializes an empty inlay for the host's current worktree. A
// failure part way leaves nothing behind, since a git dir with a HEAD
// would otherwise look attached.
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
	in, err := Adopt(h, name, dir)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return in, nil
}

// Adopt configures an existing bare git dir as an inlay of the host's
// current worktree and installs its push guard.
func Adopt(h *host.Host, name, dir string) (*Inlay, error) {
	in := &Inlay{Name: name, Key: h.Key, GitDir: dir, Host: h}
	if err := in.Configure(); err != nil {
		return nil, err
	}
	return in, nil
}

// config lists the settings that bind the inlay to its host: the working
// tree it shares, the common dir its push guard answers from, and its own
// hooks directory, so a global core.hooksPath cannot bypass the guard.
func (i *Inlay) config() [][2]string {
	return [][2]string{
		{"core.bare", "false"},
		{"core.worktree", i.Host.Root},
		{"core.hookspath", i.hooksDir()},
		{"inlay.name", i.Name},
		{"inlay.host", i.Host.CommonDir},
	}
}

func (i *Inlay) hooksDir() string { return filepath.Join(i.GitDir, "hooks") }

// Configure writes the inlay's config and its pre-push guard, then the
// stamp that records them. Every command repeats it when Stamped says no,
// which is how a moved checkout gets its absolute paths back.
func (i *Inlay) Configure() error {
	run := i.Git()
	for _, kv := range i.config() {
		if err := run.Run("config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(i.hooksDir(), 0o755); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(filepath.Join(i.hooksDir(), "pre-push"), []byte(hooks.Shim("inlay-pre-push")), 0o755); err != nil {
		return err
	}
	return atomicfile.WriteFile(i.stampFile(), i.stamp(), 0o644)
}

// stampFile is where Configure records, inside the inlay git dir, the
// config it wrote.
func (i *Inlay) stampFile() string { return filepath.Join(i.GitDir, "git-inlay-config") }

// stamp returns the record Configure leaves for the config this host
// needs: one key=value line per setting.
func (i *Inlay) stamp() []byte {
	var b bytes.Buffer
	for _, kv := range i.config() {
		fmt.Fprintf(&b, "%s=%s\n", kv[0], kv[1])
	}
	return b.Bytes()
}

// Stamped reports, without running git, whether Configure has run for the
// host as it stands: its stamp matches the config this host needs and the
// push guard is the current shim. A moved checkout or an upgrade of
// git-inlay fails it; a change to the config that Configure did not make,
// by hand, through an include or by an older git-inlay, does not, which
// Configured, at the cost of a process, does see.
func (i *Inlay) Stamped() bool {
	got, err := os.ReadFile(i.stampFile())
	if err != nil || !bytes.Equal(got, i.stamp()) {
		return false
	}
	shim, err := os.ReadFile(filepath.Join(i.hooksDir(), "pre-push"))
	return err == nil && string(shim) == hooks.Shim("inlay-pre-push")
}

// Configured reports whether the inlay's config still names this host and
// its push guard is the current shim, so git will run it and an upgrade
// of git-inlay reaches it on the next command.
func (i *Inlay) Configured() (bool, error) {
	entries, err := i.Git().OutputZ("config", "-z", "--get-regexp", `^(core\.(bare|worktree|hookspath)|inlay\.(name|host))$`)
	if err != nil {
		var gerr *gitx.Error
		if errors.As(err, &gerr) && gerr.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	got := make(map[string]string, len(entries))
	for _, e := range entries {
		k, v, _ := strings.Cut(e, "\n")
		got[k] = v
	}
	for _, kv := range i.config() {
		// Git may spell a path with forward slashes on Windows.
		if filepath.Clean(got[kv[0]]) != filepath.Clean(kv[1]) {
			return false, nil
		}
	}
	data, err := os.ReadFile(filepath.Join(i.hooksDir(), "pre-push"))
	if err != nil {
		return false, nil
	}
	return string(data) == hooks.Shim("inlay-pre-push"), nil
}

// Git returns a runner pinned to the inlay, running at the host root.
func (i *Inlay) Git() gitx.Runner { return i.GitAt(i.Host.Root) }

// GitAt returns a runner pinned to the inlay, running in dir, so relative
// paths on a passthrough command resolve as the user expects.
func (i *Inlay) GitAt(dir string) gitx.Runner {
	if i.Host.Root == "" {
		return Elsewhere(i.Host, i.GitDir)
	}
	return gitx.Runner{GitDir: i.GitDir, WorkTree: i.Host.Root, Dir: dir, Env: i.Host.Env}
}

// Elsewhere returns a runner for an inlay attached to another worktree, or
// one whose worktree is gone. The work tree is pinned to the common dir,
// which always exists, so git never resolves the inlay's core.worktree: a
// missing parent directory there makes git refuse to start at all. Nothing
// run through this runner may touch a tree.
func Elsewhere(h *host.Host, gitDir string) gitx.Runner {
	return gitx.Runner{GitDir: gitDir, WorkTree: h.CommonDir, Dir: h.CommonDir, Env: h.Env}
}

// ManifestFile is the absolute path of the manifest in the working tree.
func (i *Inlay) ManifestFile() string {
	return filepath.Join(i.Host.Root, filepath.FromSlash(manifest.File(i.Name)))
}

// ReadManifest returns the manifest from the working tree, falling back to
// HEAD when the file is missing and to an empty manifest before any commit.
func (i *Inlay) ReadManifest() (*manifest.Manifest, error) {
	return readManifest(i.Host.Root, i.Git(), i.Name, false)
}

// readManifest reads the manifest of inlay name from the working tree at
// root, falling back to the commit at HEAD through run when root is empty
// or the file is missing. With lenient set, as for an inlay of another
// worktree, a file that cannot be read or parsed falls back to HEAD too:
// that worktree's own commands report it, and it must not stop this one's.
func readManifest(root string, run gitx.Runner, name string, lenient bool) (*manifest.Manifest, error) {
	if root == "" {
		return ManifestAt(run, name, "HEAD")
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(manifest.File(name))))
	switch {
	case err == nil:
		m, err := manifest.Parse(name, data)
		if err == nil || !lenient {
			return m, err
		}
	case !errors.Is(err, os.ErrNotExist) && !lenient:
		return nil, err
	}
	return ManifestAt(run, name, "HEAD")
}

// ManifestAt reads the manifest committed at rev through run. A missing
// commit or file yields an empty manifest; any other failure is an error,
// so a broken inlay never silently loses its claims.
func ManifestAt(run gitx.Runner, name, rev string) (*manifest.Manifest, error) {
	// One cat-file --batch call both finds and reads the file: an unborn
	// HEAD, an unknown rev, or an absent path all answer "missing" rather
	// than failing, so no probing with rev-parse and ls-tree is needed.
	obj := rev + ":" + manifest.File(name)
	out, err := run.OutputWithInput([]byte(obj+"\n"), "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	header, body, _ := strings.Cut(out, "\n")
	if strings.HasSuffix(header, " missing") {
		return manifest.New(name), nil
	}
	if fields := strings.Fields(header); len(fields) == 3 && fields[1] == "blob" {
		return manifest.Parse(name, []byte(body))
	}
	return nil, fmt.Errorf("%s: cat-file answered %q, not a blob", obj, header)
}

// WriteManifest writes m to the working tree and stages it in the inlay.
func (i *Inlay) WriteManifest(m *manifest.Manifest) error {
	path := i.ManifestFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(path, m.Format(), 0o644); err != nil {
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
	return atomicfile.WriteFile(path, content, 0o644)
}

// Remotes returns the inlay's configured remotes.
func (i *Inlay) Remotes() ([]remote.Remote, error) { return remote.Configured(i.Git()) }

// RemoteNames returns the inlay's remote names.
func (i *Inlay) RemoteNames() ([]string, error) { return i.Git().Lines("remote") }

// Tracked returns every path the inlay tracks.
func (i *Inlay) Tracked() ([]string, error) { return i.Git().OutputZ("ls-files", "-z") }

// Entry is an inlay attached to any worktree of the host, with its manifest.
type Entry struct {
	Key      string
	Name     string
	GitDir   string
	Manifest *manifest.Manifest
	Inlay    *Inlay // set when attached to the current worktree
	Gone     bool   // the linked worktree it is attached to no longer exists
}

// All lists every inlay attached to any worktree of the host. Each
// manifest is read from its worktree's tree, as the current worktree's
// is, so a claim staged but not committed in another worktree still
// counts; an inlay whose worktree is gone, or cannot be found, answers
// from its HEAD.
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
		root, gone := h.Worktree(key)
		for _, d := range names {
			gitDir := filepath.Join(dir, d.Name())
			if !d.IsDir() || !isRepo(gitDir) || manifest.ValidName(d.Name()) != nil {
				continue
			}
			e := Entry{Key: key, Name: d.Name(), GitDir: gitDir, Gone: gone}
			if key == h.Key {
				e.Inlay = &Inlay{Name: e.Name, Key: key, GitDir: gitDir, Host: h}
				e.Manifest, err = e.Inlay.ReadManifest()
			} else {
				e.Manifest, err = readManifest(root, Elsewhere(h, gitDir), e.Name, true)
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
