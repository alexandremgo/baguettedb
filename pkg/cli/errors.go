package cli

import (
	"context"
	"errors"

	"github.com/spf13/cobra"
)

// usageError marks an error in how a command was invoked (an unknown command or flag, wrong arguments, an invalid
// flag value), as opposed to a failure while running it.
type usageError struct {
	err error

	// help is the line pointing at further help, printed in place of the failed command's --help pointer; empty for
	// that pointer.
	help string
}

// Error returns the wrapped error's message.
func (e usageError) Error() string {
	return e.err.Error()
}

// Unwrap returns the wrapped error.
func (e usageError) Unwrap() error {
	return e.err
}

// isUsageError reports whether err, or an error it wraps, is a usageError.
func isUsageError(err error) bool {
	_, ok := errors.AsType[usageError](err)
	return ok
}

// usageArgs wraps a cobra positional-argument validator so the errors it returns are usage errors.
func usageArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validate(cmd, args); err != nil {
			return usageError{err: err}
		}
		return nil
	}
}

// Process exit statuses a command can end with.
const (
	exitOK          = 0
	exitFailure     = 1
	exitUsage       = 2
	exitInterrupted = 130
)

// exitCode maps the error a command returned to the process exit status: exitOK for nil, exitUsage for a usageError,
// exitInterrupted when the command stopped because its context was cancelled (SIGINT or SIGTERM), exitFailure
// otherwise. Wrapped errors are unwrapped.
func exitCode(err error) int {
	switch {
	case err == nil:
		return exitOK
	case isUsageError(err):
		return exitUsage
	case errors.Is(err, context.Canceled):
		return exitInterrupted
	default:
		return exitFailure
	}
}
