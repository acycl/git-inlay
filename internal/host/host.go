// Package host discovers the repository whose working tree holds inlay
// files and answers questions about what it tracks.
package host

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/acycl/git-inlay/internal/gitx"
)

// Host is one worktree of the host repository.
type Host struct {
	Root      string   // absolute root of this worktree, symlinks resolved
	GitDir    string   // this worktree's git dir
	CommonDir string   // the git dir shared by every worktree
	Key       string   // "main" or "wt/<id>", from PLAN.md section 3
	Env       []string // environment for git; nil means the process's
	IndexFile string   // GIT_INDEX_FILE to honour, set by the pre-commit hook

	ignoreCase *bool // cached core.ignoreCase
}

// Discover finds the host worktree that contains dir.
func Discover(dir string, env []string) (*Host, error) {
	run := gitx.Runner{Dir: dir, Env: env}
	out, err := run.Output("rev-parse", "--path-format=absolute",
		"--show-toplevel", "--git-dir", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("not inside a git working tree: %w", err)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		return nil, fmt.Errorf("unexpected rev-parse output %q", out)
	}
	h := &Host{Root: lines[0], GitDir: lines[1], CommonDir: lines[2], Env: env}
	// Compare in slash form: git prints forward slashes on every platform,
	// while filepath.Join would produce backslashes on Windows.
	gitDir, common := filepath.ToSlash(h.GitDir), filepath.ToSlash(h.CommonDir)
	switch {
	case gitDir == common:
		h.Key = "main"
	case strings.HasPrefix(gitDir, common+"/worktrees/"):
		h.Key = "wt/" + path.Base(gitDir)
	default:
		return nil, fmt.Errorf("cannot place git dir %s relative to common dir %s", h.GitDir, h.CommonDir)
	}
	return h, nil
}

// Detached returns a host for an inlay whose worktree no longer exists. It
// answers from the common dir alone: config and remotes work, anything
// that needs the tree does not, and Root is empty.
func Detached(commonDir string, env []string) *Host {
	return &Host{GitDir: commonDir, CommonDir: commonDir, Env: env}
}

// Worktree returns the root of the host worktree behind key, "main" or
// "wt/<id>", and whether it is gone, from the files git keeps under the
// common dir, so no process is spent. A linked worktree is gone by the
// rules git worktree prune applies: its administrative directory, its
// gitdir file or the .git file that names is missing, and it is not
// locked, since git keeps a locked worktree through an unmounted volume.
// Root is empty when it cannot be told: a gone worktree, a main worktree
// whose git dir is not the .git directory at its root, or a path that
// cannot be read. Only an absence counts as gone; any other failure, a
// permission refused or a volume not ready, leaves the worktree standing,
// since prune deletes what is gone and every command reads every worktree.
func (h *Host) Worktree(key string) (root string, gone bool) {
	if key == h.Key && h.Root != "" {
		return h.Root, false
	}
	id, ok := strings.CutPrefix(key, "wt/")
	if !ok {
		if filepath.Base(h.CommonDir) == ".git" {
			return filepath.Dir(h.CommonDir), false
		}
		return "", false
	}
	admin := filepath.Join(h.CommonDir, "worktrees", id)
	if _, err := os.Stat(admin); err != nil {
		return "", absent(err)
	}
	_, err := os.Stat(filepath.Join(admin, "locked"))
	locked := err == nil
	data, err := os.ReadFile(filepath.Join(admin, "gitdir"))
	if err != nil {
		return "", absent(err) && !locked
	}
	dotGit := strings.TrimSpace(string(data))
	if dotGit == "" {
		return "", !locked
	}
	// Git 2.48 writes the path relative to the administrative directory
	// under worktree.useRelativePaths.
	if !filepath.IsAbs(dotGit) {
		dotGit = filepath.Join(admin, dotGit)
	}
	if _, err := os.Stat(dotGit); err != nil {
		return "", absent(err) && !locked
	}
	return filepath.Dir(dotGit), false
}

// absent reports whether err says a path does not exist, counting a file
// where a directory should be, as git does.
func absent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// Git returns a runner for the host, running at its root, or pinned to the
// common dir when the host is detached.
func (h *Host) Git() gitx.Runner {
	r := gitx.Runner{Dir: h.Root, Env: h.Env}
	if h.Root == "" {
		r = gitx.Runner{GitDir: h.CommonDir, Dir: h.CommonDir, Env: h.Env}
	}
	if h.IndexFile != "" {
		r.ExtraEnv = []string{"GIT_INDEX_FILE=" + h.IndexFile}
	}
	return r
}

// InlaysDir is where every inlay git dir lives.
func (h *Host) InlaysDir() string { return filepath.Join(h.CommonDir, "inlays") }

// ExcludeFile is the shared exclude file that carries the managed block.
func (h *Host) ExcludeFile() string { return filepath.Join(h.CommonDir, "info", "exclude") }

// HooksPath returns core.hooksPath when a hook manager has set it.
func (h *Host) HooksPath() (string, error) {
	v, _, err := h.Git().Config("core.hooksPath")
	return v, err
}

// HooksDir returns the directory git runs hooks from.
func (h *Host) HooksDir() (string, error) {
	return h.Git().Output("rev-parse", "--path-format=absolute", "--git-path", "hooks")
}

// SparseCheckout reports whether the host uses sparse checkout, which
// git-inlay does not support.
func (h *Host) SparseCheckout() (bool, error) {
	v, _, err := h.Git().Config("core.sparseCheckout")
	return strings.EqualFold(v, "true"), err
}

// Tracked returns the host-tracked paths matching the literal pathspecs.
// On a case-insensitive host they match case-insensitively too.
func (h *Host) Tracked(pathspecs ...string) ([]string, error) {
	if len(pathspecs) == 0 {
		return nil, nil
	}
	icase, err := h.IgnoreCase()
	if err != nil {
		return nil, err
	}
	if !icase {
		return h.Git().OutputZ(append([]string{"ls-files", "-z", "--"}, pathspecs...)...)
	}
	// Git's icase magic folds ASCII letters only, so a pathspec with a
	// letter beyond ASCII is answered by folding here instead. Splitting
	// keeps each pathspec's answer the same whatever is asked beside it.
	var plain, wide []string
	for _, p := range pathspecs {
		if ascii(p) {
			plain = append(plain, strings.Replace(p, ":(literal)", ":(literal,icase)", 1))
		} else {
			wide = append(wide, p)
		}
	}
	var out []string
	if len(plain) > 0 {
		if out, err = h.Git().OutputZ(append([]string{"ls-files", "-z", "--"}, plain...)...); err != nil {
			return nil, err
		}
	}
	if len(wide) > 0 {
		more, err := h.trackedFolded(wide)
		if err != nil {
			return nil, err
		}
		out = append(out, more...)
		slices.Sort(out)
		out = slices.Compact(out)
	}
	return out, nil
}

// trackedFolded answers Tracked by folding case itself. Git's icase magic
// folds ASCII letters only, while the filesystem folds every letter, so
// a host-tracked CAFÉ/x would slip past a pathspec for Café.
func (h *Host) trackedFolded(pathspecs []string) ([]string, error) {
	keys := make([]string, len(pathspecs))
	for i, s := range pathspecs {
		p, ok := strings.CutPrefix(s, ":(literal)")
		if !ok {
			return nil, fmt.Errorf("pathspec %q is not literal", s)
		}
		keys[i] = Fold(p)
	}
	all, err := h.Git().OutputZ("ls-files", "-z")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range all {
		k := Fold(p)
		for _, s := range keys {
			if k == s || strings.HasPrefix(k, s+"/") {
				out = append(out, p)
				break
			}
		}
	}
	return out, nil
}

// ascii reports whether s is plain ASCII.
func ascii(s string) bool {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// Fold maps p to a key shared by every case variant of p, for matching
// paths on a host with core.ignoreCase. It folds every letter, round trip
// through upper case included, so final sigma, long s and dotless i meet
// their plain forms: a superset of what APFS and NTFS fold, so that a
// guard errs towards refusing. A byte that is not valid UTF-8 passes
// through as itself, so two different Latin-1 names keep different keys.
func Fold(p string) string {
	var b strings.Builder
	b.Grow(len(p))
	for i := 0; i < len(p); {
		r, size := utf8.DecodeRuneInString(p[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteByte(p[i])
		} else {
			b.WriteRune(unicode.ToLower(unicode.ToUpper(r)))
		}
		i += size
	}
	return b.String()
}

// IgnoreCase reports core.ignoreCase, which git sets at init on a
// case-insensitive filesystem.
func (h *Host) IgnoreCase() (bool, error) {
	if h.ignoreCase == nil {
		b, err := h.Git().Bool("core.ignoreCase")
		if err != nil {
			return false, err
		}
		h.ignoreCase = &b
	}
	return *h.ignoreCase, nil
}

// Staged returns the paths staged as added, copied, modified or renamed.
func (h *Host) Staged() ([]string, error) {
	return h.Git().OutputZ("diff", "--cached", "--name-only", "-z", "--diff-filter=ACMR")
}

// RemoteNames returns the configured remote names.
func (h *Host) RemoteNames() ([]string, error) { return h.Git().Lines("remote") }

// Submodules reports which of the given root-relative paths are submodule
// entries in HEAD.
func (h *Host) Submodules(paths ...string) (map[string]bool, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	entries, err := h.Git().OutputZ(append([]string{"ls-tree", "-z", "HEAD", "--"}, paths...)...)
	if err != nil {
		// Before the first commit there is no tree and so no submodule;
		// ask only now, to spare the common case a process.
		if ok, verr := h.Git().Succeeds("rev-parse", "--verify", "-q", "HEAD"); verr == nil && !ok {
			return nil, nil
		}
		return nil, err
	}
	subs := map[string]bool{}
	for _, e := range entries {
		meta, path, ok := strings.Cut(e, "\t")
		if ok && strings.HasPrefix(meta, "160000 ") {
			subs[path] = true
		}
	}
	return subs, nil
}

// Rel converts a command-line path, relative to cwd, into a root-relative
// slash path. Symlinks in the existing part of the parent path are resolved
// so a cwd reached through a symlink still maps into the real root; the
// last component is kept as named, since git tracks a symlink as itself.
func (h *Host) Rel(cwd, p string) (string, error) {
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, abs)
	}
	abs = filepath.Clean(abs)
	parent, err := resolveExisting(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(h.Root, filepath.Join(parent, filepath.Base(abs)))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the repository", p)
	}
	return filepath.ToSlash(rel), nil
}

// resolveExisting evaluates symlinks in the longest existing prefix of abs
// and re-joins the remainder.
func resolveExisting(abs string) (string, error) {
	rest := ""
	cur := abs
	for {
		r, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(r, rest), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}
