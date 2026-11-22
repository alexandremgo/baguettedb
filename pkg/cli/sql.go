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
//
// A query and a data command also take the values of the SQL's named placeholders, with --param, --params or
// --params-file; a schema command takes none.
func newSQLRunCmd(name, short string, intent domain.Intent, flags *globalFlags, open localOpener) *cobra.Command {
	takesParams := intent != domain.IntentSchema
	params := paramFlag{}
	var file, paramsJSON, paramsFile string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   name + " <bdb> [<sql> | --file <path>]",
		Short: short,
		Long:  short + ". " + bdbArgHelp + " The SQL is the second argument, or the content of --file (- reads stdin).",
		Args:  usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "-" && paramsFile == "-" {
				return usageError{err: errors.New("the SQL and the parameters cannot both come from stdin")}
			}
			sql, err := readSQL(cmd.InOrStdin(), args[1:], file)
			if err != nil {
				return err
			}
			var spec []byte
			if takesParams {
				if spec, err = readParamsSpec(cmd, paramsJSON, paramsFile); err != nil {
					return err
				}
			}
			return runLocal(cmd, flags, open, func(l *local) error {
				var b domain.Bdb
				var result domain.QueryResult
				var err error
				if spec != nil {
					b, result, err = l.sql.RunJSON(cmd.Context(), args[0], sql, intent, spec)
				} else {
					b, result, err = l.sql.Run(cmd.Context(), args[0], sql, intent, params)
				}
				if refused, ok := errors.AsType[*domain.StatementKindError](err); ok {
					return fmt.Errorf("in %s: %s", b.Name, statementKindMessage(cmd.Root().Name(), refused))
				}
				if missing, ok := errors.AsType[*domain.ParametersError](err); ok && takesParams {
					return fmt.Errorf("in %s: %s but no values for them; give each one with --param name=value, or "+
						"all of them as a JSON object with --params", b.Name, missing.Error())
				}
				if missing, ok := errors.AsType[*domain.ParametersError](err); ok {
					return fmt.Errorf("in %s: %s, which sql %s doesn't take; write the values in the SQL", b.Name,
						missing.Error(), name)
				}
				if err != nil {
					return err
				}
				return printQueryResult(cmd, result, intent, asJSON)
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "read the SQL from this file, or from stdin with -")
	if takesParams {
		cmd.Use = name + " <bdb> [<sql> | --file <path>] [--param name=value]... [--params <json>] " +
			"[--params-file <path>]"
		cmd.Long += " To keep a value out of the SQL, write a named placeholder (:name) and give its value with " +
			"--param name=value: it binds as text, exactly as given, with no quote to double. --params gives " +
			"every value as one JSON object instead, each typed as JSON types it (a number, true or false, null, " +
			"or a list or object bound as its JSON text); --params-file reads that object from a file, or from " +
			"stdin with -. Give only one of the three."
		cmd.Example = "  bdb sql " + name + " Bakery " + sqlParamExample(intent)
		cmd.Flags().Var(params, "param", "the value of a named placeholder of the SQL, as text; repeated for each one")
		cmd.Flags().StringVar(&paramsJSON, "params", "", "the value of every placeholder as one JSON object")
		cmd.Flags().StringVar(&paramsFile, "params-file", "",
			"read the value of every placeholder as one JSON object from this file, or from stdin with -")
	}
	jsonHelp := "print the result as a JSON object: the rows changed, and any rows RETURNING gives"
	if intent == domain.IntentQuery {
		jsonHelp = "print the rows as a JSON array, each row an object keyed by column"
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, jsonHelp)
	return cmd
}

// sqlParamExample returns the end of an example command line running a statement of the kind intent with values.
func sqlParamExample(intent domain.Intent) string {
	if intent == domain.IntentQuery {
		return "\"SELECT name FROM loaves WHERE flour = :flour\" --param flour=rye\n" +
			"  bdb sql query Bakery \"SELECT name FROM loaves WHERE grams > :grams\" --params '{\"grams\": 500}'"
	}
	return "\"UPDATE loaves SET note = :note WHERE id = :id\" --param id=3 --param note=\"Baker's choice\"\n" +
		"  bdb sql data Bakery \"INSERT INTO loaves (name, grams) VALUES (:name, :grams)\" " +
		"--params '{\"name\": \"Rye\", \"grams\": 800}'"
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
