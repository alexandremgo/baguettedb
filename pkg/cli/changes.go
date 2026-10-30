package cli

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// newChangesCmd builds the changes command group, whose commands list a bdb's changes and undo, redo or revert them;
// bare, it lists them.
func newChangesCmd(flags *globalFlags, open localOpener) *cobra.Command {
	return newListGroup("changes", "List, undo, redo and revert a bdb's changes",
		newChangesListCmd(flags, open),
		newChangesUndoCmd("undo", "Undo your last change in a bdb", flags, open),
		newChangesUndoCmd("redo", "Redo your last undone change in a bdb", flags, open),
		newChangesRevertCmd(flags, open),
	)
}

// newChangesListCmd builds the changes list command, which prints a bdb's changes newest first, one line per batch.
func newChangesListCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var filter domain.ChangeFilter
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list <bdb>",
		Short: "List a bdb's changes, newest first",
		Long: "list prints a bdb's changes, newest first, one line per batch: what one write, undo, redo or revert " +
			"touched: rows written and schema changed, while formulas and skills stay out of the log. " + bdbArgHelp +
			" --json adds every entry with its values before and after.",
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			if filter.Kind != "" && filter.Kind != domain.ChangeKindData && filter.Kind != domain.ChangeKindSchema {
				return usageError{err: fmt.Errorf("invalid --kind %q: want data or schema", filter.Kind)}
			}
			if filter.Limit < 0 {
				return usageError{err: fmt.Errorf("invalid --limit %d: want 1 or more", filter.Limit)}
			}
			return runLocal(cmd, flags, open, func(l *local) error {
				b, batches, err := l.changes.List(cmd.Context(), args[0], filter)
				if err != nil {
					return err
				}
				if asJSON {
					return writeJSON(cmd.OutOrStdout(), toChangeBatchesJSON(batches))
				}
				if len(batches) == 0 {
					if filter.Table != "" || filter.Kind != "" || filter.Mine {
						cmd.PrintErrf("No matching changes in %s.\n", b.Name)
						return nil
					}
					cmd.PrintErrf("No changes in %s.\n", b.Name)
					return nil
				}
				users, err := l.users.List(cmd.Context())
				if err != nil {
					return err
				}
				emails := make(map[string]string, len(users))
				for _, u := range users {
					emails[u.ID.String()] = u.Email
				}
				return writeChangesTable(cmd.OutOrStdout(), batches, emails)
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&filter.Table, "table", "", "only the changes to this table or schema object")
	f.StringVar(&filter.Kind, "kind", "", "only data (row) or schema changes")
	f.BoolVar(&filter.Mine, "mine", false, "only your own changes")
	f.IntVar(&filter.Limit, "limit", 0, "show at most this many changes (default 20)")
	f.BoolVar(&asJSON, "json", false, "print the changes as JSON, with every entry and its values")
	return cmd
}

// writeChangesTable writes batches to w, one line each: when, by whom, the kind, what it touched, and its batch ID.
//
// The author is "you" for the caller's own changes, the email emails holds for another account's ID, and "someone
// else" for an author the instance has no account for, as a bdb imported from another instance has.
func writeChangesTable(w io.Writer, batches []domain.ChangeBatch, emails map[string]string) error {
	rows := make([][]string, len(batches))
	for i, b := range batches {
		by := "you"
		if !b.Mine {
			by = cmp.Or(emails[b.ChangedBy], "someone else")
		}
		rows[i] = []string{b.ChangedAt.Local().Format("2006-01-02 15:04:05"), by, b.Kind, changeSummary(b), b.ID}
	}
	return writeTable(w, []string{"WHEN", "BY", "KIND", "WHAT", "BATCH"}, rows)
}

// changeSummary describes what a batch touched, its entries grouped by operation and target in order of first
// appearance ("UPDATE places ×3, INSERT notes", "CREATE TABLE notes"), prefixed with the origin of a batch that
// reversed or re-applied another ("undo: DELETE places ×2"), and marked when only part of it was read.
func changeSummary(b domain.ChangeBatch) string {
	type group struct {
		label string
		count int
	}
	var groups []*group
	for _, e := range b.Entries {
		label := strings.TrimSpace(strings.Join([]string{e.Operation, e.ObjectType, e.Table}, " "))
		label = strings.Join(strings.Fields(label), " ")
		i := slices.IndexFunc(groups, func(g *group) bool { return g.label == label })
		if i < 0 {
			groups = append(groups, &group{label: label})
			i = len(groups) - 1
		}
		groups[i].count++
	}
	parts := make([]string, len(groups))
	for i, g := range groups {
		parts[i] = g.label
		if g.count > 1 {
			parts[i] += fmt.Sprintf(" ×%d", g.count)
		}
	}
	summary := strings.Join(parts, ", ")
	if b.Origin != "" && b.Origin != "change" {
		summary = b.Origin + ": " + summary
	}
	if b.Partial {
		summary += " (more not shown)"
	}
	return summary
}

// newChangesUndoCmd builds the changes undo or redo command, named name, which reverses or re-applies the last
// change in a bdb: the caller's own, or anyone's with --anyone.
func newChangesUndoCmd(name, short string, flags *globalFlags, open localOpener) *cobra.Command {
	var anyone, asJSON bool
	cmd := &cobra.Command{
		Use:   name + " <bdb>",
		Short: short,
		Long: short + ", or anyone's with --anyone (the bdb's owner only). Only rows and schema are in the change " +
			"log: a formula or a skill changed since is not reverted. " + bdbArgHelp,
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLocal(cmd, flags, open, func(l *local) error {
				reverse, done := l.changes.Undo, "Undid the last change"
				if name == "redo" {
					reverse, done = l.changes.Redo, "Redid the last undone change"
				}
				b, result, err := reverse(cmd.Context(), args[0], anyone)
				if err != nil {
					return changeConflictError(cmd, b, err)
				}
				return printReversal(cmd, done, result, asJSON)
			})
		},
	}
	cmd.Flags().BoolVar(&anyone, "anyone", false, "act on the last change whoever made it, not only yours")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON, with the rows it changed")
	return cmd
}

// newChangesRevertCmd builds the changes revert command, which reverses one batch, or one entry of a batch, whoever
// made it.
func newChangesRevertCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var entryID int64
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "revert <bdb> (<batch> | --entry <id>)",
		Short: "Revert one change in a bdb, whoever made it",
		Long: "revert reverses one change: a batch, by its ID or a prefix of it (changes list and sql data print " +
			"them), or a single entry of one with --entry (its ID is in changes list --json). " + bdbArgHelp,
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case len(args) == 2 && entryID != 0:
				return usageError{err: errors.New("give a batch or --entry, not both")}
			case len(args) == 1 && entryID == 0:
				return usageError{err: errors.New("give the batch to revert, or --entry")}
			case entryID < 0:
				return usageError{err: fmt.Errorf("invalid --entry %d", entryID)}
			}
			return runLocal(cmd, flags, open, func(l *local) error {
				if entryID != 0 {
					b, result, err := l.changes.RevertEntry(cmd.Context(), args[0], entryID)
					if errors.Is(err, domain.ErrChangeNotFound) {
						return fmt.Errorf("%w with entry ID %d", err, entryID)
					}
					if err != nil {
						return changeConflictError(cmd, b, err)
					}
					return printReversal(cmd, fmt.Sprintf("Reverted entry %d", entryID), result, asJSON)
				}
				b, batchID, result, err := l.changes.RevertBatch(cmd.Context(), args[0], args[1])
				if errors.Is(err, domain.ErrChangeNotFound) {
					return fmt.Errorf("%w; `%s` lists them", err,
						shellJoin([]string{cmd.Root().Name(), "changes", cmp.Or(b.Name, args[0])}))
				}
				if err != nil {
					return changeConflictError(cmd, b, err)
				}
				return printReversal(cmd, "Reverted batch "+batchID, result, asJSON)
			})
		},
	}
	cmd.Flags().Int64Var(&entryID, "entry", 0, "revert only this change log entry")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON, with the rows it changed")
	return cmd
}

// changeConflictError rewords err, when it is a conflict in the bdb b, with the command that reverts a blocking entry;
// any other error passes through.
func changeConflictError(cmd *cobra.Command, b domain.Bdb, err error) error {
	conflict, ok := errors.AsType[*domain.ChangeConflictError](err)
	if !ok {
		return err
	}
	revert := shellJoin([]string{cmd.Root().Name(), "changes", "revert", b.Name})
	return fmt.Errorf("in %s: %s", b.Name, conflict.Message(func(id string) string {
		return "`" + revert + " --entry " + id + "`"
	}))
}

// printReversal prints the result of an undo, redo or revert: as JSON under asJSON, otherwise done, the count of rows
// it changed when there are any, and the batch it recorded.
func printReversal(cmd *cobra.Command, done string, r domain.QueryResult, asJSON bool) error {
	if asJSON {
		doc, err := toChangeResultJSON(r)
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), doc)
	}
	line := done
	if r.AffectedRowCount > 0 {
		line += ": " + countOf(int(r.AffectedRowCount), "row") + " changed"
	}
	if r.BatchID != "" {
		line += " (batch " + r.BatchID + ")"
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), line)
	return err
}
