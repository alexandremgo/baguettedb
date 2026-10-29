package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// dataDirEnv overrides the default data directory; the --data-dir flag overrides both.
const dataDirEnv = "BAGUETTEDB_DATA_DIR"

// globalFlags holds the flags every command accepts.
type globalFlags struct {
	dataDir  string
	logLevel string

	// as is the email of the account commands act as, the owner's when empty.
	as string

	// dataDirFlag is the --data-dir flag, to tell it given empty from left out.
	dataDirFlag *pflag.Flag
}

// newRootCmd builds the command tree under a root named name. argv0 is the command line's program name, as run.
func newRootCmd(name, argv0 string) *cobra.Command {
	flags := &globalFlags{}
	root := &cobra.Command{
		Use:   name,
		Short: "A private BaguetteDB on this machine, for your AI agents",
		Long: name + " runs a BaguetteDB instance on this machine, with no server, login or Docker: " +
			"its data lives in a directory of your own (~/.baguettedb by default).",
		Version:       version,
		Args:          usageArgs(noArgsSuggesting),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.SetVersionTemplate("{{.Name}} {{.Version}}\n")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageError{err: err}
	})

	pf := root.PersistentFlags()
	pf.StringVar(&flags.dataDir, "data-dir", "",
		"directory holding the instance (default $"+dataDirEnv+", or ~/.baguettedb)")
	flags.dataDirFlag = pf.Lookup("data-dir")
	pf.StringVar(&flags.logLevel, "log-level", zerolog.LevelWarnValue,
		"minimum level of the logs written to stderr (debug, info, warn, error)")
	pf.StringVar(&flags.as, "as", "", "act as the account with this email instead of the owner, you")

	mcpCmd := newMCPCmd(flags, openStdio)
	mcpCmd.AddCommand(newMCPConfigCmd(flags, argv0))
	root.AddCommand(
		newVersionCmd(), newInfoCmd(flags), mcpCmd, newBdbsCmd(flags, openLocal), newStartersCmd(flags, openLocal),
		newSQLCmd(flags, openLocal), newChangesCmd(flags, openLocal), newFormulasCmd(flags, openLocal),
		newSkillsCmd(flags, openLocal),
		newUsersCmd(flags, openLocal), newTokensCmd(flags, openLocal), newServeCmd(flags, openLocal),
		newInitCmd(flags, openLocal),
		newSQLRunCmd("q", "Run a read query and print its rows (short for sql query)", domain.IntentQuery, flags,
			openLocal),
	)
	return root
}

// newListGroup builds the command group use, whose subcommands are list then more. Bare, the group runs list, with
// list's arguments and flags, so its most common use takes no subcommand, and its usage line shows list's arguments;
// --help still prints the group's help. An argument naming a subcommand runs that subcommand; when list takes no
// arguments, any other is an unknown command.
func newListGroup(use, short string, list *cobra.Command, more ...*cobra.Command) *cobra.Command {
	args := list.Args
	if required, optional := usageArgNames(list.Use); len(required)+len(optional) == 0 {
		args = usageArgs(noArgsSuggesting)
	}
	cmd := &cobra.Command{
		Use:   strings.Join(append([]string{use}, strings.Fields(list.Use)[1:]...), " "),
		Short: short,
		Long:  short + ". With no subcommand, " + use + " runs " + use + " list.",
		Args:  args,
		RunE:  list.RunE,
	}
	cmd.Flags().AddFlagSet(list.Flags())
	cmd.AddCommand(append([]*cobra.Command{list}, more...)...)
	return cmd
}

// resolveDataDir returns the data directory: the --data-dir flag, else $BAGUETTEDB_DATA_DIR, else ~/.baguettedb. The
// result is absolute, so it reads the same in logs and in the instructions sent to agents whatever the working
// directory. A --data-dir given empty is a usage error rather than the default, which the caller didn't ask for.
//
// A leading ~ or ~/ stands for the home directory, as a shell expands it, since an MCP client's configuration passes
// the environment and arguments unexpanded.
func (f *globalFlags) resolveDataDir() (string, error) {
	if f.dataDir == "" && f.dataDirFlag != nil && f.dataDirFlag.Changed {
		return "", usageError{err: fmt.Errorf("--data-dir is empty: give a directory, or leave the flag out for "+
			"$%s or ~/.baguettedb", dataDirEnv)}
	}
	dir := f.dataDir
	if dir == "" {
		dir = os.Getenv(dataDirEnv)
	}
	if dir == "" {
		dir = "~/.baguettedb"
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locating the home directory for the data directory %s: %w", dir, err)
		}
		dir = filepath.Join(home, dir[1:])
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolving data directory %q: %w", dir, err)
	}
	return abs, nil
}

// logger returns a logger writing to w at the --log-level level, in color only when w is a terminal, so logs
// redirected to a file hold plain text. An unknown level is a usage error.
func (f *globalFlags) logger(w io.Writer) (zerolog.Logger, error) {
	level, err := zerolog.ParseLevel(f.logLevel)
	if err != nil || level == zerolog.NoLevel {
		invalid := fmt.Errorf("invalid --log-level %q: want debug, info, warn or error", f.logLevel)
		return zerolog.Nop(), usageError{err: invalid}
	}
	out := zerolog.ConsoleWriter{Out: w, NoColor: !isTerminal(w)}
	return zerolog.New(out).Level(level).With().Timestamp().Logger(), nil
}

// isTerminal reports whether w is a file open on a terminal.
func isTerminal(w any) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
