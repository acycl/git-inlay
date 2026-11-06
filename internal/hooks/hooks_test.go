package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ShellQuote's output, read back by sh, is the original string, with a
// single quote and other characters special to the shell kept literal.
func TestShellQuote(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	for _, s := range []string{
		"/home/o'brien/go/bin/git-inlay",
		"'",
		"''leading and trailing''",
		`$HOME "$(echo x)" \n * ;`,
		"",
	} {
		out, err := exec.Command(sh, "-c", "printf %s "+ShellQuote(s)).Output()
		if err != nil {
			t.Fatalf("sh -c printf %%s %s: %v", ShellQuote(s), err)
		}
		if string(out) != s {
			t.Errorf("sh read ShellQuote(%q) = %s back as %q", s, ShellQuote(s), out)
		}
	}
}

// Manager recognizes husky, lefthook and the pre-commit framework by their
// marker files, and answers "" for a host with none.
func TestManager(t *testing.T) {
	for _, c := range []struct {
		marker string // file or directory to create; "" for none
		dir    bool
		want   string
	}{
		{"", false, ""},
		{".husky", true, "husky"},
		{"lefthook.yml", false, "lefthook"},
		{"lefthook.yaml", false, "lefthook"},
		{".lefthook.yml", false, "lefthook"},
		{".pre-commit-config.yaml", false, "pre-commit"},
	} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var err error
		switch {
		case c.marker == "":
		case c.dir:
			err = os.Mkdir(filepath.Join(root, c.marker), 0o755)
		default:
			err = os.WriteFile(filepath.Join(root, c.marker), nil, 0o644)
		}
		if err != nil {
			t.Fatal(err)
		}
		if got := Manager(root); got != c.want {
			t.Errorf("Manager with %q = %q, want %q", c.marker, got, c.want)
		}
	}
}

// Command and Shim both run the named hook subcommand. Only the shim, a
// file of its own, carries the markers that let uninstall recognize it;
// Command is one line ending in the exec, since git appends "$@" itself
// and ours matches on that ending.
func TestCommandAndShim(t *testing.T) {
	for _, name := range []string{"pre-commit", "pre-push", "inlay-pre-push"} {
		line := `exec "$bin" hook ` + name
		cmd, shim := Command(name), Shim(name)
		if !strings.HasSuffix(cmd, line) {
			t.Errorf("Command(%q) = %q, want it to end with %q", name, cmd, line)
		}
		if strings.Contains(cmd, "\n") {
			t.Errorf("Command(%q) spans lines: %q", name, cmd)
		}
		if !strings.Contains(shim, line+` "$@"`+"\n") {
			t.Errorf("Shim(%q) does not run %q with the hook's arguments:\n%s", name, line, shim)
		}
		if !strings.HasPrefix(shim, "#!/bin/sh\n") {
			t.Errorf("Shim(%q) does not start with a #!/bin/sh line:\n%s", name, shim)
		}
		for _, m := range []string{Begin, End} {
			if !strings.Contains(shim, m+"\n") {
				t.Errorf("Shim(%q) lacks the marker %q:\n%s", name, m, shim)
			}
			if strings.Contains(cmd, m) {
				t.Errorf("Command(%q) carries the marker %q: %q", name, m, cmd)
			}
		}
	}
}
