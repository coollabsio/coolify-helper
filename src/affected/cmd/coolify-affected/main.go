// Command coolify-affected decides which applications of a Git repository a
// push affects, so that Coolify deploys only those. See README.md.
package main

import (
	"os"

	"github.com/coollabsio/coolify-helper/src/affected/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
