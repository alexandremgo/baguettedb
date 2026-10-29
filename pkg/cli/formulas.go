package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
)

// newFormulasCmd builds the formulas command group, whose commands list, show, run, create, update and delete a bdb's
// formulas; bare, it lists them.
func newFormulasCmd(flags *globalFlags, open localOpener) *cobra.Command {
	return newListGroup("formulas", "List, show, run, create, update and delete a bdb's formulas",
		newFormulasListCmd(flags, open), newFormulasShowCmd(flags, open), newFormulasRunCmd(flags, open),
		newFormulasCreateCmd(flags, open), newFormulasUpdateCmd(flags, open), newFormulasDeleteCmd(flags, open))
}

// newFormulasListCmd builds the formulas list command, which prints a bdb's formulas, hidden ones included.
func newFormulasListCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list <bdb>",
		Short: "List a bdb's formulas",
		Long: "list prints a bdb's formulas: each one's name (what formulas run takes), kind (a view reads, an " +
			"action writes), label and description, hidden ones included. " + bdbArgHelp,
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLocal(cmd, flags, open, func(l *local) error {
				b, formulas, err := l.formulas.List(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				if asJSON {
					if formulas == nil {
						formulas = []apptypes.Formula{}
					}
					return writeJSON(cmd.OutOrStdout(), formulas)
				}
				if len(formulas) == 0 {
					cmd.PrintErrf("No formulas in %s.\n", b.Name)
					return nil
				}
				return writeFormulaList(cmd.OutOrStdout(), formulas)
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the formulas as JSON")
	return cmd
}

// writeFormulaList writes formulas to w as a table of their names, kinds (hidden ones marked), labels and descriptions.
func writeFormulaList(w io.Writer, formulas []apptypes.Formula) error {
	rows := make([][]string, len(formulas))
	for i, f := range formulas {
		kind := f.Kind
		if f.Hidden {
			kind += ", hidden"
		}
		rows[i] = []string{f.Name, kind, f.Label, f.Description}
	}
	return writeTable(w, []string{"NAME", "KIND", "LABEL", "DESCRIPTION"}, rows)
}

// newFormulasShowCmd builds the formulas show command, which prints one of a bdb's formulas: what it is, the
// parameters formulas run takes, and its SQL.
func newFormulasShowCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show <bdb> <formula>",
		Short: "Show a formula of a bdb and its parameters",
		Long: "show prints a formula of a bdb, by name: its kind, label and description, each parameter formulas run " +
			"takes (its type, whether it is required, its default and the values it accepts), and its SQL. " +
			bdbArgHelp,
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLocal(cmd, flags, open, func(l *local) error {
				b, f, err := l.formulas.Show(cmd.Context(), args[0], args[1])
				if err != nil {
					return formulaError(cmd, b, args[1], err)
				}
				if asJSON {
					return writeJSON(cmd.OutOrStdout(), toFormulaDetailJSON(f))
				}
				return printFormula(cmd.OutOrStdout(), f)
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the formula as JSON")
	return cmd
}

// formulaError adds to err, when it reports an unknown formula of the bdb b, the command that lists b's formulas, and
// words a parameter value error as a usage error about its --param flag, pointing at the command that shows the
// formula's parameters.
func formulaError(cmd *cobra.Command, b domain.Bdb, formula string, err error) error {
	if refused, ok := errors.AsType[*domain.ParamValueError](err); ok {
		show := shellJoin([]string{cmd.Root().Name(), "formulas", "show", b.Name, formula})
		return usageError{
			err:  fmt.Errorf("--param %s %s", refused.Param, refused.Problem),
			help: fmt.Sprintf("Run `%s` for its parameters.", show),
		}
	}
	if errors.Is(err, domain.ErrFormulaNotFound) {
		return fmt.Errorf("%w; `%s formulas %s` lists them", err, cmd.Root().Name(), b.Name)
	}
	return err
}

// printFormula writes f to w: a heading with its kind, its label and description, a table of its parameters, and its
// SQL.
func printFormula(w io.Writer, f domain.FormulaDetail) error {
	var out strings.Builder
	about := formulaKind(f)
	if f.Hidden {
		about += ", hidden"
	}
	fmt.Fprintf(&out, "%s  (%s)\n", f.Name, about)
	if f.Label != "" {
		fmt.Fprintf(&out, "Label:        %s\n", f.Label)
	}
	if f.Description != "" {
		fmt.Fprintf(&out, "Description:  %s\n", f.Description)
	}

	if len(f.Params) == 0 {
		out.WriteString("\nNo parameters.\n")
	} else {
		rows := make([][]string, len(f.Params))
		for i, p := range f.Params {
			required := "no"
			if p.Required {
				required = "yes"
			}
			defaults := p.Default
			if len(p.Defaults) > 0 {
				defaults = "[" + strings.Join(p.Defaults, ", ") + "]"
			}
			rows[i] = []string{p.Name, p.Type, required, defaults, paramValues(p)}
		}
		out.WriteString("\nPARAMETERS\n")
		if err := writeTable(&out, []string{"NAME", "TYPE", "REQUIRED", "DEFAULT", "VALUES"}, rows); err != nil {
			return err
		}
	}

	fmt.Fprintf(&out, "\nSQL\n%s\n", strings.TrimSuffix(f.SQL, "\n"))
	_, err := io.WriteString(w, out.String())
	return err
}

// paramWhenOps maps a parameter condition's comparison to its operator.
var paramWhenOps = map[string]string{"eq": "=", "ne": "!=", "lt": "<", "lte": "<=", "gt": ">", "gte": ">="}

// paramValues describes the values the parameter p accepts and how --param gives them: a row's rowid for a selected
// row, a JSON array for a list, the column a lookup suggests values from, the options it is limited to, and the
// condition under which it applies.
func paramValues(p domain.FormulaParam) string {
	var parts []string
	switch p.Source {
	case domain.ParamSourceSelectionSingle:
		parts = append(parts, "a row's rowid")
	case domain.ParamSourceSelectionBulk:
		parts = append(parts, "JSON array of rowids")
	case domain.ParamSourceReorder:
		parts = append(parts, "JSON array of rowids, in their new order")
	case domain.ParamSourcePagination:
		parts = append(parts, "pagination")
	case domain.ParamSourceWindow:
		parts = append(parts, "a bound of the date range")
	}
	list := ""
	if p.Multiple {
		list = "JSON array of "
	}
	if p.Lookup != nil {
		from := "lookup: " + p.Lookup.Table + "." + p.Lookup.ValueColumn
		if p.Lookup.LabelColumn != "" && p.Lookup.LabelColumn != p.Lookup.ValueColumn {
			from += " (shown as " + p.Lookup.LabelColumn + ")"
		}
		if p.Lookup.AllowNew {
			from += ", or a new value"
		}
		parts = append(parts, list+from)
		list = ""
	}
	if len(p.Options) > 0 {
		values := make([]string, len(p.Options))
		for i, o := range p.Options {
			values[i] = o.Value
		}
		parts = append(parts, list+"one of: "+strings.Join(values, ", "))
		list = ""
	}
	if list != "" {
		parts = append(parts, strings.TrimSuffix(list, " of "))
	}
	if len(p.Fields) > 0 {
		names := make([]string, len(p.Fields))
		for i, field := range p.Fields {
			names[i] = field.Name
		}
		parts = append(parts, "rows of: "+strings.Join(names, ", "))
	}
	if p.When != nil {
		op, ok := paramWhenOps[p.When.Op]
		if !ok {
			op = p.When.Op
		}
		parts = append(parts, "only when "+p.When.Param+" "+op+" "+p.When.Value)
	}
	return strings.Join(parts, "; ")
}

// newFormulasRunCmd builds the formulas run command, which runs one of a bdb's formulas with --param values and
// prints a view's rows or an action's changes.
func newFormulasRunCmd(flags *globalFlags, open localOpener) *cobra.Command {
	params := paramFlag{}
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "run <bdb> <formula> [--param name=value]...",
		Short: "Run a formula of a bdb",
		Long: "run runs a formula of a bdb, by name: a view prints its rows, an action the rows it changed. " +
			bdbArgHelp + " Each --param sets one of the formula's parameters (formulas show lists them), converted " +
			"to the type it declares; a list (a multi-select, or the rows to act on) is a JSON array, e.g. " +
			"--param 'rowids=[3,5]' (quoted, as shells read [ ] as a pattern).",
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLocal(cmd, flags, open, func(l *local) error {
				b, run, err := l.formulas.Run(cmd.Context(), args[0], args[1], params)
				if err != nil {
					return formulaError(cmd, b, args[1], err)
				}
				if run.Kind == domain.FormulaKindAction {
					return printQueryResult(cmd, run.Result, domain.IntentData, asJSON)
				}
				if err := printQueryResult(cmd, run.Result, domain.IntentQuery, asJSON); err != nil {
					return err
				}
				if run.HasMore && run.NextCursor != nil {
					cmd.PrintErrf("More rows: add --param cursor=%d\n", *run.NextCursor)
				}
				return nil
			})
		},
	}
	cmd.Flags().Var(params, "param", "a parameter of the formula, repeated for each one")
	cmd.Flags().BoolVar(&asJSON, "json", false,
		"print a view's rows as a JSON array, or an action's result as an object")
	return cmd
}

// formulaSpecHelp describes the JSON object formulas create and update read.
const formulaSpecHelp = "The formula is a JSON object read from --file (- reads stdin), with the fields of the " +
	"create_formula MCP tool: name, description and sql, and optionally label, params, display, " +
	"attached_to_formula_id, display_order and hidden. A key it does not know is refused, so a misspelled field is " +
	"not dropped."

// newFormulasCreateCmd builds the formulas create command, which creates a formula in a bdb from a JSON object.
func newFormulasCreateCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var file string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "create <bdb> --file <path>",
		Short: "Create a formula in a bdb",
		Long:  "create creates a formula in a bdb. " + bdbArgHelp + " " + formulaSpecHelp,
		Example: "  bdb formulas create Kanban --file add-task.json\n" +
			"  bdb formulas create Kanban --file - <<'EOF'\n" +
			"  {\"name\": \"open-tasks\", \"description\": \"Tasks not done yet\", " +
			"\"sql\": \"SELECT name FROM tasks WHERE status != 'done'\"}\n" +
			"  EOF",
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := readFileFlag(cmd.InOrStdin(), file, "formula")
			if err != nil {
				return err
			}
			return runLocal(cmd, flags, open, func(l *local) error {
				b, f, err := l.formulas.Create(cmd.Context(), args[0], spec)
				if err != nil {
					return err
				}
				return printWrittenFormula(cmd, "Created", b, f, asJSON)
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "read the formula from this file, or from stdin with -")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the created formula as JSON")
	return cmd
}

// newFormulasUpdateCmd builds the formulas update command, which changes a formula of a bdb from a JSON object of the
// fields to change.
func newFormulasUpdateCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var file string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "update <bdb> <formula> --file <path>",
		Short: "Change a formula of a bdb",
		Long: "update changes a formula of a bdb, by name: only the fields the JSON object holds change, params " +
			"replace the whole list, and \"display\": null resets the display to a table. " + bdbArgHelp + " " +
			formulaSpecHelp,
		Example: "  bdb formulas update Kanban open-tasks --file - <<'EOF'\n" +
			"  {\"sql\": \"SELECT name FROM tasks WHERE status = 'todo'\"}\n" +
			"  EOF",
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := readFileFlag(cmd.InOrStdin(), file, "formula")
			if err != nil {
				return err
			}
			return runLocal(cmd, flags, open, func(l *local) error {
				b, f, err := l.formulas.Update(cmd.Context(), args[0], args[1], spec)
				if err != nil {
					return formulaError(cmd, b, args[1], err)
				}
				return printWrittenFormula(cmd, "Updated", b, f, asJSON)
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "read the fields to change from this file, or from stdin with -")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the changed formula as JSON")
	return cmd
}

// newFormulasDeleteCmd builds the formulas delete command, which deletes one of a bdb's formulas.
func newFormulasDeleteCmd(flags *globalFlags, open localOpener) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <bdb> <formula>",
		Short: "Delete a formula of a bdb",
		Long:  "delete deletes a formula of a bdb, by name. " + bdbArgHelp,
		Args:  usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLocal(cmd, flags, open, func(l *local) error {
				b, err := l.formulas.Delete(cmd.Context(), args[0], args[1])
				if err != nil {
					return formulaError(cmd, b, args[1], err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Deleted formula %s from %s\n", args[1], b.Name)
				return err
			})
		},
	}
	return cmd
}

// printWrittenFormula prints the formula f a command just created or updated in the bdb b: as JSON under asJSON, or
// as one line naming it with its kind, which the SQL decides, after verb.
func printWrittenFormula(cmd *cobra.Command, verb string, b domain.Bdb, f domain.FormulaDetail, asJSON bool) error {
	if asJSON {
		return writeJSON(cmd.OutOrStdout(), toFormulaDetailJSON(f))
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s formula %s in %s (%s)\n", verb, f.Name, b.Name, formulaKind(f))
	return err
}

// formulaKind words the kind of the formula f, which its SQL decides: "view", or "action" with the change it makes
// ("action, inserts").
func formulaKind(f domain.FormulaDetail) string {
	if f.Kind == domain.FormulaKindAction && f.Verb != "" {
		return f.Kind + ", " + f.Verb + "s"
	}
	return f.Kind
}
