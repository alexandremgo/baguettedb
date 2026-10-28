// Package cli implements bdb, the command line of a local BaguetteDB instance: every command opens the instance
// in-process on a data directory, does its work and exits.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

// version identifies the build, stamped at link time with -X github.com/alexandremgo/baguettedb/pkg/cli.version=<tag>;
// "dev" for a plain go build.
var version = "dev"

// Run executes the command line args with the given standard streams and returns the process exit status. args[0] is
// the program name, whose base name the usage text shows, so the same code serves both the bdb and the baguettedb
// binaries. SIGINT and SIGTERM cancel the running command.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return RunContext(ctx, args, stdin, stdout, stderr)
}

// RunContext executes the command line args as Run does, but under ctx instead of the process's signals: cancelling
// ctx cancels the running command.
func RunContext(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	argv0 := "bdb"
	if len(args) > 0 {
		argv0, args = args[0], args[1:]
	}
	name := filepath.Base(argv0)

	root := newRootCmd(name, argv0)
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)

	failed, err := root.ExecuteContextC(ctx)
	if err != nil {
		root.PrintErrf("Error: %v\n", err)
		if usage, ok := errors.AsType[usageError](err); ok {
			if failed == nil {
				failed = root
			}
			help := usage.help
			if help == "" {
				help = fmt.Sprintf("Run `%s --help` for usage.", failed.CommandPath())
			}
			root.PrintErrln(help)
		}
	}
	return exitCode(err)
}
