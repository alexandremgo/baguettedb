// Command bdb is the command line of a local BaguetteDB instance: every command opens the instance in-process on a data
// directory, ~/.baguettedb unless --data-dir or BAGUETTEDB_DATA_DIR names another. bdb mcp serves it to an agent over
// stdio and bdb serve over HTTP; the other commands manage bdbs, their data and the accounts, then exit. The commands
// live in package cli.
package main

import (
	"os"

	"github.com/alexandremgo/baguettedb/pkg/cli"
)

func main() {
	os.Exit(cli.Run(os.Args, os.Stdin, os.Stdout, os.Stderr))
}
