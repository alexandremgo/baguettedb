package cli

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	authsqlitemigrations "github.com/alexandremgo/baguettedb/pkg/auth/migrations/sqlite"
	bdbsqlitemigrations "github.com/alexandremgo/baguettedb/pkg/bdbs/migrations/sqlite"
	"github.com/alexandremgo/baguettedb/pkg/cli/adapters/datadir"
	"github.com/alexandremgo/baguettedb/pkg/cli/adapters/instancestore"
	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlitelocal"
	dbsqlitemigrations "github.com/alexandremgo/baguettedb/pkg/databases/migrations/sqlite"
	dbservice "github.com/alexandremgo/baguettedb/pkg/databases/service"
	"github.com/alexandremgo/baguettedb/pkg/webui"
)

// newInfoCmd builds the info command, which reports the binary, the data directory a command would use and how it
// was chosen, the instance it holds and the server running on it, without creating or changing anything.
func newInfoCmd(flags *globalFlags) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "info [--json]",
		Short: "Show the binary, the data directory in use and what it holds, changing nothing",
		Long: "info reports the binary (its version, platform, encryption library and whether it serves the web " +
			"app), the data directory commands use and why (--data-dir, $" + dataDirEnv + " or the default), the " +
			"key, instance store and bdb files in it, the owner and the account --as acts as, the bdbs and " +
			"accounts, the schema versions, the bdb serve running on it, and the name an MCP client registers it " +
			"under.\n\n" +
			"Unlike every other command, it creates nothing: a data directory that doesn't exist yet is reported as " +
			"such, and the instance is read without being upgraded.",
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, _ []string) error {
			info, err := gatherInfo(cmd.Context(), cmd.Root().Name(), flags)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), toInfoJSON(info))
			}
			return writeInfo(cmd.OutOrStdout(), info)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	return cmd
}

// gatherInfo collects what info reports for the binary named name and the data directory flags choose, reading files
// only.
func gatherInfo(ctx context.Context, name string, flags *globalFlags) (domain.Info, error) {
	dataDir, err := flags.resolveDataDir()
	if err != nil {
		return domain.Info{}, err
	}
	info := domain.Info{As: flags.as, MCPName: domain.MCPServerKey(domain.MCPClientClaude, flags.as)}

	cipherVersion, cipherProvider, err := sqlitelocal.Cipher(ctx)
	if err != nil {
		return domain.Info{}, err
	}
	info.Binary = domain.BinaryInfo{
		Name: name, Version: version, OS: runtime.GOOS, Arch: runtime.GOARCH,
		CipherVersion: cipherVersion, CipherProvider: cipherProvider, WebApp: webui.Embedded(),
	}

	if info.DataDir, err = dataDirInfo(dataDir, flags); err != nil {
		return domain.Info{}, err
	}
	if !info.DataDir.Exists {
		return info, nil
	}

	if info.DataDir.InstanceStore.Exists {
		apps, err := appSchemas()
		if err != nil {
			return domain.Info{}, err
		}
		summary, err := instancestore.Read(ctx, info.DataDir.InstanceStore.Path, flags.as, apps)
		if err != nil {
			return domain.Info{}, err
		}
		summary.PlatformSchema = dbservice.CurrentPlatformSchemaVersion
		info.Instance = &summary
	}

	served, ok, err := datadir.NewServedFile(dataDir).Current()
	if err != nil {
		return domain.Info{}, err
	}
	if ok {
		info.Server = &served
	}
	return info, nil
}

// dataDirInfo describes dataDir, chosen by flags, as datadir.Inspect does, with how it was chosen and the environment
// variables that place its files elsewhere.
func dataDirInfo(dataDir string, flags *globalFlags) (domain.DataDirInfo, error) {
	cfg, err := config.Load()
	if err != nil {
		return domain.DataDirInfo{}, fmt.Errorf("loading config: %w", err)
	}
	applyDataDir(&cfg, dataDir, os.LookupEnv)
	d, err := datadir.Inspect(dataDir, cfg.DatabasesKEKFile, cfg.InstanceSQLitePath, cfg.DatabasesDataDir)
	if err != nil {
		return domain.DataDirInfo{}, err
	}
	d.Source = dataDirSource(flags)
	d.Key.Override = envOverride("BAGUETTEDB_DATABASES_KEK_FILE")
	d.InstanceStore.Override = envOverride("BAGUETTEDB_INSTANCE_SQLITE_PATH")
	d.Databases.Override = envOverride("BAGUETTEDB_DATABASES_DATA_DIR")
	return d, nil
}

// dataDirSource returns how flags choose the data directory, as resolveDataDir does.
func dataDirSource(flags *globalFlags) string {
	switch {
	case flags.dataDir != "":
		return domain.DataDirFromFlag
	case os.Getenv(dataDirEnv) != "":
		return domain.DataDirFromEnv
	default:
		return domain.DataDirFromDefault
	}
}

// envOverride returns env when the environment sets it, and an empty string otherwise.
func envOverride(env string) string {
	if v, ok := os.LookupEnv(env); ok && v != "" {
		return env
	}
	return ""
}

// appSchemas returns the apps whose tables the instance store holds, each with the version this binary migrates it
// to.
func appSchemas() ([]domain.AppSchema, error) {
	apps := []struct {
		name string
		fsys fs.FS
	}{
		{"auth", authsqlitemigrations.FS}, {"bdbs", bdbsqlitemigrations.FS}, {"databases", dbsqlitemigrations.FS},
	}
	schemas := make([]domain.AppSchema, 0, len(apps))
	for _, app := range apps {
		latest, err := database.LatestMigrationVersion(app.fsys)
		if err != nil {
			return nil, fmt.Errorf("reading the %s migrations: %w", app.name, err)
		}
		schemas = append(schemas, domain.AppSchema{App: app.name, Latest: latest})
	}
	return schemas, nil
}

// writeInfo writes info to w as labelled lines.
func writeInfo(w io.Writer, info domain.Info) error {
	b := info.Binary
	webApp := "with the web app"
	if !b.WebApp {
		webApp = "without the web app (API and MCP only)"
	}
	header := fmt.Sprintf("%s %s on %s/%s, SQLCipher %s with %s, %s\n\n", b.Name, b.Version, b.OS, b.Arch,
		b.CipherVersion, b.CipherProvider, webApp)
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}

	d := info.DataDir
	rows := [][]string{{"Data directory", d.Path + " " + dataDirSourceText(d.Source)}}
	if !d.Exists {
		rows[0][1] += ": not created yet; `" + b.Name + " init` creates it"
		return writeRows(w, rows)
	}
	keyText := fileText(d.Key, "")
	if d.Key.Exists {
		if d.KeyOwnerOnly {
			keyText += ", owner-only"
		} else {
			keyText += ", readable by others: chmod 600 " + d.Key.Path
		}
	}
	rows = append(rows,
		[]string{"Key", keyText},
		[]string{"Instance store", fileText(d.InstanceStore, humanBytes(d.InstanceStoreBytes))},
		[]string{"Bdb files", fileText(d.Databases, countOf(d.BdbFiles, "file")+", "+humanBytes(d.BdbBytes))},
	)

	if in := info.Instance; in != nil {
		owner := "none yet: `" + b.Name + " init` sets it"
		if in.Owner != nil {
			owner = strings.TrimSpace(fullName(*in.Owner) + " <" + in.Owner.Email + ">")
		}
		rows = append(rows, []string{"Owner", owner})
		if info.As != "" {
			as := info.As + " (--as)"
			if !in.AsFound {
				as += ", no such account: `" + b.Name + " users` lists them"
			}
			rows = append(rows, []string{"Acting as", as})
		}
		rows = append(rows,
			[]string{"Contents", countOf(in.Bdbs, "bdb") + ", " + countOf(in.Accounts, "account")},
			[]string{"Schemas", schemasText(in, b.Name)},
		)
	}

	server := "none running"
	if info.Server != nil {
		server = fmt.Sprintf("%s serve on %s (pid %d)", b.Name, info.Server.URL, info.Server.PID)
	}
	rows = append(rows, []string{"Server", server}, []string{"MCP name", info.MCPName})
	return writeRows(w, rows)
}

// writeRows writes label and value rows as aligned columns, with no header.
func writeRows(w io.Writer, rows [][]string) error {
	var table strings.Builder
	if err := writeTable(&table, rows[0], rows[1:]); err != nil {
		return err
	}
	_, err := io.WriteString(w, table.String())
	return err
}

// dataDirSourceText says, in parentheses, how the data directory was chosen.
func dataDirSourceText(source string) string {
	switch source {
	case domain.DataDirFromFlag:
		return "(from --data-dir)"
	case domain.DataDirFromEnv:
		return "(from $" + dataDirEnv + ")"
	default:
		return "(the default; --data-dir or $" + dataDirEnv + " choose another)"
	}
}

// fileText describes f: its path, the variable that placed it, and detail when it exists or that it is missing.
func fileText(f domain.DataFile, detail string) string {
	text := f.Path
	if f.Override != "" {
		text += " (from $" + f.Override + ")"
	}
	switch {
	case !f.Exists:
		return text + ", missing"
	case detail != "":
		return text + ", " + detail
	default:
		return text
	}
}

// schemasText describes the instance store's schema versions against this binary's, and the bdbs' platform schema.
func schemasText(in *domain.InstanceSummary, name string) string {
	parts := make([]string, 0, len(in.Schemas))
	upToDate := true
	for _, s := range in.Schemas {
		part := fmt.Sprintf("%s %d", s.App, s.Version)
		switch {
		case s.Newer():
			part += fmt.Sprintf(" (newer than this %s's %d: upgrade %s)", name, s.Latest, name)
			upToDate = false
		case s.Version < s.Latest:
			part += fmt.Sprintf(" (upgraded to %d on the next command)", s.Latest)
			upToDate = false
		}
		if s.Dirty {
			part += " (a migration stopped halfway)"
			upToDate = false
		}
		parts = append(parts, part)
	}
	text := strings.Join(parts, ", ")
	if upToDate {
		text += " (up to date)"
	}
	return text + fmt.Sprintf("; this %s keeps bdbs at platform schema %d", name, in.PlatformSchema)
}

// humanBytes returns n bytes in the largest unit that keeps a number of at least one, in powers of 1000.
func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}
