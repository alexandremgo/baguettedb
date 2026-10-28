package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// bdbArgHelp describes the <bdb> argument the commands working in one bdb take.
const bdbArgHelp = "<bdb> is the bdb's name (any case) or a prefix of its ID."

// newSQLCmd builds the sql command group, one subcommand per kind of statement.
func newSQLCmd(flags *globalFlags, open localOpener) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sql",
		Short: "Run SQL in a bdb: query, data or schema",
		Long: "sql runs SQL in a bdb, one subcommand per kind of statement: query reads, data changes rows, schema " +
			"changes tables. Each refuses a statement of another kind, so a query never changes anything.",
		Args: usageArgs(noArgsSuggesting),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(
		newSQLRunCmd("query", "Run a read query and print its rows", domain.IntentQuery, flags, open),
		newSQLRunCmd("data", "Insert, update or delete rows", domain.IntentData, flags, open),
		newSQLRunCmd("schema", "Create, alter or drop tables, views, indexes or triggers", domain.IntentSchema,
			flags, open),
	)
	return cmd
}

// newSQLRunCmd builds a command named name that runs one statement of the kind intent in a bdb, the SQL given as an
// argument or read from --file (- for stdin).
func newSQLRunCmd(name, short string, intent domain.Intent, flags *globalFlags, open localOpener) *cobra.Command {
	var file string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   name + " <bdb> [<sql> | --file <path>]",
		Short: short,
		Long:  short + ". " + bdbArgHelp + " The SQL is the second argument, or the content of --file (- reads stdin).",
		Args:  usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			sql, err := readSQL(cmd.InOrStdin(), args[1:], file)
			if err != nil {
				return err
			}
			return runLocal(cmd, flags, open, func(l *local) error {
				b, result, err := l.sql.Run(cmd.Context(), args[0], sql, intent)
				if refused, ok := errors.AsType[*domain.StatementKindError](err); ok {
					return fmt.Errorf("in %s: %s", b.Name, statementKindMessage(cmd.Root().Name(), refused))
				}
				if params, ok := errors.AsType[*domain.ParametersError](err); ok {
					return fmt.Errorf("in %s: %v, which sql commands don't take; write the values in the SQL, or save "+
						"it as a formula and run it with --param", b.Name, params)
				}
				if err != nil {
					return err
				}
				return printQueryResult(cmd, result, intent, asJSON)
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "read the SQL from this file, or from stdin with -")
	jsonHelp := "print the result as a JSON object: the rows changed, and any rows RETURNING gives"
	if intent == domain.IntentQuery {
		jsonHelp = "print the rows as a JSON array, each row an object keyed by column"
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, jsonHelp)
	return cmd
}

// readSQL returns the SQL from args (at most one) or from file, stdin when file is -. Neither or both, and SQL that
// is only blank, are usage errors.
func readSQL(stdin io.Reader, args []string, file string) (string, error) {
	var sql string
	switch {
	case len(args) == 1 && file != "":
		return "", usageError{err: errors.New("give the SQL as an argument or with --file, not both")}
	case len(args) == 1:
		sql = args[0]
	case file == "-":
		b, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("reading the SQL from stdin: %w", err)
		}
		sql = string(b)
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("reading the SQL: %w", err)
		}
		sql = string(b)
	default:
		return "", usageError{err: errors.New("give the SQL as an argument or with --file")}
	}
	if strings.TrimSpace(sql) == "" {
		return "", usageError{err: errors.New("the SQL is empty")}
	}
	return sql, nil
}

// printQueryResult prints the result of a statement of the kind intent. Under asJSON, a query's rows print as a JSON
// array and a change's result as a JSON object. Otherwise a query's rows print as a table with the row count on
// stderr, a data change prints any RETURNING rows as a table then its count of changed rows, and a schema change its
// confirmation; both changes name their batch, the ID bdb revert takes.
func printQueryResult(cmd *cobra.Command, r domain.QueryResult, intent domain.Intent, asJSON bool) error {
	out := cmd.OutOrStdout()
	if asJSON && intent == domain.IntentQuery {
		rows, err := toRowsJSON(r)
		if err != nil {
			return err
		}
		return writeJSON(out, rows)
	}
	if asJSON {
		doc, err := toChangeResultJSON(r)
		if err != nil {
			return fmt.Errorf("the change was applied, but %w", err)
		}
		return writeJSON(out, doc)
	}

	if len(r.Columns) > 0 {
		header := make([]string, len(r.Columns))
		for i, c := range r.Columns {
			header[i] = c.Name
		}
		rows := make([][]string, len(r.Rows))
		for i, values := range r.Rows {
			rows[i] = make([]string, len(values))
			for j, v := range values {
				rows[i][j] = tableCell(v)
			}
		}
		if err := writeTable(out, header, rows); err != nil {
			return err
		}
		cmd.PrintErrf("(%s)\n", countOf(len(r.Rows), "row"))
	}

	var line string
	switch intent {
	case domain.IntentData:
		line = countOf(int(r.AffectedRowCount), "row") + " changed"
		if r.LastInsertRowID != nil {
			line += fmt.Sprintf(", last inserted rowid %d", *r.LastInsertRowID)
		}
	case domain.IntentSchema:
		line = "Schema updated"
	default:
		return nil
	}
	if r.BatchID != "" {
		line += " (batch " + r.BatchID + ")"
	}
	_, err := fmt.Fprintln(out, line)
	return err
}

// statementKindMessage returns what to tell someone whose SQL was refused, naming the subcommand of the binary name
// that runs the refused statement.
func statementKindMessage(name string, refused *domain.StatementKindError) string {
	if refused.Count > 1 {
		return fmt.Sprintf("`%s sql query` runs a single statement; got %d", name, refused.Count)
	}
	subject := "the statement"
	if refused.Statement > 0 {
		subject = fmt.Sprintf("statement %d", refused.Statement)
	}
	var kind, command string
	switch refused.Kind {
	case domain.IntentQuery:
		kind, command = "a read", "query"
	case domain.IntentData:
		kind, command = "a row change", "data"
	default:
		kind, command = "a schema change", "schema"
	}
	return fmt.Sprintf("%s is %s; run it with `%s sql %s`", subject, kind, name, command)
}
