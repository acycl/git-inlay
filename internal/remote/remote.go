// Package remote reads a repository's configured remotes and compares
// repository URLs, so an inlay can never be pushed to its host's remote.
package remote

import (
	"errors"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/acycl/git-inlay/internal/gitx"
)

// Remote is one configured remote URL after insteadOf rewrites. A remote
// with several URLs, or a separate pushurl, yields several entries.
type Remote struct {
	Name string
	URL  string // fetch URL, empty for a pushurl-only entry
	Push string // push URL
}

// rewrite is one url.<base>.insteadOf or pushInsteadOf rule.
type rewrite struct {
	base   string
	prefix string
}

// Configured returns the remotes of the repository run points at, with
// url.<base>.insteadOf and pushInsteadOf applied as git would.
func Configured(run gitx.Runner) ([]Remote, error) {
	entries, err := run.OutputZ("config", "-z", "--get-regexp",
		`^(remote\..*\.(url|pushurl)|url\..*\.(insteadof|pushinsteadof))$`)
	if err != nil {
		var gerr *gitx.Error
		if errors.As(err, &gerr) && gerr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}

	var order []string
	urls := map[string][]string{}
	pushurls := map[string][]string{}
	var insteadOf, pushInsteadOf []rewrite
	note := func(name string) {
		if !slices.Contains(order, name) {
			order = append(order, name)
		}
	}
	for _, e := range entries {
		key, value, _ := strings.Cut(e, "\n")
		switch {
		case strings.HasPrefix(key, "remote.") && strings.HasSuffix(key, ".url"):
			name := key[len("remote.") : len(key)-len(".url")]
			note(name)
			urls[name] = append(urls[name], value)
		case strings.HasPrefix(key, "remote.") && strings.HasSuffix(key, ".pushurl"):
			name := key[len("remote.") : len(key)-len(".pushurl")]
			note(name)
			pushurls[name] = append(pushurls[name], value)
		case strings.HasPrefix(key, "url.") && strings.HasSuffix(key, ".insteadof"):
			insteadOf = append(insteadOf, rewrite{base: key[len("url.") : len(key)-len(".insteadof")], prefix: value})
		case strings.HasPrefix(key, "url.") && strings.HasSuffix(key, ".pushinsteadof"):
			pushInsteadOf = append(pushInsteadOf, rewrite{base: key[len("url.") : len(key)-len(".pushinsteadof")], prefix: value})
		}
	}

	var out []Remote
	for _, name := range order {
		for _, u := range urls[name] {
			fetch, _ := apply(insteadOf, u)
			push := fetch
			if len(pushurls[name]) == 0 {
				if p, ok := apply(pushInsteadOf, u); ok {
					push = p
				}
			}
			out = append(out, Remote{Name: name, URL: Absolute(run.Dir, fetch), Push: Absolute(run.Dir, push)})
		}
		for _, p := range pushurls[name] {
			push, _ := apply(insteadOf, p)
			out = append(out, Remote{Name: name, Push: Absolute(run.Dir, push)})
		}
	}
	return out, nil
}

// apply rewrites u with the longest matching rule.
func apply(rules []rewrite, u string) (string, bool) {
	best, bestLen := -1, -1
	for i, r := range rules {
		if strings.HasPrefix(u, r.prefix) && len(r.prefix) > bestLen {
			best, bestLen = i, len(r.prefix)
		}
	}
	if best < 0 {
		return u, false
	}
	return rules[best].base + u[bestLen:], true
}

// Absolute resolves a relative local path against base, which is how git
// treats a relative remote URL, and leaves every other URL unchanged.
func Absolute(base, u string) string {
	if u == "" || base == "" || strings.Contains(u, "://") || scpLike(u) || filepath.IsAbs(u) {
		return u
	}
	return filepath.Join(base, u)
}

// Same reports whether two URLs name the same repository.
func Same(a, b string) bool { return Normalize(a) == Normalize(b) }

// Normalize reduces a git URL to a comparable "host/path" form: scp-like,
// ssh and https spellings of one repository compare equal, as do variants
// with or without a user, a trailing slash or a .git suffix. Case is folded
// because the common hosting services ignore it.
func Normalize(raw string) string {
	s := strings.TrimSpace(raw)
	host, p := "", s
	switch {
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err == nil {
			host = strings.ToLower(u.Hostname())
			if port := u.Port(); port != "" && !defaultPort(u.Scheme, port) {
				host += ":" + port
			}
			p = u.Path
			if u.Scheme == "file" {
				host, p = "", localPath(driveSlash(u.Path))
			}
		}
	case scpLike(s):
		h, rest, _ := strings.Cut(s, ":")
		if i := strings.LastIndex(h, "@"); i >= 0 {
			h = h[i+1:]
		}
		host, p = strings.ToLower(h), rest
	default:
		p = localPath(s)
	}
	p = strings.Trim(p, "/")
	p = strings.TrimSuffix(p, ".git")
	p = strings.TrimSuffix(p, "/")
	p = strings.ToLower(p)
	if host == "" {
		return p
	}
	return host + "/" + p
}

// driveSlash drops the slash a file URL puts before a Windows drive letter.
func driveSlash(p string) string {
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' && unicode.IsLetter(rune(p[1])) {
		return p[1:]
	}
	return p
}

// scpLike reports whether s has the form [user@]host:path.
func scpLike(s string) bool {
	i := strings.Index(s, ":")
	if i <= 0 {
		return false
	}
	head := s[:i]
	if strings.Contains(head, "/") {
		return false
	}
	if len(head) == 1 && unicode.IsLetter(rune(head[0])) {
		return false // a Windows drive letter
	}
	return true
}

// localPath canonicalizes a filesystem path when it exists.
func localPath(s string) string {
	if r, err := filepath.EvalSymlinks(s); err == nil {
		s = r
	}
	if a, err := filepath.Abs(s); err == nil {
		s = a
	}
	return filepath.ToSlash(s)
}

// defaultPort reports whether port is the scheme's default and can be dropped.
func defaultPort(scheme, port string) bool {
	switch scheme {
	case "ssh":
		return port == "22"
	case "https":
		return port == "443"
	case "http":
		return port == "80"
	case "git":
		return port == "9418"
	}
	return false
}
