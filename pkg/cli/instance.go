package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	authmodule "github.com/alexandremgo/baguettedb/pkg/auth"
	"github.com/alexandremgo/baguettedb/pkg/cli/adapters/intraprocess"
	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
	"github.com/alexandremgo/baguettedb/pkg/cli/service"
	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
	dbmodule "github.com/alexandremgo/baguettedb/pkg/databases"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/filekek"
	"github.com/alexandremgo/baguettedb/pkg/instance"
	"github.com/alexandremgo/baguettedb/pkg/mcpgateway"
)

// serverName is the name the local instance reports to MCP clients, distinct from a hosted instance.
const serverName = "baguettedb-local"

// Messages data upgraded by a newer version of bdb is refused with. The process refusing is always the older one, so
// each tells the user how to run the newer version from where they meet the refusal: a one-shot command, an MCP client
// that started the server, or a server started by hand.
const (
	newerBdbFromCommand = "this bdb was upgraded by a newer version of bdb: update this binary"
	newerBdbOverStdio   = "this bdb was upgraded by a newer version of bdb than the one this server runs. Restart " +
		"this server to use the new version: reconnect it in your MCP client (in Claude Code, `/mcp`), or restart " +
		"the session that started it"
	newerBdbServed = "this bdb was upgraded by a newer version of bdb than the one this server runs: restart this " +
		"server to use the new version"
	newerDataDir = "this data directory was upgraded by a newer version of bdb: update this binary"
)

// servedTokenHint tells a client bdb serve refuses how to get a personal access token, as MCP clients otherwise look
// for an OAuth flow the server doesn't have.
const servedTokenHint = "create a personal access token with `bdb tokens create <token-name>` on the machine " +
	"running bdb serve, and send it as an `Authorization: Bearer` header"

// Paths inside the data directory.
const (
	instanceStoreFile = "instance.sqlite"
	databasesDir      = "databases"
	kekFile           = "kek"
)

// openInstance opens the local instance on dataDir: it creates the directory on first use (owner-only), derives the
// instance's settings from it, and composes every app in local mode, with the implicit owner and no HTTP
// authentication. Its MCP instructions say an agent acts as the account with email as, when as is not empty. A bdb
// upgraded by a newer version of bdb is refused with newerBdb. The caller closes the instance.
//
// A server that keeps the instance open passes guard, which then watches the files the instance opened and refuses
// every MCP tool call once the directory is replaced; nil, for a command that ends with its one call, watches none.
func openInstance(
	ctx context.Context, dataDir, as string, logger zerolog.Logger, newerBdb string, guard *dataDirGuard,
) (*instance.Instance, error) {
	cfg, err := localConfig(dataDir)
	if err != nil {
		return nil, err
	}
	opts := instance.Options{
		Databases:      dbmodule.SetupOptions{NewerPlatformSchemaMessage: newerBdb},
		Authentication: instance.InjectedAuth(refuseHTTP),
		ImplicitOwner:  true,
		Gateway:        gatewayOptions(localPreamble(dataDir, as)),
	}
	if guard != nil {
		opts.GatewayServerOptions = []mcpserver.ServerOption{mcpserver.WithToolHandlerMiddleware(guard.toolMiddleware)}
	}
	inst, err := newInstance(ctx, cfg, logger, opts)
	if err != nil {
		return nil, err
	}
	return inst, watchOpened(inst, guard, cfg)
}

// watchOpened has guard, when not nil, watch the key and instance store files inst opened as cfg names them; on
// failure it closes inst.
func watchOpened(inst *instance.Instance, guard *dataDirGuard, cfg config.Config) error {
	if guard == nil {
		return nil
	}
	if err := guard.watch(cfg.DatabasesKEKFile, cfg.InstanceSQLitePath); err != nil {
		inst.Close()
		return err
	}
	return nil
}

// openServedInstance opens the local instance on dataDir, as openInstance does, for serving over HTTP at publicURL:
// every request authenticates with a personal access token, as its bearer token or in a browser's cookie, and one
// whose Host is neither loopback, an IP address nor one of allowedHosts is refused. A browser loading a page through
// another loopback name than publicURL's is sent to publicURL, where its session cookie lives. The cookie is Secure
// when publicURL is https, unless BAGUETTEDB_SECURE_COOKIES says otherwise. The web app, invite links included, is at
// publicURL too, unless BAGUETTEDB_APP_BASE_URL says otherwise. The caller closes the instance.
//
// guard watches the files the instance opened, and every request is refused once the directory is replaced.
func openServedInstance(
	ctx context.Context, dataDir string, logger zerolog.Logger, publicURL string, allowedHosts []string,
	guard *dataDirGuard,
) (*instance.Instance, error) {
	cfg, err := localConfig(dataDir)
	if err != nil {
		return nil, err
	}
	base, err := url.Parse(publicURL)
	if err != nil {
		return nil, fmt.Errorf("parsing the URL served at: %w", err)
	}
	cfg.PublicURL = publicURL
	if v, ok := os.LookupEnv("BAGUETTEDB_SECURE_COOKIES"); !ok || v == "" {
		cfg.SecureCookies = new(strings.HasPrefix(publicURL, "https://"))
	}
	if v, ok := os.LookupEnv("BAGUETTEDB_APP_BASE_URL"); !ok || v == "" {
		cfg.AppBaseURL = publicURL
	}
	inst, err := newInstance(ctx, cfg, logger, instance.Options{
		Auth:      authmodule.SetupOptions{TokenHint: servedTokenHint, SessionCookieName: sessionCookieName(dataDir)},
		Databases: dbmodule.SetupOptions{NewerPlatformSchemaMessage: newerBdbServed},
		Middlewares: []func(http.Handler) http.Handler{
			httputil.RequireHost(allowedHosts...), guard.httpMiddleware, httputil.RedirectLoopbackAliases(base),
		},
		Authentication: instance.PersonalTokensOnly(),
		ImplicitOwner:  true,
		Gateway:        gatewayOptions(servedPreamble(dataDir)),
	})
	if err != nil {
		return nil, err
	}
	return inst, watchOpened(inst, guard, cfg)
}

// newInstance composes the local instance as instance.New does, refusing an instance store a newer version of bdb
// migrated with a message saying so.
func newInstance(
	ctx context.Context, cfg config.Config, logger zerolog.Logger, opts instance.Options,
) (*instance.Instance, error) {
	inst, err := instance.New(ctx, cfg, logger, opts)
	if errors.Is(err, database.ErrNewerSchema) {
		logger.Debug().Err(err).Msg("refusing the instance store")
		return nil, errors.New(newerDataDir)
	}
	return inst, err
}

// sessionCookieName returns the name of the cookie browsers keep their bdb serve session in for dataDir, an absolute
// path: one per data directory, since cookies ignore ports and two servers on one host name would otherwise overwrite
// each other's session.
func sessionCookieName(dataDir string) string {
	sum := sha256.Sum256([]byte(dataDir))
	return "bdb_session_" + hex.EncodeToString(sum[:4])
}

// localConfig returns the settings of the local instance on dataDir, creating the directory on first use
// (owner-only): the loaded config, with what applyDataDir derives from dataDir.
func localConfig(dataDir string) (config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.Config{}, fmt.Errorf("loading config: %w", err)
	}
	applyDataDir(&cfg, dataDir, os.LookupEnv)

	for _, dir := range []string{dataDir, cfg.DatabasesDataDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return config.Config{}, fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	return cfg, nil
}

// gatewayOptions returns how the local instance presents itself to MCP clients, preamble leading its instructions.
func gatewayOptions(preamble string) mcpgateway.Options {
	return mcpgateway.Options{
		ServerName:           serverName,
		ServerVersion:        version,
		InstructionsPreamble: preamble,
	}
}

// local is the local instance opened for one command: the services the commands run, acting as one account, the owner
// unless --as names another.
type local struct {
	bdbs     *service.BdbService
	starters *service.StarterService
	sql      *service.SQLService
	changes  *service.ChangeService
	formulas *service.FormulaService
	exports  *service.ExportService
	imports  *service.ImportService
	users    *service.UserService
	tokens   *service.TokenService
	owner    *service.OwnerService
	close    func()

	// email is the email of the account the commands act as.
	email string
}

// localOpener opens the local instance on dataDir, acting as the account with email as, or the owner when as is
// empty.
type localOpener func(ctx context.Context, dataDir, as string, logger zerolog.Logger) (*local, error)

// openLocal opens the local instance on dataDir, as openInstance does, and builds the command line's services over
// its apps in-process, acting as the account actAs returns for as, except the account services, which manage every
// account. The caller calls close.
func openLocal(ctx context.Context, dataDir, as string, logger zerolog.Logger) (*local, error) {
	inst, err := openInstance(ctx, dataDir, as, logger, newerBdbFromCommand, nil)
	if err != nil {
		return nil, err
	}
	auth := intraprocess.NewAuthApp(inst.Auth.UserSvc, inst.Auth.RoleSvc, inst.Auth.TokenSvc)
	acting, email, err := actAs(ctx, inst, auth, as)
	if err != nil {
		inst.Close()
		return nil, err
	}
	starters := intraprocess.NewStartersApp(inst.Starters, acting)
	databases := intraprocess.NewDatabasesApp(inst.Databases.QuerySvc, inst.Databases.FormulaSvc,
		inst.Databases.ExportSvc, inst.Databases.ImportSvc, acting)
	bdbsApp := intraprocess.NewBdbsApp(inst.Bdbs.BdbSvc, acting)
	bdbs := service.NewBdbService(bdbsApp, starters, databases, databases)
	return &local{
		bdbs:     bdbs,
		starters: service.NewStarterService(starters),
		sql:      service.NewSQLService(bdbs, databases),
		changes:  service.NewChangeService(bdbs, databases),
		formulas: service.NewFormulaService(bdbs, databases, databases, databases),
		exports:  service.NewExportService(bdbs, databases),
		imports:  service.NewImportService(bdbsApp, databases),
		users:    service.NewUserService(auth),
		tokens:   service.NewTokenService(auth, auth),
		owner:    service.NewOwnerService(auth, inst.Owner.ID),
		close:    inst.Close,

		email: email,
	}, nil
}

// actAs returns the account the command line acts as on inst, and its email: the account with email as, looked up
// with auth, or the owner when as is empty. An email no account has wraps domain.ErrUserNotFound.
func actAs(
	ctx context.Context, inst *instance.Instance, auth *intraprocess.AuthApp, as string,
) (intraprocess.Account, string, error) {
	if as == "" {
		return intraprocess.Account{ID: inst.Owner.ID, Roles: inst.Owner.Roles}, inst.Owner.Email, nil
	}
	account, user, err := auth.ActAs(ctx, as)
	if err != nil {
		return intraprocess.Account{}, "", fmt.Errorf("--as: %w", err)
	}
	return account, user.Email, nil
}

// runLocal sets up the command's logger and data directory, opens the local instance with open, runs fn on it, then
// closes it.
func runLocal(cmd *cobra.Command, flags *globalFlags, open localOpener, fn func(*local) error) error {
	logger, err := flags.logger(cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	dataDir, err := flags.resolveDataDir()
	if err != nil {
		return err
	}
	if err := prepareDataDir(cmd, dataDir); err != nil {
		return err
	}
	l, err := open(cmd.Context(), dataDir, flags.as, logger)
	if err != nil {
		return withActAsHint(cmd, err)
	}
	defer l.close()
	return fn(l)
}

// prepareDataDir creates the key the bdbs of dataDir are encrypted with, when the settings derived from dataDir have
// it generated and it doesn't exist yet, and then says so on cmd's stderr, in place of the databases app's log line:
// without that file, the bdbs can't be opened.
func prepareDataDir(cmd *cobra.Command, dataDir string) error {
	cfg, err := localConfig(dataDir)
	if err != nil || !cfg.DatabasesKEKAutogenerate {
		return err
	}
	created, err := filekek.EnsureFile(cfg.DatabasesKEKFile)
	if err != nil {
		return err
	}
	if created {
		cmd.PrintErrf("Created %s, the key your bdbs are encrypted with: back it up, they can't be opened "+
			"without it.\n", cfg.DatabasesKEKFile)
	}
	return nil
}

// applyDataDir fills the settings a local instance derives from dataDir, skipping every one whose environment
// variable (looked up with lookupEnv) is set to a non-empty value, so the environment keeps precedence:
//
//   - InstanceSQLitePath (BAGUETTEDB_INSTANCE_SQLITE_PATH): dataDir/instance.sqlite
//   - DatabasesDataDir (BAGUETTEDB_DATABASES_DATA_DIR): dataDir/databases
//   - DatabasesKEKFile (BAGUETTEDB_DATABASES_KEK_FILE): dataDir/kek
//   - DatabasesKEKAutogenerate (BAGUETTEDB_DATABASES_KEK_AUTOGENERATE): true
//   - DatabasesBackupEnabled (BAGUETTEDB_DATABASES_BACKUP_ENABLED): false
func applyDataDir(cfg *config.Config, dataDir string, lookupEnv func(key string) (string, bool)) {
	isUnset := func(key string) bool {
		v, ok := lookupEnv(key)
		return !ok || v == ""
	}

	if isUnset("BAGUETTEDB_INSTANCE_SQLITE_PATH") {
		cfg.InstanceSQLitePath = filepath.Join(dataDir, instanceStoreFile)
	}
	if isUnset("BAGUETTEDB_DATABASES_DATA_DIR") {
		cfg.DatabasesDataDir = filepath.Join(dataDir, databasesDir)
	}
	if isUnset("BAGUETTEDB_DATABASES_KEK_FILE") {
		cfg.DatabasesKEKFile = filepath.Join(dataDir, kekFile)
	}
	if isUnset("BAGUETTEDB_DATABASES_KEK_AUTOGENERATE") {
		cfg.DatabasesKEKAutogenerate = true
	}
	if isUnset("BAGUETTEDB_DATABASES_BACKUP_ENABLED") {
		cfg.DatabasesBackupEnabled = new(false)
	}
}

// refuseHTTP answers every request with 401. A command-line instance serves no HTTP, so the middleware only stands
// in for the authentication an HTTP listener would need.
func refuseHTTP(http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "this instance serves no HTTP", http.StatusUnauthorized)
	})
}

// localPreamble returns the text sent to agents over stdio before the default instructions: which instance they are
// talking to, where its data lives, whose account they act as when as names one, and how to tell its bdbs from another
// BaguetteDB server's when both are connected.
func localPreamble(dataDir, as string) string {
	preamble := "This is the user's local BaguetteDB instance, running on this machine; its data lives in " +
		filepath.Clean(dataDir) + ". "
	if as != "" {
		preamble += "You act as " + strings.ToLower(strings.TrimSpace(as)) + " on it, one of its accounts. "
	}
	return preamble + otherServersHint
}

// servedPreamble returns the text sent to agents over HTTP before the default instructions, as localPreamble does
// for an instance an agent may reach from another machine: its data lives on the machine serving it.
func servedPreamble(dataDir string) string {
	return "This is the user's own BaguetteDB instance, served over HTTP by bdb serve; its data lives in " +
		filepath.Clean(dataDir) + " on the machine serving it, which may not be the one you run on. " +
		otherServersHint
}

// otherServersHint tells agents how to tell an instance's bdbs from another BaguetteDB server's when both are
// connected.
const otherServersHint = "If another BaguetteDB server is also connected, each bdb exists in exactly one of them: " +
	"resolve a bdb by name with list_bdbs on both, and ask the user when both have a match."

// withActAsHint returns err pointing at users list and users create when --as named no account, err as is otherwise.
func withActAsHint(cmd *cobra.Command, err error) error {
	if !errors.Is(err, domain.ErrUserNotFound) {
		return err
	}
	name := cmd.Root().Name()
	return fmt.Errorf("%w; `%s users` lists the accounts, and `%s users create <email>` creates one", err, name, name)
}
