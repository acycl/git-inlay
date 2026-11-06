package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/acycl/git-inlay/internal/gitx"
	"github.com/acycl/git-inlay/internal/inlay"
	"github.com/acycl/git-inlay/internal/manifest"
	"github.com/acycl/git-inlay/internal/remote"
)

// canonicalCase rewrites rel with the on-disk spelling of every existing
// component, so a claim typed in the wrong case on a case-insensitive
// filesystem matches what git tracks and what the exclude files list.
func canonicalCase(root, rel string) string {
	parts := strings.Split(rel, "/")
	cur := root
	for i, part := range parts {
		entries, err := os.ReadDir(cur)
		if err != nil {
			break
		}
		found := ""
		for _, e := range entries {
			if e.Name() == part {
				found = part
				break
			}
			if strings.EqualFold(e.Name(), part) {
				found = e.Name()
			}
		}
		if found == "" {
			break
		}
		parts[i] = found
		cur = filepath.Join(cur, found)
	}
	return strings.Join(parts, "/")
}

// summarize renders a path list with at most five entries shown.
func summarize(paths []string) string {
	shown := paths
	if len(shown) > 5 {
		shown = shown[:5]
	}
	return strings.Join(shown, ", ") + more(len(paths)-len(shown))
}

// homeDir returns the user's home directory from the command environment.
func (c *cli) homeDir() string {
	if h := c.env("HOME"); h != "" {
		return h
	}
	return c.env("USERPROFILE")
}

// resolveURL makes a candidate remote URL comparable with the host's
// configured remotes: a relative local path is resolved against the root,
// as git does, and git applies its own insteadOf rewrites without touching
// the network.
func (s *session) resolveURL(u string) string {
	u = remote.Absolute(s.host.Root, u)
	if out, err := s.host.Git().Output("ls-remote", "--get-url", u); err == nil && out != "" {
		return out
	}
	return u
}

// globalExcludes returns the user's global excludes file, if there is one.
// Like git, a configured file that does not exist is silently skipped.
func (s *session) globalExcludes() string {
	if v, ok, _ := s.host.Git().Config("core.excludesFile"); ok && v != "" {
		if rest, ok := strings.CutPrefix(v, "~/"); ok {
			v = filepath.Join(s.c.homeDir(), rest)
		}
		if _, err := os.Stat(v); err == nil {
			return v
		}
		return ""
	}
	base := s.c.env("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(s.c.homeDir(), ".config")
	}
	p := filepath.Join(base, "git", "ignore")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// stageable lists the files under the claims that the inlay should add:
// untracked files not excluded by the inlay's own rules or by the user's
// global excludes. The host's .gitignore files are deliberately left out,
// because they outrank the inlay's exclude file and would otherwise hide
// claimed paths the host happens to ignore.
func (s *session) stageable(in *inlay.Inlay, claims []manifest.Claim) ([]string, error) {
	args := []string{"ls-files", "-z", "--others"}
	if g := s.globalExcludes(); g != "" {
		args = append(args, "--exclude-from="+g)
	}
	args = append(args, "--exclude-from="+filepath.Join(in.GitDir, "info", "exclude"), "--")
	n := len(args)
	for _, cl := range claims {
		if _, err := os.Lstat(filepath.Join(s.host.Root, filepath.FromSlash(cl.Path))); err == nil {
			args = append(args, cl.Pathspec())
		}
	}
	if len(args) == n {
		return nil, nil
	}
	return in.Git().OutputZ(args...)
}

// ignoreMatch reports the pattern that decides whether run's repository
// ignores path: its source file and the pattern text, which starts with "!"
// for a negation. ok is false when no pattern matches.
func ignoreMatch(run gitx.Runner, path string) (src, pattern string, ok bool) {
	// -z needs --stdin; the fields come back as source, line, pattern, path.
	fields, err := run.OutputZWithInput([]byte(path+"\x00"),
		"check-ignore", "-v", "-z", "--no-index", "--stdin")
	if err != nil || len(fields) < 4 {
		return "", "", false // exit 1: no pattern matched
	}
	return fields[0], fields[2], true
}

// unexcluded returns the claims the host does not ignore even with the
// managed block in place, which a .gitignore negation causes.
func (s *session) unexcluded() ([]manifest.Claim, error) {
	claims := s.index.Claims()
	if len(claims) == 0 {
		return nil, nil
	}
	var input bytes.Buffer
	for _, cl := range claims {
		input.WriteString(cl.Path)
		input.WriteByte(0)
	}
	ignored, err := s.host.Git().OutputZWithInput(input.Bytes(), "check-ignore", "-z", "--no-index", "--stdin")
	if err != nil {
		// Exit 1 means no path is ignored, which the loop below reports.
		var gerr *gitx.Error
		if !errors.As(err, &gerr) || gerr.ExitCode() != 1 {
			return nil, err
		}
	}
	set := make(map[string]bool, len(ignored))
	for _, p := range ignored {
		set[p] = true
	}
	var out []manifest.Claim
	for _, cl := range claims {
		if !set[cl.Path] {
			out = append(out, cl)
		}
	}
	return out, nil
}

// warnIgnored tells the user when a host .gitignore file ignores a claim.
// Tracked files are unaffected, but new files under such a claim never show
// as untracked in the inlay and must be added with -f.
func (s *session) warnIgnored(in *inlay.Inlay, claims []manifest.Claim) {
	for _, cl := range claims {
		src, pattern, ok := ignoreMatch(in.Git(), cl.Path)
		if !ok || strings.HasPrefix(pattern, "!") || strings.HasSuffix(filepath.ToSlash(src), "info/exclude") {
			continue
		}
		s.c.warnf("%s is ignored by %s (pattern %s); inlay %s tracks it anyway, but new files under it will not show as untracked and must be added with: git inlay %s add -f <path>",
			cl, src, pattern, in.Name, in.Name)
	}
}

// warnHistory tells the user when a claimed path exists anywhere in the
// host's history, since checking out such a commit in the host would
// overwrite the inlay's files with the host's copy.
func (s *session) warnHistory(claims []manifest.Claim) {
	// On a case-insensitive host a commit with the path in any case
	// would overwrite the inlay's files.
	icase, err := s.host.IgnoreCase()
	if err != nil {
		return
	}
	spec := func(cl manifest.Claim) string {
		if icase {
			return ":(literal,icase)" + cl.Path
		}
		return cl.Pathspec()
	}
	if len(claims) > 1 {
		// Each query walks the whole history when the path was never
		// there, so settle the usual case of no hits with one walk.
		args := []string{"rev-list", "--all", "-n", "1", "--"}
		for _, cl := range claims {
			args = append(args, spec(cl))
		}
		if out, err := s.host.Git().Output(args...); err != nil || out == "" {
			return
		}
	}
	for _, cl := range claims {
		out, err := s.host.Git().Output("rev-list", "--all", "-n", "1", "--", spec(cl))
		if err == nil && len(out) >= 7 {
			s.c.warnf("%s exists in the host's history (commit %s); checking out that commit in the host would overwrite the inlay's files", cl, out[:7])
		}
	}
}
