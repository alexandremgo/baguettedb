// Command baguettedb is the bdb command line under the long name baguettedb: the same commands, from package cli, whose
// usage text shows the name the binary runs as. Every command opens a local BaguetteDB instance in-process on a data
// directory (~/.baguettedb by default); baguettedb mcp serves it to an agent over stdio, baguettedb serve over HTTP.
package main

import (
	"os"

	"github.com/alexandremgo/baguettedb/pkg/cli"
)

func main() {
	os.Exit(cli.Run(os.Args, os.Stdin, os.Stdout, os.Stderr))
}
