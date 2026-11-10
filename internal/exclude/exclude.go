// Package exclude generates the two ignore files git-inlay maintains: the
// managed block in the host's info/exclude and each inlay's negation chain
// (docs/design.md section 4.4).
package exclude

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/acycl/git-inlay/internal/atomicfile"
	"github.com/acycl/git-inlay/internal/manifest"
)

// Markers delimit the managed block in the host's exclude file.
const (
	HostBegin = "# >>> git-inlay managed block; do not edit >>>"
	HostEnd   = "# <<< git-inlay managed block <<<"
)

// escape makes a path safe as a literal gitignore pattern. It works on
// bytes, not runes: every character it escapes is ASCII, and a name that
// is not valid UTF-8, legal on Linux, must reach git unchanged.
func escape(p string) string {
	var b strings.Builder
	for i := range len(p) {
		switch p[i] {
		case '#', '!', '*', '?', '[', '\\':
			b.WriteByte('\\')
		}
		b.WriteByte(p[i])
	}
	s := b.String()
	if trimmed := strings.TrimRight(s, " "); len(trimmed) != len(s) {
		s = trimmed + strings.Repeat(`\ `, len(s)-len(trimmed))
	}
	return s
}

// hostBlock renders the managed block for the union of claims across every
// inlay. Entries have no trailing slash so they match a file or a directory.
func hostBlock(claims []manifest.Claim) string {
	lines := map[string]bool{"/" + manifest.Dir: true}
	for _, c := range claims {
		if strings.HasPrefix(c.Path, manifest.Dir+"/") {
			continue
		}
		lines["/"+escape(c.Path)] = true
	}
	var b strings.Builder
	b.WriteString(HostBegin + "\n")
	for _, l := range slices.Sorted(maps.Keys(lines)) {
		b.WriteString(l + "\n")
	}
	b.WriteString(HostEnd + "\n")
	return b.String()
}

// bounds locates the managed block in s: where the begin marker's line
// starts and where the end marker's line finishes. A marker counts only
// as a whole line, so a comment quoting one is left alone. A begin marker
// whose end marker was deleted opens a block that runs to the end of s,
// so one rewrite settles it. ok is false when there is no block.
func bounds(s string) (begin, end int, ok bool) {
	begin = lineIndex(s, HostBegin, 0)
	if begin < 0 {
		return 0, 0, false
	}
	end = lineIndex(s, HostEnd, begin+len(HostBegin))
	if end < 0 {
		return begin, len(s), true
	}
	end += len(HostEnd)
	if end < len(s) && s[end] == '\r' {
		end++
	}
	if end < len(s) && s[end] == '\n' {
		end++
	}
	return begin, end, true
}

// lineIndex returns the offset of the first line of s, at or after from,
// that is exactly marker, allowing a CRLF ending, or -1.
func lineIndex(s, marker string, from int) int {
	for from <= len(s) {
		n := strings.Index(s[from:], marker)
		if n < 0 {
			return -1
		}
		at, end := from+n, from+n+len(marker)
		if (at == 0 || s[at-1] == '\n') &&
			(end == len(s) || s[end] == '\n' || strings.HasPrefix(s[end:], "\r\n")) {
			return at
		}
		from = at + 1
	}
	return -1
}

// splice returns content with the managed block replaced, or appended when
// absent. Everything outside the markers is preserved byte for byte. A
// file that has come to hold several blocks, by a hand edit or a copy
// from another clone, keeps the first one's place and loses the others,
// whose entries would otherwise hide paths from the host for good.
func splice(content []byte, block string) []byte {
	s := string(content)
	if begin, end, ok := bounds(s); ok {
		return []byte(s[:begin] + block + string(strip([]byte(s[end:]))))
	}
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return []byte(s + block)
}

// strip returns content without any managed block.
func strip(content []byte) []byte {
	s := string(content)
	for {
		begin, end, ok := bounds(s)
		if !ok {
			return []byte(s)
		}
		s = s[:begin] + s[end:]
	}
}

// WriteHost rewrites the host exclude file at path so its managed block
// lists claims, leaving the file untouched when nothing changes.
func WriteHost(path string, claims []manifest.Claim) error {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return replace(path, existing, splice(existing, hostBlock(claims)))
}

// HostCurrent reports whether the host exclude file at path already holds
// what WriteHost would leave for claims, or with no claims what RemoveHost
// would, so check can tell a stale block without writing.
func HostCurrent(path string, claims []manifest.Claim) (bool, error) {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	want := strip(existing)
	if len(claims) > 0 {
		want = splice(existing, hostBlock(claims))
	}
	return bytes.Equal(existing, want), nil
}

// RemoveHost drops the managed block from the host exclude file at path,
// for when no inlay remains. A missing file is left missing.
func RemoveHost(path string) error {
	existing, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return replace(path, existing, strip(existing))
}

// replace writes updated over path unless it already holds that content.
func replace(path string, existing, updated []byte) error {
	if bytes.Equal(existing, updated) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicfile.WriteFile(path, updated, 0o644)
}

// Inlay renders the negation chain that makes an inlay ignore everything
// except its claims. Every ancestor directory is re-included before its
// children are excluded, and claim negations come last so they win.
func Inlay(m *manifest.Manifest) string {
	claims := m.All()
	anc := map[string]bool{}
	for _, c := range claims {
		for _, a := range c.Ancestors() {
			anc[a] = true
		}
	}
	dirs := slices.Sorted(maps.Keys(anc))
	slices.SortStableFunc(dirs, func(a, b string) int {
		return strings.Count(a, "/") - strings.Count(b, "/")
	})
	slices.SortFunc(claims, func(a, b manifest.Claim) int { return strings.Compare(a.Path, b.Path) })

	var b strings.Builder
	fmt.Fprintf(&b, "# generated by git-inlay for inlay %q; do not edit\n", m.Name)
	b.WriteString("/*\n")
	for _, d := range dirs {
		fmt.Fprintf(&b, "!/%s/\n/%s/*\n", escape(d), escape(d))
	}
	for _, c := range claims {
		if c.Dir {
			fmt.Fprintf(&b, "!/%s/\n", escape(c.Path))
		} else {
			fmt.Fprintf(&b, "!/%s\n", escape(c.Path))
		}
	}
	return b.String()
}
