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

// skillHelp says what a skill is, for the long help of the commands that write one.
const skillHelp = "A skill is markdown in the bdb that teaches agents how it is meant to be used: for the whole bdb, " +
	"or for some of its tables."

// newSkillsCmd builds the skills command group, whose commands list, show, create and update a bdb's skills; bare, it
// lists them.
func newSkillsCmd(flags *globalFlags, open localOpener) *cobra.Command {
	return newListGroup("skills", "List, show, create and update a bdb's skills",
		newSkillsListCmd(flags, open), newSkillsShowCmd(flags, open), newSkillsCreateCmd(flags, open),
		newSkillsUpdateCmd(flags, open))
}

// newSkillsListCmd builds the skills list command, which prints a bdb's skills.
func newSkillsListCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list <bdb>",
		Short: "List a bdb's skills",
		Long: "list prints a bdb's skills: each one's name, the tables it targets (bdb-level when it covers the " +
			"whole bdb) and its description; skills show prints one with its body. " + bdbArgHelp,
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLocal(cmd, flags, open, func(l *local) error {
				b, skills, err := l.skills.List(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				if asJSON {
					if skills == nil {
						skills = []apptypes.Skill{}
					}
					return writeJSON(cmd.OutOrStdout(), skills)
				}
				if len(skills) == 0 {
					cmd.PrintErrf("No skills in %s.\n", b.Name)
					return nil
				}
				rows := make([][]string, len(skills))
				for i, s := range skills {
					rows[i] = []string{s.Name, skillTables(s), s.Description}
				}
				return writeTable(cmd.OutOrStdout(), []string{"NAME", "TABLES", "DESCRIPTION"}, rows)
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the skills as JSON, with their bodies")
	return cmd
}

// newSkillsShowCmd builds the skills show command, which prints one of a bdb's skills with its body.
func newSkillsShowCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show <bdb> <skill>",
		Short: "Show a skill of a bdb",
		Long: "show prints a skill of a bdb, by name: its description, the tables it targets and its markdown body. " +
			bdbArgHelp,
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLocal(cmd, flags, open, func(l *local) error {
				b, s, err := l.skills.Show(cmd.Context(), args[0], args[1])
				if err != nil {
					return skillError(cmd, b, err)
				}
				if asJSON {
					return writeJSON(cmd.OutOrStdout(), s)
				}
				return printSkill(cmd.OutOrStdout(), s)
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the skill as JSON")
	return cmd
}

// newSkillsCreateCmd builds the skills create command, which creates a skill in a bdb, its body read from a file.
func newSkillsCreateCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var description, file string
	var tables []string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "create <bdb> <name> --description <text> [--table <table>]... --file <path>",
		Short: "Create a skill in a bdb",
		Long: "create creates a skill in a bdb. " + skillHelp + " Its name is slug-shaped and unique in the bdb, " +
			"and its markdown body is read from --file (- reads stdin). Without --table it covers the whole bdb; " +
			"each --table makes it target one of the bdb's tables. " + bdbArgHelp,
		Example: "  bdb skills create Kanban triage --description \"How to triage new tasks\" --table tasks " +
			"--file triage.md",
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			if description == "" {
				return usageError{err: errors.New("--description is required: one line on what the skill is for")}
			}
			body, err := readFileFlag(cmd.InOrStdin(), file, "skill's body")
			if err != nil {
				return err
			}
			return runLocal(cmd, flags, open, func(l *local) error {
				b, s, err := l.skills.Create(cmd.Context(), args[0], args[1], description, string(body), tables)
				if err != nil {
					return err
				}
				return printWrittenSkill(cmd, "Created", b, s, asJSON)
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&description, "description", "", "one line on what the skill is for")
	f.StringArrayVar(&tables, "table", nil, "a table the skill targets; repeat it for several")
	f.StringVar(&file, "file", "", "read the markdown body from this file, or from stdin with -")
	f.BoolVar(&asJSON, "json", false, "print the created skill as JSON")
	return cmd
}

// newSkillsUpdateCmd builds the skills update command, which changes the fields of a skill its flags give.
func newSkillsUpdateCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var name, description, file string
	var tables []string
	var noTables, asJSON bool
	cmd := &cobra.Command{
		Use: "update <bdb> <skill> [--name <name>] [--description <text>] [--table <table>]... [--no-tables] " +
			"[--file <path>]",
		Short: "Change a skill of a bdb",
		Long: "update changes a skill of a bdb, by name: only what its flags give changes. --table replaces the " +
			"tables the skill targets (repeat it for several), --no-tables makes it cover the whole bdb, and --file " +
			"replaces its markdown body (- reads stdin). " + bdbArgHelp,
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := cmd.Flags()
			var change domain.SkillChange
			if f.Changed("name") {
				change.Name = &name
			}
			if f.Changed("description") {
				change.Description = &description
			}
			switch {
			case f.Changed("table") && noTables:
				return usageError{err: errors.New("--table and --no-tables don't go together")}
			case f.Changed("table"):
				change.Tables = &tables
			case noTables:
				change.Tables = &[]string{}
			}
			if file != "" {
				body, err := readFileFlag(cmd.InOrStdin(), file, "skill's body")
				if err != nil {
					return err
				}
				text := string(body)
				change.Body = &text
			}
			if change.IsEmpty() {
				return usageError{err: errors.New("nothing to change: give --name, --description, --table, " +
					"--no-tables or --file")}
			}
			return runLocal(cmd, flags, open, func(l *local) error {
				b, s, err := l.skills.Update(cmd.Context(), args[0], args[1], change)
				if err != nil {
					return skillError(cmd, b, err)
				}
				return printWrittenSkill(cmd, "Updated", b, s, asJSON)
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "a new name, slug-shaped and unique in the bdb")
	f.StringVar(&description, "description", "", "a new one-line description")
	f.StringArrayVar(&tables, "table", nil, "a table the skill targets instead; repeat it for several")
	f.BoolVar(&noTables, "no-tables", false, "make the skill cover the whole bdb")
	f.StringVar(&file, "file", "", "read a new markdown body from this file, or from stdin with -")
	f.BoolVar(&asJSON, "json", false, "print the changed skill as JSON")
	return cmd
}

// skillError adds to err, when it reports an unknown skill of the bdb b, the command that lists b's skills.
func skillError(cmd *cobra.Command, b domain.Bdb, err error) error {
	if errors.Is(err, domain.ErrSkillNotFound) {
		return fmt.Errorf("%w; `%s skills %s` lists them", err, cmd.Root().Name(), b.Name)
	}
	return err
}

// printSkill writes the skill s to w: a heading with the tables it targets, its description, and its body.
func printSkill(w io.Writer, s apptypes.Skill) error {
	var out strings.Builder
	fmt.Fprintf(&out, "%s  (%s)\n", s.Name, skillScope(s))
	fmt.Fprintf(&out, "Description:  %s\n", s.Description)
	fmt.Fprintf(&out, "\n%s\n", strings.TrimSuffix(s.Body, "\n"))
	_, err := io.WriteString(w, out.String())
	return err
}

// printWrittenSkill prints the skill s a command just created or updated in the bdb b: as JSON under asJSON, or as one
// line naming it with the tables it targets, after verb.
func printWrittenSkill(cmd *cobra.Command, verb string, b domain.Bdb, s apptypes.Skill, asJSON bool) error {
	if asJSON {
		return writeJSON(cmd.OutOrStdout(), s)
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s skill %s in %s (%s)\n", verb, s.Name, b.Name, skillScope(s))
	return err
}

// skillTables lists the tables the skill s targets, or "bdb-level" when it covers the whole bdb.
func skillTables(s apptypes.Skill) string {
	if len(s.TargetTables) == 0 {
		return "bdb-level"
	}
	return strings.Join(s.TargetTables, ", ")
}

// skillScope words what the skill s covers: "bdb-level", or the tables it targets ("tables: tasks, lists").
func skillScope(s apptypes.Skill) string {
	if len(s.TargetTables) == 0 {
		return "bdb-level"
	}
	return "tables: " + skillTables(s)
}
