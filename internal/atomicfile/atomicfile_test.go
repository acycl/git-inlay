package atomicfile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFileCreatesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exclude")
	if err := WriteFile(path, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "one\n" {
		t.Fatalf("after create: %q", got)
	}
	if err := WriteFile(path, []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "two\n" {
		t.Fatalf("after replace: %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "exclude" {
		t.Fatalf("directory holds %v, want only the target", entries)
	}
}

func TestWriteFileKeepsExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not meaningful on Windows")
	}
	path := filepath.Join(t.TempDir(), "pre-push")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o755 {
		t.Fatalf("mode after replace = %o, want 755", got)
	}
}

func TestWriteFileFollowsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs a privilege on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "exclude")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "exclude")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(link, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink was replaced: %v, %v", fi, err)
	}
	if got, _ := os.ReadFile(target); string(got) != "new\n" {
		t.Fatalf("target after write through the link: %q", got)
	}

	// A dangling link gets its target created rather than being replaced.
	dangling := filepath.Join(dir, "hook")
	if err := os.Symlink("dotfiles/hook", dangling); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(dangling, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(dangling); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the dangling symlink was replaced: %v, %v", fi, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "dotfiles", "hook")); string(got) != "#!/bin/sh\n" {
		t.Fatalf("target of the dangling link: %q", got)
	}
}

func TestWriteFileMissingDirectory(t *testing.T) {
	if err := WriteFile(filepath.Join(t.TempDir(), "missing", "file"), nil, 0o644); err == nil {
		t.Fatal("WriteFile into a missing directory succeeded")
	}
}
