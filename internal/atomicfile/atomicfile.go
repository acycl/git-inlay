// Package atomicfile replaces a file without ever exposing a partial write.
// The exclude files and hook shims git-inlay maintains are what keeps
// private paths out of the host, so a reader must see the old content or
// the new, never a truncated file left by an interrupted write.
package atomicfile

import (
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFile writes data to a temporary file beside path, syncs it, and
// renames it over path. A new file gets perm as os.WriteFile would; an
// existing file keeps its mode, and a symlink is followed, so the file it
// points at is what gets replaced.
func WriteFile(path string, data []byte, perm fs.FileMode) error {
	path = follow(path)
	existing, err := os.Stat(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	dir, base := filepath.Split(path)
	var f *os.File
	for {
		f, err = os.OpenFile(filepath.Join(dir, "."+base+"."+rand.Text()+".tmp"),
			os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	tmp := f.Name()
	if err := write(f, data, existing); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// follow returns the file a symlink at path points at, so the link stays
// and its target is replaced, as a write through it would do. A dangling
// link resolves to the file it names, which the write then creates.
func follow(path string) string {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		return target
	}
	link, err := os.Readlink(path)
	if err != nil {
		return path
	}
	if !filepath.IsAbs(link) {
		link = filepath.Join(filepath.Dir(path), link)
	}
	return link
}

// write fills the temporary file and gives it the mode of the file it will
// replace, if there is one.
func write(f *os.File, data []byte, existing fs.FileInfo) error {
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if existing != nil {
		return f.Chmod(existing.Mode().Perm())
	}
	return nil
}
