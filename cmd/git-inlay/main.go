// Command git-inlay keeps private sets of files tracked in their own git
// repositories while they live inside another repository's working tree.
// Git exposes it as "git inlay".
package main

import (
	"cmp"
	"os"
	"runtime/debug"
	"strings"

	"github.com/acycl/git-inlay/internal/cli"
)

// version is set by the release build with -ldflags "-X main.version=...".
// Builds without it fall back to moduleVersion.
var version string

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.Options{
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Version: cmp.Or(version, moduleVersion()),
	}))
}

// moduleVersion returns the main module's version recorded in the binary,
// without the "v" prefix so it matches the release build's version, or ""
// when none was recorded. "go install ...@v1.2.3" records v1.2.3; a build
// from a git checkout records a pseudo-version.
func moduleVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "(devel)" {
		return ""
	}
	return strings.TrimPrefix(info.Main.Version, "v")
}
