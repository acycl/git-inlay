// Package guard checks the invariants from PLAN.md section 6 and powers the
// pre-commit and pre-push hooks.
package guard

import (
	"bufio"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/acycl/git-inlay/internal/gitx"
	"github.com/acycl/git-inlay/internal/host"
	"github.com/acycl/git-inlay/internal/inlay"
	"github.com/acycl/git-inlay/internal/manifest"
	"github.com/acycl/git-inlay/internal/remote"
)

// Owner names the inlay and claim that own a path.
type Owner struct {
	Inlay string
	Claim manifest.Claim
}

// Index resolves paths to owners across every inlay of the host.
type Index struct {
	owners []Owner
	keys   []manifest.Claim // owners' claims as matched, lower-cased when folding
	fold   bool
}

// NewIndex builds the union of claims across entries, including each
// manifest's implicit claim on its own file. With fold set, which a host
// with core.ignoreCase needs, paths match claims whatever their case, as
// git's own ignore and pathspec matching does there.
func NewIndex(entries []inlay.Entry, fold bool) *Index {
	x := &Index{owners: owners(entries), fold: fold}
	x.keys = make([]manifest.Claim, len(x.owners))
	for i, o := range x.owners {
		x.keys[i] = x.claimKey(o.Claim)
	}
	return x
}

// owners returns every claim of entries with its inlay, including each
// manifest's implicit claim on its own file, once each, sorted by path.
func owners(entries []inlay.Entry) []Owner {
	var out []Owner
	seen := map[Owner]bool{}
	for _, e := range entries {
		for _, c := range e.Manifest.All() {
			o := Owner{Inlay: e.Name, Claim: c}
			if !seen[o] {
				seen[o] = true
				out = append(out, o)
			}
		}
	}
	slices.SortFunc(out, func(a, b Owner) int { return strings.Compare(a.Claim.Path, b.Claim.Path) })
	return out
}

// Union returns the claims an Index over entries holds, which do not
// depend on folding, for writing the managed block without building one.
func Union(entries []inlay.Entry) []manifest.Claim {
	return (&Index{owners: owners(entries)}).Claims()
}

// key returns p as the index matches it.
func (x *Index) key(p string) string {
	if x.fold {
		return host.Fold(p)
	}
	return p
}

// claimKey returns c as the index matches it.
func (x *Index) claimKey(c manifest.Claim) manifest.Claim {
	return manifest.Claim{Path: x.key(c.Path), Dir: c.Dir}
}

// Owns reports whether c owns p as the index matches paths.
func (x *Index) Owns(c manifest.Claim, p string) bool { return x.claimKey(c).Owns(x.key(p)) }

// Owner returns the owner of p, if any.
func (x *Index) Owner(p string) (Owner, bool) {
	k := x.key(p)
	for i, c := range x.keys {
		if c.Owns(k) {
			return x.owners[i], true
		}
	}
	return Owner{}, false
}

// Claims returns every claim in the index.
func (x *Index) Claims() []manifest.Claim {
	out := make([]manifest.Claim, 0, len(x.owners))
	for _, o := range x.owners {
		out = append(out, o.Claim)
	}
	return out
}

// Violation is one failed invariant, with the command that resolves it.
type Violation struct {
	Path   string
	Inlay  string
	Reason string
	Fix    string
}

func (v Violation) String() string {
	s := v.Path + ": " + v.Reason
	if v.Fix != "" {
		s += "\n    fix: " + v.Fix
	}
	return s
}

// Overlaps finds claims of different inlays that own each other (I1), as
// the index matches paths.
func (x *Index) Overlaps(entries []inlay.Entry) []Violation {
	var out []Violation
	for i, a := range entries {
		for _, b := range entries[i+1:] {
			if a.Name == b.Name {
				continue
			}
			for _, ca := range a.Manifest.Claims {
				for _, cb := range b.Manifest.Claims {
					if x.claimKey(ca).Overlaps(x.claimKey(cb)) {
						out = append(out, Violation{
							Path:   ca.String(),
							Inlay:  a.Name,
							Reason: fmt.Sprintf("overlaps %s claimed by inlay %s", cb, b.Name),
							Fix:    fmt.Sprintf("git inlay unclaim %s %s   or   git inlay unclaim %s %s", a.Name, ca, b.Name, cb),
						})
					}
				}
			}
		}
	}
	return out
}

// HostTracked finds claimed paths the host tracks (I1).
func HostTracked(h *host.Host, x *Index) ([]Violation, error) {
	var specs []string
	for _, c := range x.Claims() {
		specs = append(specs, c.Pathspec())
	}
	tracked, err := h.Tracked(specs...)
	if err != nil {
		return nil, err
	}
	var out []Violation
	for _, p := range tracked {
		o, ok := x.Owner(p)
		if !ok {
			continue
		}
		out = append(out, Violation{
			Path:   p,
			Inlay:  o.Inlay,
			Reason: fmt.Sprintf("tracked by the host but claimed by inlay %s", o.Inlay),
			Fix:    fmt.Sprintf("git rm --cached -- %q && git commit   or   git inlay unclaim %s %s", p, o.Inlay, o.Claim),
		})
	}
	return out, nil
}

// Staged finds staged host paths that belong to an inlay (pre-commit). The
// fix for a claimed path also names the inlay-side commit, since whoever
// staged it, a person or a coding agent, most likely meant to record it
// somewhere and would otherwise only learn where it may not go; for an
// inlay not among those attached here, it names the attach that must come
// first, which refuses to overwrite the file, so the file is moved aside
// for it. Paths in the fix carry the top and literal pathspec magic, which
// anchors them at the root, so the commands work from any directory of the
// worktree, and keeps a leading "!" or a glob character from widening them.
func Staged(h *host.Host, x *Index, attached []string) ([]Violation, error) {
	staged, err := h.Staged()
	if err != nil {
		return nil, err
	}
	var out []Violation
	for _, p := range staged {
		v, ok := claimed(x, p)
		if !ok {
			continue
		}
		spec := fmt.Sprintf("%q", ":(top,literal)"+p)
		v.Fix = "git restore --staged -- " + spec
		switch {
		case v.Inlay == "":
		case slices.Contains(attached, v.Inlay):
			v.Fix += fmt.Sprintf("   then commit it to its inlay:   git inlay %s add -- %s && git inlay %s commit -m \"...\"",
				v.Inlay, spec, v.Inlay)
		default:
			v.Fix += fmt.Sprintf("   then, to commit it to its inlay, move the file aside, run git inlay attach %s, and copy your changes back",
				v.Inlay)
		}
		out = append(out, v)
	}
	return out, nil
}

// claimed reports whether p belongs to an inlay or to the reserved
// manifest directory.
func claimed(x *Index, p string) (Violation, bool) {
	if o, ok := x.Owner(p); ok {
		return Violation{Path: p, Inlay: o.Inlay, Reason: "belongs to inlay " + o.Inlay}, true
	}
	if strings.HasPrefix(x.key(p), manifest.Dir+"/") {
		return Violation{Path: p, Reason: "is inside " + manifest.Dir + ", which is reserved for inlays"}, true
	}
	return Violation{}, false
}

// Containment finds paths the inlay tracks outside its claims (I3).
func Containment(in *inlay.Inlay, m *manifest.Manifest) ([]Violation, error) {
	tracked, err := in.Tracked()
	if err != nil {
		return nil, err
	}
	var out []Violation
	for _, p := range tracked {
		if _, ok := m.Owner(p); !ok {
			out = append(out, Violation{
				Path:   p,
				Inlay:  in.Name,
				Reason: fmt.Sprintf("tracked by inlay %s but not under any of its claims", in.Name),
				Fix:    fmt.Sprintf("git inlay claim %s %q   or   git inlay %s rm --cached -- %q", in.Name, p, in.Name, p),
			})
		}
	}
	return out, nil
}

// RemoteConflicts finds inlay remotes that resolve to a host remote (I4).
func RemoteConflicts(hostRemotes []remote.Remote, inlayName string, inlayRemotes []remote.Remote) []Violation {
	var out []Violation
	for _, r := range inlayRemotes {
		for _, u := range []string{r.URL, r.Push} {
			if u == "" {
				continue
			}
			if hr, ok := conflict(hostRemotes, u); ok {
				out = append(out, Violation{
					Path:   u,
					Inlay:  inlayName,
					Reason: fmt.Sprintf("inlay remote %s is the host's remote %s", r.Name, hr.Name),
					Fix:    fmt.Sprintf("git inlay %s remote set-url %s <private url>", inlayName, r.Name),
				})
			}
		}
	}
	return out
}

// CheckRemoteURL refuses a URL that names a host remote (I4).
func CheckRemoteURL(hostRemotes []remote.Remote, candidate string) error {
	if hr, ok := conflict(hostRemotes, candidate); ok {
		return fmt.Errorf("%s is the host's remote %s; an inlay must never push to the host's repository", candidate, hr.Name)
	}
	return nil
}

// conflict finds the host remote that u resolves to, if any.
func conflict(hostRemotes []remote.Remote, u string) (remote.Remote, bool) {
	for _, hr := range hostRemotes {
		for _, hu := range []string{hr.URL, hr.Push} {
			if hu != "" && remote.Same(hu, u) {
				return hr, true
			}
		}
	}
	return remote.Remote{}, false
}

// RefUpdate is one line of a pre-push hook's stdin.
type RefUpdate struct {
	LocalRef, LocalOID, RemoteRef, RemoteOID string
}

// IsZero reports whether oid is the all-zero id git uses for "absent".
func IsZero(oid string) bool { return oid != "" && strings.Trim(oid, "0") == "" }

// ParseRefUpdates reads the lines git writes to a pre-push hook's stdin.
func ParseRefUpdates(r io.Reader) ([]RefUpdate, error) {
	var out []RefUpdate
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		if len(f) != 4 {
			return nil, fmt.Errorf("malformed pre-push line %q", sc.Text())
		}
		out = append(out, RefUpdate{f[0], f[1], f[2], f[3]})
	}
	return out, sc.Err()
}

// OutgoingPaths lists every path added, changed or renamed by the commits
// the updates would publish. Deletions are skipped: a commit that only
// removes a claimed path publishes nothing new and is the documented fix.
// When the remote side of an update is unknown locally, commits already on
// the named remote are excluded; for an unnamed remote the whole history
// behind the update is scanned.
func OutgoingPaths(run gitx.Runner, updates []RefUpdate, remoteName string, named bool) ([]string, error) {
	seen := map[string]bool{}
	for _, u := range updates {
		if IsZero(u.LocalOID) {
			continue
		}
		commits, err := revList(run, u, remoteName, named)
		if err != nil {
			return nil, err
		}
		if commits == "" {
			continue
		}
		paths, err := run.OutputZWithInput([]byte(commits+"\n"),
			"diff-tree", "-r", "-m", "--root", "--no-commit-id", "--name-only", "--diff-filter=ACMRT", "-z", "--stdin")
		if err != nil {
			return nil, err
		}
		for _, p := range paths {
			if p != "" {
				seen[p] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(seen)), nil
}

// revList lists the commits an update would publish.
func revList(run gitx.Runner, u RefUpdate, remoteName string, named bool) (string, error) {
	if !IsZero(u.RemoteOID) {
		if out, err := run.Output("rev-list", u.LocalOID, "--not", u.RemoteOID); err == nil {
			return out, nil
		}
		// The remote's object may be unknown locally; fall back to its refs.
	}
	args := []string{"rev-list", u.LocalOID}
	if named {
		args = append(args, "--not", "--remotes="+remoteName)
	}
	return run.Output(args...)
}

// Outgoing finds claimed paths in the commits a host push would publish
// (pre-push).
func Outgoing(h *host.Host, x *Index, updates []RefUpdate, remoteName string) ([]Violation, error) {
	names, err := h.RemoteNames()
	if err != nil {
		return nil, err
	}
	paths, err := OutgoingPaths(h.Git(), updates, remoteName, slices.Contains(names, remoteName))
	if err != nil {
		return nil, err
	}
	var out []Violation
	for _, p := range paths {
		if v, ok := claimed(x, p); ok {
			v.Fix = "remove the path from the unpushed commits, for example with git rebase -i, or unclaim it"
			out = append(out, v)
		}
	}
	return out, nil
}
