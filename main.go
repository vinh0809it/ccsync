// Command ccsync syncs Claude Code sessions between machines through an
// end-to-end-encrypted git repository.
package main

import (
	"os"

	"github.com/vinh0809it/ccsync/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=...".
var version string

func main() {
	os.Exit(cli.Run(os.Args[1:], version))
}
