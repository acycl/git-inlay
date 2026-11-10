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
)

// interruptGroup starts cmd in a process group of its own and, once what
// git runs has created the file started, sends the group an interrupt,
// which is what a terminal does to the foreground job on Ctrl-C: git-inlay,
// git and whatever git runs all get the signal at once. It returns what
// Wait did.
func interruptGroup(t *testing.T, cmd *exec.Cmd, started string) error {
	t.Helper()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, started)
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
	started := filepath.Join(f.base, "started")
	loop := `for n in 1 2; do git-inlay claude -c "alias.wait=!touch '$STARTED'; sleep 5" wait; echo "after $n"; done`
	cmd := exec.Command("sh", "-c", loop)
	cmd.Dir, cmd.Env = f.root, append(f.env[:len(f.env):len(f.env)], "STARTED="+started)
	var out bytes.Buffer
	cmd.Stdout = &out
	interruptGroup(t, cmd, started)
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
	started := filepath.Join(f.base, "started")
	// The first wait is for git, which takes to ignoring the interrupt
	// only once it has started the editor.
	script := "#!/bin/sh\ntrap '' INT\nsleep 1\ntouch '" + started + "'\nsleep 1\necho edited > \"$1\"\n"
	if err := os.WriteFile(editor, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(binDir, "git-inlay"), "claude", "commit", "-q", "--allow-empty")
	cmd.Dir, cmd.Env = f.root, append(f.env[:len(f.env):len(f.env)], "GIT_EDITOR="+editor)
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := interruptGroup(t, cmd, started); err != nil {
		t.Fatalf("commit through an interrupted editor: %v\n%s", err, errOut.String())
	}
	if got := f.inlayOK(f.root, "claude", "log", "-1", "--format=%s"); got != "edited" {
		t.Fatalf("subject of the commit = %q, want the editor's", got)
	}
}
