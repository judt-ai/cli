// Command judt manages Judt Apps from the command line.
package main

import (
	"os"

	"github.com/judt-ai/cli/internal/cli"
)

// version is set at release: -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	os.Exit(cli.Execute(version))
}
