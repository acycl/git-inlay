//go:build unix

package cli_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// interruptGroup starts cmd in a process group of its own and, once it has
// had time to start git, sends the group an interrupt, which is what a
// terminal does to the foreground job on Ctrl-C: git-inlay, git and
// whatever git runs all get the signal at once. It returns what Wait did.
func interruptGroup(t *testing.T, cmd *exec.Cmd) error {
	t.Helper()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	return cmd.Wait()
}

// Ctrl-C on a shell loop around a passthrough command must end the loop. A
// shell abandons it only when the command it waited for died of the
// interrupt, so git-inlay has to die of it too, after git has.
func TestInterruptStopsShellLoop(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "claude")
	loop := `for n in 1 2; do git-inlay claude -c 'alias.wait=!sleep 5' wait; echo "after $n"; done`
	cmd := exec.Command("sh", "-c", loop)
	cmd.Dir, cmd.Env = f.root, f.env
	var out bytes.Buffer
	cmd.Stdout = &out
	interruptGroup(t, cmd)
	if strings.Contains(out.String(), "after") {
		t.Fatalf("the loop went on after the interrupt:\n%s", out.String())
	}
}

// While git waits on an editor it ignores the interrupt, which is the
// editor's to handle, as a pager's is. git-inlay must outlive it as git
// does, so that the command completes and the sync after it runs.
func TestInterruptLeftToGitsEditor(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.inlayOK(f.root, "init", "claude")
	editor := filepath.Join(f.base, "editor.sh")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\ntrap '' INT\nsleep 2\necho edited > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(binDir, "git-inlay"), "claude", "commit", "-q", "--allow-empty")
	cmd.Dir, cmd.Env = f.root, append(f.env[:len(f.env):len(f.env)], "GIT_EDITOR="+editor)
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := interruptGroup(t, cmd); err != nil {
		t.Fatalf("commit through an interrupted editor: %v\n%s", err, errOut.String())
	}
	if got := f.inlayOK(f.root, "claude", "log", "-1", "--format=%s"); got != "edited" {
		t.Fatalf("subject of the commit = %q, want the editor's", got)
	}
}
