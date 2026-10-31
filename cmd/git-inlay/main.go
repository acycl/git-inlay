// Command git-inlay keeps private sets of files tracked in their own git
// repositories while they live inside another repository's working tree.
// Git exposes it as "git inlay".
package main

import (
	"os"

	"github.com/acycl/git-inlay/internal/cli"
)

// version is set by the release build with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.Options{
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Version: version,
	}))
}
