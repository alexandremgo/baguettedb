package cli

import (
	"io"

	"github.com/spf13/cobra"
)

// agentGuide is what docs prints bare: how an agent finds its way in the bdbs, the command line's counterpart of the
// instructions an MCP session starts with.
const agentGuide = `BaguetteDB keeps structured data for you and your AI agents: each bdb (short for
baguettedb) is a database about one topic, which its owner shares with members. The bdb command works in them.

Before working in a bdb:

1. bdb bdbs lists your bdbs with their tables: pick the one the task belongs to. Create one (bdb bdbs create, blank
   or from a starter that bdb starters lists) only when the user asks, with its name confirmed first.
2. bdb bdbs describe <bdb> prints its tables with their columns, its skills in full and its formulas. Read the
   skills before changing anything: they say how the bdb is meant to be used.
3. Prefer its formulas to writing SQL: bdb formulas run <bdb> <formula> --param name=value runs one, and
   bdb formulas show <bdb> <formula> prints its parameters.
4. Read bdb docs sql before writing SQL (bdb q, bdb sql data, bdb sql schema), and bdb docs formulas before writing
   a formula (bdb formulas create).

Every change is logged: bdb changes <bdb> lists them, and bdb changes undo <bdb> reverts your last one. When the
user wants to keep a workflow, a naming convention or a rule, offer to write it down as a skill
(bdb skills create).

Manuals: bdb docs sql, bdb docs formulas.
`

// newDocsCmd builds the docs command, which prints the guide for agents, or one of the built-in manuals.
func newDocsCmd(flags *globalFlags, open localOpener) *cobra.Command {
	return &cobra.Command{
		Use:   "docs [<topic>]",
		Short: "Print the guide for agents, or a manual: sql or formulas",
		Long: "docs, bare, prints what an agent should know before working in a bdb, and needs no data directory. " +
			"With a topic it prints a built-in manual: sql, the SQL engine's rules, the change log and the limits in " +
			"force; formulas, how to write a formula.",
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				_, err := io.WriteString(cmd.OutOrStdout(), agentGuide)
				return err
			}
			return runLocal(cmd, flags, open, func(l *local) error {
				manual, err := l.docs.Manual(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				_, err = io.WriteString(cmd.OutOrStdout(), manual)
				return err
			})
		},
	}
}
