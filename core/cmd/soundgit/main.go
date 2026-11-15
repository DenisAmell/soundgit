// Command soundgit is version control for DAW projects.
package main

import (
	"os"

	"github.com/DenisAmell/soundgit/core/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
