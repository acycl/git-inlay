// Package manifest defines an inlay's list of claims and the rules a claim
// must satisfy (docs/design.md section 4.3).
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Dir is the directory at the host root that holds every inlay's manifest.
// The host excludes it as a whole; each inlay owns its own file inside it.
const Dir = ".git-inlay"

// File returns the root-relative manifest path for an inlay.
func File(name string) string { return Dir + "/" + name + ".paths" }

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// ValidName reports whether name may be used for an inlay.
func ValidName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid inlay name %q: use lowercase letters, digits, '.', '_' and '-', starting with a letter or digit", name)
	}
	return nil
}

// ErrRedundant reports a claim already covered by an existing one.
var ErrRedundant = errors.New("already covered by an existing claim")

// Claim is one path an inlay owns. A directory claim owns everything beneath
// it.
type Claim struct {
	Path string // root-relative, forward slashes, no trailing slash
	Dir  bool
}

// String renders the claim as it appears in a manifest.
func (c Claim) String() string {
	if c.Dir {
		return c.Path + "/"
	}
	return c.Path
}

// Owns reports whether p is the claim itself or, for a directory claim,
// beneath it.
func (c Claim) Owns(p string) bool {
	return p == c.Path || (c.Dir && strings.HasPrefix(p, c.Path+"/"))
}

// Overlaps reports whether either claim owns the other.
func (c Claim) Overlaps(o Claim) bool { return c.Owns(o.Path) || o.Owns(c.Path) }

// Ancestors returns the claim's proper ancestor directories, shallowest first.
func (c Claim) Ancestors() []string {
	var out []string
	for i, r := range c.Path {
		if r == '/' {
			out = append(out, c.Path[:i])
		}
	}
	return out
}

// Pathspec returns a literal git pathspec that matches the claim and, for a
// directory, everything beneath it.
func (c Claim) Pathspec() string { return ":(literal)" + c.Path }

// ParseClaim parses a root-relative path as written on the command line.
// A trailing slash marks a directory. It refuses what no new claim may
// hold, a control character among it; Parse is laxer with a claim that a
// manifest already holds.
func ParseClaim(s string) (Claim, error) {
	c, err := parseClaim(s)
	if err == nil && strings.ContainsFunc(c.Path, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		// A tab would also break the tab-separated output of ls and which.
		err = fmt.Errorf("invalid path %q: contains a control character", s)
	}
	return c, err
}

// parseClaim parses a claim as a manifest writes it.
func parseClaim(s string) (Claim, error) {
	c := Claim{Path: s}
	if strings.HasSuffix(s, "/") {
		c.Dir = true
		c.Path = strings.TrimSuffix(s, "/")
	}
	if err := validatePath(c.Path); err != nil {
		return Claim{}, fmt.Errorf("invalid path %q: %w", s, err)
	}
	return c, nil
}

// validatePath applies the syntax rules a claim path must satisfy.
func validatePath(p string) error {
	switch {
	case p == "":
		return errors.New("empty path")
	case strings.ContainsAny(p, "\r\n"):
		return errors.New("contains a line break")
	case strings.Contains(p, "\x00"):
		// What a file saved as UTF-16 is full of; no path can hold one.
		return errors.New("contains a NUL byte")
	case strings.HasPrefix(p, "#"):
		return errors.New("starts with '#'")
	case p != strings.TrimSpace(p):
		return errors.New("has leading or trailing whitespace")
	case strings.HasPrefix(p, "/"):
		return errors.New("must be relative to the repository root")
	case strings.Contains(p, "\\"):
		return errors.New("must use forward slashes")
	}
	segs := strings.Split(p, "/")
	for _, seg := range segs {
		switch {
		case seg == "":
			return errors.New("has an empty path segment")
		case seg == "." || seg == "..":
			return errors.New("contains a '.' or '..' segment")
		case strings.HasSuffix(seg, " "):
			return errors.New("has a segment ending in a space")
		}
	}
	// Any case, since a case-insensitive filesystem reaches the same
	// directory through each.
	switch {
	case strings.EqualFold(segs[0], ".git"):
		return errors.New("is inside .git")
	case strings.EqualFold(segs[0], Dir):
		return fmt.Errorf("is inside %s, which is reserved", Dir)
	}
	return nil
}

// Manifest is an inlay's set of claims, kept sorted and non-overlapping.
type Manifest struct {
	Name   string
	Claims []Claim
}

// New returns an empty manifest.
func New(name string) *Manifest { return &Manifest{Name: name} }

// ErrConflict reports a manifest that holds the markers of an unresolved
// merge, whose lines would otherwise parse as claims.
var ErrConflict = errors.New("holds merge conflict markers; resolve the merge and keep the claims of both sides")

// conflictMarker reports whether line is one git writes into a file it
// could not merge.
func conflictMarker(line string) bool {
	for _, m := range []string{"<<<<<<<", "|||||||", "=======", ">>>>>>>"} {
		if line == m || strings.HasPrefix(line, m+" ") {
			return true
		}
	}
	return false
}

// Parse reads a manifest file. Blank lines and '#' comments are ignored, a
// byte order mark is skipped, and a claim already covered by an earlier
// one is dropped. A claim with a control character other than NUL is read
// as it stands, since a manifest written before ParseClaim refused such
// claims must not lock its inlay.
func Parse(name string, data []byte) (*Manifest, error) {
	m := New(name)
	n := 0
	for raw := range bytes.Lines(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))) {
		n++
		line := strings.TrimRight(string(raw), "\r\n")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if conflictMarker(line) {
			return nil, fmt.Errorf("%s line %d: %w", File(name), n, ErrConflict)
		}
		c, err := parseClaim(line)
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %w", File(name), n, err)
		}
		if _, err := m.Add(c); err != nil && !errors.Is(err, ErrRedundant) {
			return nil, fmt.Errorf("%s line %d: %w", File(name), n, err)
		}
	}
	return m, nil
}

// Format renders the manifest with its header and sorted claims.
func (m *Manifest) Format() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# git-inlay manifest for %q. Edit with: git inlay claim %s <path>\n", m.Name, m.Name)
	for _, c := range m.Claims {
		b.WriteString(c.String())
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// Self is the implicit claim on the manifest file itself.
func (m *Manifest) Self() Claim { return Claim{Path: File(m.Name)} }

// All returns the explicit claims followed by the implicit self claim.
func (m *Manifest) All() []Claim { return append(slices.Clone(m.Claims), m.Self()) }

// Owner returns the claim that owns p, if any. The self claim counts.
func (m *Manifest) Owner(p string) (Claim, bool) {
	for _, c := range m.All() {
		if c.Owns(p) {
			return c, true
		}
	}
	return Claim{}, false
}

// Add inserts c. It fails with ErrRedundant when c is already present or an
// existing claim covers it, and otherwise removes and returns the existing
// claims that c covers, including one at the same path whose kind changed.
func (m *Manifest) Add(c Claim) (absorbed []Claim, err error) {
	for _, e := range m.Claims {
		if e == c || (e.Path != c.Path && e.Owns(c.Path)) {
			return nil, ErrRedundant
		}
	}
	kept := make([]Claim, 0, len(m.Claims)+1)
	for _, e := range m.Claims {
		if c.Owns(e.Path) {
			absorbed = append(absorbed, e)
		} else {
			kept = append(kept, e)
		}
	}
	m.Claims = append(kept, c)
	slices.SortFunc(m.Claims, func(a, b Claim) int { return strings.Compare(a.Path, b.Path) })
	return absorbed, nil
}

// Remove deletes the claim at exactly p and reports whether it existed.
func (m *Manifest) Remove(p string) bool {
	p = strings.TrimSuffix(p, "/")
	n := len(m.Claims)
	m.Claims = slices.DeleteFunc(m.Claims, func(c Claim) bool { return c.Path == p })
	return len(m.Claims) != n
}
