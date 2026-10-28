package cli

import (
	"github.com/spf13/cobra"
)

// newStartersCmd builds the starters command group, whose commands show the catalog bdbs can be created from; bare,
// it lists the catalog.
func newStartersCmd(flags *globalFlags, open localOpener) *cobra.Command {
	return newListGroup("starters", "Show the starters a bdb can be created from", newStartersListCmd(flags, open))
}

// newStartersListCmd builds the starters list command, which prints each starter's key, name and description.
func newStartersListCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the starters, with the key bdbs create --starter takes",
		Args:  usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLocal(cmd, flags, open, func(l *local) error {
				starters, err := l.starters.List(cmd.Context())
				if err != nil {
					return err
				}
				if asJSON {
					return writeJSON(cmd.OutOrStdout(), toStartersJSON(starters))
				}
				rows := make([][]string, len(starters))
				for i, s := range starters {
					rows[i] = []string{s.Key, s.Name, s.Description}
				}
				return writeTable(cmd.OutOrStdout(), []string{"KEY", "NAME", "DESCRIPTION"}, rows)
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the starters as JSON, with their tags")
	return cmd
}
