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

// Same reports whether two URLs name the same repository. A port tells
// two repositories apart only when both URLs name one, for the same
// transport. A server reached over https and over ssh on a port of its own
// is still one server; and a URL without a port may reach any, since
// ~/.ssh/config can set it. Taking such a pair for different repositories
// would let an inlay push to its host's remote, which costs more than
// refusing two servers that share a name.
func Same(a, b string) bool {
	la, lb := locate(a), locate(b)
	if la.key != lb.key {
		return false
	}
	return la.transport != lb.transport || la.port == "" || lb.port == "" || la.port == lb.port
}

// Normalize reduces a git URL to a comparable "host/path" form: scp-like,
// ssh and https spellings of one repository compare equal, as do variants
// with or without a user, a port, a trailing slash or a .git suffix. Case
// is folded because the common hosting services ignore it.
func Normalize(raw string) string { return locate(raw).key }

// location is a git URL taken apart for comparison.
type location struct {
	key       string // "host/path", or the path alone for a local repository
	transport string // "ssh", "https", "http" or "git"; empty for a local path
	port      string // the port the URL names, or empty
}

// locate parses a git URL.
func locate(raw string) location {
	s := strings.TrimSpace(raw)
	var loc location
	host, p := "", s
	if h, rest, ok := scpSplit(s); ok {
		host, p = strings.ToLower(h), rest
		loc.transport = "ssh"
	} else if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil {
			host, p = strings.ToLower(u.Hostname()), u.Path
			switch scheme := strings.ToLower(u.Scheme); scheme {
			case "file":
				host, p = "", localPath(driveSlash(u.Path))
			case "git+ssh", "ssh+git":
				loc.transport = "ssh"
			default:
				loc.transport = scheme
			}
			loc.port = u.Port()
		}
	} else {
		p = localPath(s)
	}
	p = strings.Trim(p, "/")
	p = strings.TrimSuffix(p, ".git")
	p = strings.TrimSuffix(p, "/")
	p = strings.ToLower(p)
	loc.key = p
	if host != "" {
		loc.key = host + "/" + p
	}
	return loc
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
	_, _, ok := scpSplit(s)
	return ok
}

// scpSplit splits the scp-like form [user@]host:path into its host and
// path. The host may be an IPv6 address in brackets, which come off.
func scpSplit(s string) (host, path string, ok bool) {
	if strings.Contains(s, "://") {
		return "", "", false
	}
	i := strings.Index(s, ":")
	// Brackets hold an IPv6 address when they open the host, straight
	// away or after "user@"; anywhere else they belong to a name.
	if at := strings.Index(s, "@["); strings.HasPrefix(s, "[") || at >= 0 && !strings.ContainsAny(s[:at], ":/") {
		if j := strings.Index(s, "]:"); j >= 0 {
			i = j + 1
		}
	}
	if i <= 0 {
		return "", "", false
	}
	head := s[:i]
	if strings.Contains(head, "/") {
		return "", "", false
	}
	if len(head) == 1 && unicode.IsLetter(rune(head[0])) {
		return "", "", false // a Windows drive letter
	}
	if at := strings.LastIndex(head, "@"); at >= 0 {
		head = head[at+1:]
	}
	head = strings.TrimSuffix(strings.TrimPrefix(head, "["), "]")
	return head, s[i+1:], true
}

// localPath canonicalizes a filesystem path: absolute, with the symbolic
// links resolved in as much of it as exists. The repository itself may be
// spelled without the ".git" its directory carries, which git accepts, so
// the last component need not exist for the rest to be resolved.
func localPath(s string) string {
	if a, err := filepath.Abs(s); err == nil {
		s = a
	}
	rest := ""
	for cur := s; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.ToSlash(filepath.Join(r, rest))
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return filepath.ToSlash(s)
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}
