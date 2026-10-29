package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// newBdbsCmd builds the bdbs command group, whose commands list, create, export and import the owner's bdbs; bare, it
// lists them.
func newBdbsCmd(flags *globalFlags, open localOpener) *cobra.Command {
	return newListGroup("bdbs", "List, create, update, export and import bdbs", newBdbsListCmd(flags, open),
		newBdbsCreateCmd(flags, open), newBdbsUpdateCmd(flags, open), newBdbsExportCmd(flags, open),
		newBdbsImportCmd(flags, open))
}

// newBdbsListCmd builds the bdbs list command, which prints the owner's bdbs with their role and table count, or
// with their tables under --json.
func newBdbsListCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your bdbs",
		Args:  usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLocal(cmd, flags, open, func(l *local) error {
				summaries, err := l.bdbs.List(cmd.Context())
				if err != nil {
					return err
				}
				if asJSON {
					return writeJSON(cmd.OutOrStdout(), toBdbSummariesJSON(summaries))
				}
				if len(summaries) == 0 {
					cmd.PrintErrf("No bdbs yet. Create one with `%s bdbs create <name>`.\n", cmd.Root().Name())
					return nil
				}

				rows := make([][]string, len(summaries))
				for i, s := range summaries {
					tables := strconv.Itoa(len(s.Tables))
					if s.TablesErr != nil {
						tables = "?"
						cmd.PrintErrf("Warning: reading the tables of %s: %v\n", s.Name, s.TablesErr)
					}
					rows[i] = []string{s.Name, s.ID.String(), s.Role, tables}
				}
				return writeTable(cmd.OutOrStdout(), []string{"NAME", "ID", "ROLE", "TABLES"}, rows)
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the bdbs as JSON, each with its tables")
	return cmd
}

// newBdbsCreateCmd builds the bdbs create command, which creates a bdb owned by the owner, blank or seeded from a
// starter, and prints it.
func newBdbsCreateCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var description, starter string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "create [<name>] [--description <text>] [--starter <key>]",
		Short: "Create a bdb, blank or from a starter",
		Args:  usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) == 1 {
				name = args[0]
			}
			if name == "" && starter == "" {
				return usageError{err: errors.New("a name is required for a blank bdb; with --starter it defaults " +
					"to the starter's name")}
			}
			return runLocal(cmd, flags, open, func(l *local) error {
				created, err := l.bdbs.Create(cmd.Context(), name, description, starter)
				if errors.Is(err, domain.ErrStarterNotFound) {
					return fmt.Errorf("%w; `%s starters list` shows the keys", err, cmd.Root().Name())
				}
				if errors.Is(err, domain.ErrBdbNameTaken) {
					return fmt.Errorf("%w; `%s bdbs` lists them", err, cmd.Root().Name())
				}
				if err != nil {
					return err
				}
				if asJSON {
					return writeJSON(cmd.OutOrStdout(), toCreatedBdbJSON(created))
				}
				if created.ContentsErr != nil {
					cmd.PrintErrf("Warning: reading back what %s holds: %v\n", created.Name, created.ContentsErr)
				}
				_, err = io.WriteString(cmd.OutOrStdout(), createdLine(created))
				return err
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&description, "description", "", "what the bdb is for")
	f.StringVar(&starter, "starter", "", "the key of a starter to seed the bdb from (see starters list)")
	f.BoolVar(&asJSON, "json", false, "print the new bdb as JSON")
	return cmd
}

// newBdbsUpdateCmd builds the bdbs update command, which renames a bdb, changes its description, or both.
func newBdbsUpdateCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var name, description string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "update <bdb> [--name <name>] [--description <text>]",
		Short: "Rename a bdb or change its description",
		Long: "update renames a bdb, changes its description, or both: only what its flags give changes, and only " +
			"the bdb's owner may. " + bdbArgHelp,
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			var newName, newDescription *string
			if cmd.Flags().Changed("name") {
				newName = &name
			}
			if cmd.Flags().Changed("description") {
				newDescription = &description
			}
			if newName == nil && newDescription == nil {
				return usageError{err: errors.New("nothing to change: give --name, --description or both")}
			}
			return runLocal(cmd, flags, open, func(l *local) error {
				updated, err := l.bdbs.Update(cmd.Context(), args[0], newName, newDescription)
				if errors.Is(err, domain.ErrBdbNameTaken) {
					return fmt.Errorf("%w; `%s bdbs` lists them", err, cmd.Root().Name())
				}
				if err != nil {
					return err
				}
				if asJSON {
					return writeJSON(cmd.OutOrStdout(), toBdbJSON(updated))
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Updated %s (%s)\n", updated.Name, updated.ID)
				return err
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "a new name, unique among your bdbs whatever the case")
	f.StringVar(&description, "description", "", "a new description of what the bdb is for")
	f.BoolVar(&asJSON, "json", false, "print the changed bdb as JSON")
	return cmd
}

// createdLine returns the line bdbs create prints: the new bdb's name and ID, and for a bdb seeded from a starter whose
// contents were read back, the starter and how many tables and formulas it set up.
func createdLine(c domain.CreatedBdb) string {
	line := fmt.Sprintf("Created %s (%s)", c.Name, c.ID)
	if c.StarterKey != "" && c.ContentsErr == nil {
		line += fmt.Sprintf(" from %s: %s, %s", c.StarterKey, countOf(len(c.Tables), "table"),
			countOf(len(c.Formulas), "formula"))
	}
	return line + "\n"
}

// newBdbsExportCmd builds the bdbs export command, which writes a copy of a bdb's database to a file, encrypted under a
// one-time passphrase it prints or writes to --passphrase-file.
func newBdbsExportCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var out, passphraseFile string
	var force, asJSON bool
	cmd := &cobra.Command{
		Use:   "export <bdb> --out <file> [--passphrase-file <file>] [--force]",
		Short: "Export a bdb's database to an encrypted SQLite file",
		Long: "export writes a consistent copy of a bdb's database to --out: a SQLite file encrypted with SQLCipher " +
			"under a one-time passphrase, which is printed, or written to --passphrase-file. Nothing keeps the " +
			"passphrase: without it the file cannot be opened. " + bdbArgHelp,
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			if out == "" {
				return usageError{err: errors.New("give the file to write with --out")}
			}
			for _, path := range []string{out, passphraseFile} {
				if path == "" || force {
					continue
				}
				if _, err := os.Stat(path); err == nil {
					return fmt.Errorf("%s exists; pass --force to overwrite it", path)
				} else if !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			}
			return runLocal(cmd, flags, open, func(l *local) error {
				b, exported, err := exportTo(cmd, l, args[0], out)
				if err != nil {
					return err
				}
				if passphraseFile != "" {
					if err := os.WriteFile(passphraseFile, []byte(exported.Passphrase+"\n"), 0o600); err != nil {
						return fmt.Errorf("writing the passphrase (the export is in %s): %w", out, err)
					}
					exported.Passphrase = ""
				}
				if asJSON {
					return writeJSON(cmd.OutOrStdout(), exportJSON{
						Path: out, SizeBytes: exported.SizeBytes, Passphrase: exported.Passphrase,
						PassphraseFile: passphraseFile,
					})
				}
				line := fmt.Sprintf("Exported %s to %s (%d bytes)", b.Name, out, exported.SizeBytes)
				if passphraseFile != "" {
					line += "; passphrase in " + passphraseFile
				} else {
					line += "; passphrase: " + exported.Passphrase
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), line)
				return err
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&out, "out", "", "the file to write the export to")
	f.StringVar(&passphraseFile, "passphrase-file", "", "write the passphrase to this file (owner-only) instead of "+
		"printing it")
	f.BoolVar(&force, "force", false, "overwrite --out and --passphrase-file if they exist")
	f.BoolVar(&asJSON, "json", false, "print the result as JSON")
	return cmd
}

// exportTo exports the bdb ref names into a temporary owner-only file next to out, then renames it to out, so out is
// never left half written.
func exportTo(cmd *cobra.Command, l *local, ref, out string) (b domain.Bdb, exported domain.Export, err error) {
	tmp, err := os.CreateTemp(filepath.Dir(out), "."+filepath.Base(out)+".*")
	if err != nil {
		return domain.Bdb{}, domain.Export{}, fmt.Errorf("creating the export file: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(tmp.Name()))
		}
	}()
	b, exported, err = l.exports.Export(cmd.Context(), ref, tmp)
	if cerr := tmp.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("writing the export file: %w", cerr)
	}
	if err != nil {
		return b, domain.Export{}, err
	}
	if err := os.Rename(tmp.Name(), out); err != nil {
		return b, domain.Export{}, fmt.Errorf("moving the export to %s: %w", out, err)
	}
	return b, exported, nil
}

// newBdbsImportCmd builds the bdbs import command, which creates a bdb from a file bdbs export wrote, encrypted or
// decrypted.
func newBdbsImportCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var name, passphraseFile string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "import <file> [--name <name>] [--passphrase-file <file>]",
		Short: "Create a bdb from a file bdbs export wrote",
		Long: "import creates a bdb from <file>, a file `bdb bdbs export` wrote: its tables and rows, formulas, " +
			"skills and change history. The bdb is named --name, or after the file without its extension. An " +
			"encrypted export opens with its passphrase, read from the first line of --passphrase-file or asked in a " +
			"terminal; a decrypted copy needs none.",
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			path := args[0]
			if name == "" {
				name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			}
			var passphrase string
			if passphraseFile != "" {
				if passphrase, err = readPassphraseFile(passphraseFile); err != nil {
					return err
				}
			}
			f, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("opening the file to import: %w", err)
			}
			defer func() {
				if cerr := f.Close(); cerr != nil {
					err = errors.Join(err, fmt.Errorf("closing %s: %w", path, cerr))
				}
			}()

			return runLocal(cmd, flags, open, func(l *local) error {
				b, err := importBdb(cmd, l, f, name, passphrase, passphraseFile == "")
				if errors.Is(err, domain.ErrBdbNameTaken) {
					return fmt.Errorf("%w; `%s bdbs` lists them, and --name names this one", err, cmd.Root().Name())
				}
				if err != nil {
					return err
				}
				if asJSON {
					return writeJSON(cmd.OutOrStdout(), toBdbJSON(b))
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Imported %s as %s (%s)\n", path, b.Name, b.ID)
				return err
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "the new bdb's name (default: the file's name without its extension)")
	f.StringVar(&passphraseFile, "passphrase-file", "", "read the export's passphrase from this file's first line")
	f.BoolVar(&asJSON, "json", false, "print the new bdb as JSON")
	return cmd
}

// importBdb imports src as a new bdb named name, under passphrase. When mayAsk is set and cmd's stdin is a terminal,
// the passphrase of an encrypted file is asked there, missing or wrong, up to passwordTries times.
func importBdb(
	cmd *cobra.Command, l *local, src io.ReadSeeker, name, passphrase string, mayAsk bool,
) (domain.Bdb, error) {
	p := newPrompter(cmd.Context(), cmd.InOrStdin(), cmd.ErrOrStderr())
	for try := 0; ; try++ {
		b, err := l.imports.Import(cmd.Context(), src, name, passphrase)
		asked := mayAsk && p.interactive() && try < passwordTries
		switch {
		case asked && errors.Is(err, domain.ErrPassphraseNeeded):
		case asked && try > 0 && errors.Is(err, domain.ErrImportUnreadable):
			cmd.PrintErrln("That passphrase doesn't open it. Try again.")
		default:
			return b, err
		}
		if passphrase, err = p.askPassword("Passphrase"); err != nil {
			return domain.Bdb{}, err
		}
	}
}

// readPassphraseFile returns the first line of the file at path, without its line ending. An empty one is a usage
// error.
func readPassphraseFile(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading the passphrase: %w", err)
	}
	passphrase, _, _ := strings.Cut(string(content), "\n")
	if passphrase = strings.TrimSuffix(passphrase, "\r"); passphrase == "" {
		return "", usageError{err: fmt.Errorf("--passphrase-file %s holds no passphrase on its first line", path)}
	}
	return passphrase, nil
}
