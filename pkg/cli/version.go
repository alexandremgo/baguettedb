package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newVersionCmd builds the version command, which prints the same line as --version.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", cmd.Root().Name(), version)
			return err
		},
	}
}
