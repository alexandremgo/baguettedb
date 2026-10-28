// Package databases wires the databases app, which keeps each bdb's data in its own encrypted SQLite file: the metadata
// repositories on the selected instance store, the per-bdb SQLite engine and its key-encryption key, the services
// behind queries, schema changes, the change log, formulas, skills, exports and backups, and the REST routes and MCP
// tools serving them. SetupOptions carries what an embedding binary or a downstream distribution overrides.
package databases

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/filekek"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/s3store"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlitelocal"
	dbapi "github.com/alexandremgo/baguettedb/pkg/databases/api"
	dbmcp "github.com/alexandremgo/baguettedb/pkg/databases/mcp"
	dbrunner "github.com/alexandremgo/baguettedb/pkg/databases/runner"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// SetupResult holds the outputs of the databases module setup.
type SetupResult struct {
	Router      chi.Router
	DatabaseSvc *service.DatabaseService
	QuerySvc    *service.QueryService
	SkillSvc    *service.SkillService
	FormulaSvc  *service.FormulaService
	ExportSvc   *service.ExportService
	ImportSvc   *service.ImportService
	MCPServer   *mcpserver.MCPServer
	// TokenValidator validates JWTs against the platform's JWKS (signature, issuer, validity window). Nil when
	// SetupOptions.RequireToken was injected.
	TokenValidator *authhttp.TokenValidator
	// ShardRouter is nil in single-pod / monolith deployments; when set, the distribution's MCP mount wraps its
	// handler with ShardRouter.MCPProxy so MCP tool calls reach the owning pod.
	ShardRouter *dbapi.ShardRouter
	Cleanup     func()
}

// SetupOptions configures optional behaviors of the databases module setup.
type SetupOptions struct {
	// RequireToken authenticates the app's protected routes: it rejects an unauthenticated request or puts the
	// caller's identity in the request context. When nil, the app validates JWTs against the platform's JWKS.
	RequireToken func(http.Handler) http.Handler

	// Policy maps the global roles the app recognizes to the permissions they grant. The zero value is the built-in
	// policy; a distribution extends it with authz.Policy.With.
	Policy authz.Policy

	// TokenValidatorOptions configure the JWT validator the app builds when RequireToken is nil, e.g. to accept
	// opaque tokens alongside (authhttp.WithOpaqueTokens).
	TokenValidatorOptions []authhttp.ValidatorOption

	// IncludeMCPRoutes controls whether the module mounts its own MCP SSE routes.
	// Set to true in standalone mode. In monolith mode, the MCP gateway handles SSE.
	IncludeMCPRoutes bool

	// QuotaResolver overrides the default quota resolver built from config.
	// If nil, the module uses quota.Fixed over the config values.
	QuotaResolver quota.Resolver

	// KEKProvider overrides the default key-encryption-key provider used to
	// wrap/unwrap per-bdb DEKs. If nil, Setup constructs a
	// filekek.Provider from cfg.DatabasesKEKFile.
	//
	// Currently no distribution overrides this — every shipped distribution
	// relies on filekek.Provider, with KEK delivery handled by the deployment
	// manifest. The hook exists for tests (mock provider) and as an extension
	// point for alternate single-tenant KEK sources (e.g. fetching from a
	// remote KMS instead of a local file).
	KEKProvider service.KEKProvider

	// NewerPlatformSchemaMessage is the message a bdb file upgraded by a newer version than this one is refused with,
	// telling the user how to run the newer version from where they meet it. Empty refuses it with
	// domain.ErrNewerPlatformSchema's.
	NewerPlatformSchemaMessage string
}

// resolveKEKProvider returns opts.KEKProvider when set, otherwise constructs a filekek.Provider from kekPath.
// With autogenerate set, a missing file is first created with a fresh random key and a warning is logged, since that
// key now guards every bdb and exists nowhere else. Returns an error if the override is nil and kekPath is empty or
// unreadable — Setup propagates the error to abort startup.
func resolveKEKProvider(
	opts SetupOptions, kekPath string, autogenerate bool, logger zerolog.Logger,
) (service.KEKProvider, error) {
	if opts.KEKProvider != nil {
		return opts.KEKProvider, nil
	}
	if kekPath == "" {
		return nil, errors.New("BAGUETTEDB_DATABASES_KEK_FILE is required (path to 32-byte raw KEK file)")
	}
	if autogenerate {
		created, err := filekek.EnsureFile(kekPath)
		if err != nil {
			return nil, fmt.Errorf("ensuring KEK file: %w", err)
		}
		if created {
			logger.Warn().Str("path", kekPath).
				Msg("generated a new KEK; back this file up, bdbs cannot be opened without it")
		}
	}
	return filekek.NewProvider(kekPath)
}

// Setup sets up the databases module: runs migrations, creates all adapters, services,
// and handlers. The bdbsApp parameter provides bdb-based authorization.
func Setup(
	ctx context.Context,
	cfg config.Config,
	logger zerolog.Logger,
	bdbsApp service.BdbsApp,
	opts SetupOptions,
) (*SetupResult, error) {
	if cfg.DatabasesDataDir == "" {
		return nil, errors.New(
			"databases: BAGUETTEDB_DATABASES_DATA_DIR is required " +
				"(path to per-bdb SQLCipher database files)")
	}

	kekProvider, err := resolveKEKProvider(opts, cfg.DatabasesKEKFile, cfg.DatabasesKEKAutogenerate, logger)
	if err != nil {
		return nil, fmt.Errorf("databases: resolving KEK provider: %w", err)
	}

	st, err := openStore(ctx, cfg, logger)
	if err != nil {
		return nil, err
	}
	dbRepo := st.databases

	// Resolves the per-database DEK on cache miss: parses the databaseID as a database UUID, fetches the wrapped DEK
	// from the instance store, and unwraps it via the KEK provider.
	dekResolver := func(ctx context.Context, databaseID string) ([]byte, error) {
		id, err := uuid.Parse(databaseID)
		if err != nil {
			return nil, fmt.Errorf("parsing database ID %q as UUID: %w", databaseID, err)
		}
		db, err := dbRepo.FindByID(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("finding database %s: %w", id, err)
		}
		dek, err := kekProvider.Unwrap(db.EncryptionKeyWrapped)
		if err != nil {
			return nil, fmt.Errorf("unwrapping DEK for %s: %w", id, err)
		}
		return dek, nil
	}

	sqliteClient, err := sqlitelocal.NewClient(sqlitelocal.Config{
		DataDir:         cfg.DatabasesDataDir,
		CacheTTL:        cfg.DatabasesSQLiteCacheTTL,
		CacheMaxEntries: cfg.DatabasesSQLiteCacheMaxEntries,
		CacheSweepEvery: cfg.DatabasesSQLiteCacheTTL / 6,
		BusyTimeout:     cfg.DatabasesSQLiteBusyTimeout,
		DEKResolver:     dekResolver,
		Logger:          logger,
	})
	if err != nil {
		st.close()
		return nil, fmt.Errorf("databases: creating sqlitelocal client: %w", err)
	}

	// Creates services.
	classifier := sqlitelocal.NewClassifier()
	var qr quota.Resolver
	if opts.QuotaResolver != nil {
		qr = opts.QuotaResolver
	} else {
		qr = quota.Fixed(quota.Limits{
			MaxDBSizeBytes:          cfg.QuotaMaxDBSizeBytes,
			MaxTotalDBSizeBytes:     cfg.QuotaMaxTotalDBSizeBytes,
			UndoStackMaxSize:        cfg.QuotaUndoStackMaxSize,
			AffectedRowsMax:         cfg.QuotaAffectedRowsMax,
			MaxStatementsPerRequest: cfg.QuotaMaxStatementsPerRequest,
			MaxRowsPerRequest:       cfg.QuotaMaxRowsPerRequest,
			MaxQueryResultRows:      cfg.QuotaMaxQueryResultRows,
			MaxConcurrentQueries:    cfg.QuotaMaxConcurrentQueries,
			SnapshotMaxRows:         cfg.QuotaSnapshotMaxRows,
			MaxDBExportsPerWindow:   cfg.QuotaMaxDBExportsPerWindow,
			DBExportWindow:          cfg.QuotaDBExportWindow,
			MaxFormulaExportRows:    cfg.QuotaMaxFormulaExportRows,
		})
	}
	dbSvc := service.NewDatabaseService(dbRepo, sqliteClient, sqliteClient, bdbsApp, qr, kekProvider, logger)
	// Bounds every Execute / ExecuteTransaction call. DatabaseService keeps
	// the raw sqliteClient so admin paths (CreateDatabase, etc.) aren't
	// affected; the timeout targets user-facing query paths.
	boundedExecutor := service.NewBoundedExecutor(sqliteClient, cfg.DatabasesMaxQueryDuration)
	querySvc := service.NewQueryService(dbSvc, boundedExecutor, classifier, qr, cfg.QuotaAffectedRowsMax,
		cfg.DatabasesMaxQueryDuration)
	skillSvc := service.NewSkillService(dbSvc, boundedExecutor)
	formulaSvc := service.NewFormulaService(
		dbSvc, boundedExecutor, classifier, querySvc, cfg.DatabasesFormulaRunTimeout, logger)
	// The per-run deadline protects the app's interactive UX; assistant runs over MCP get only the
	// general execute ceiling (the bounded executor), so heavy-but-legitimate formulas stay runnable
	// by name instead of pushing the assistant back to raw SQL.
	mcpFormulaSvc := service.NewFormulaService(dbSvc, boundedExecutor, classifier, querySvc, 0, logger)

	// Spools export artifacts on the same volume as the database files — a database export is a full copy
	// and must not land on the container's small ephemeral filesystem.
	exportSvc, err := service.NewExportService(dbSvc, formulaSvc, sqliteClient, sqliteClient, st.exports, qr,
		filepath.Join(cfg.DatabasesDataDir, ".exports-tmp"), cfg.DatabasesMaxQueryDuration, logger)
	if err != nil {
		if cerr := sqliteClient.Close(); cerr != nil {
			logger.Warn().Err(cerr).Msg("databases: closing sqlitelocal client")
		}
		st.close()
		return nil, fmt.Errorf("databases: creating export service: %w", err)
	}

	// Spools imported files on the database volume too: an import is a full copy of a database.
	importSvc, err := service.NewImportService(dbSvc, sqliteClient, filepath.Join(cfg.DatabasesDataDir, ".imports-tmp"),
		logger)
	if err != nil {
		if cerr := sqliteClient.Close(); cerr != nil {
			logger.Warn().Err(cerr).Msg("databases: closing sqlitelocal client")
		}
		st.close()
		return nil, fmt.Errorf("databases: creating import service: %w", err)
	}

	var tokenValidator *authhttp.TokenValidator
	requireToken := opts.RequireToken
	if requireToken == nil {
		jwksURL := cfg.HydraPublicURL + "/.well-known/jwks.json"
		tokenValidator, err = authhttp.NewTokenValidator(ctx, jwksURL, cfg.HydraIssuerURL, "", logger,
			opts.TokenValidatorOptions...)
		if err != nil {
			st.close()
			return nil, fmt.Errorf("databases: creating token validator: %w", err)
		}
		requireToken = tokenValidator.RequireToken()
	}
	closeTokenValidator := func() {
		if tokenValidator != nil {
			tokenValidator.Close()
		}
	}

	// Resolves this pod's shard ordinal once, up front. The boot reconcile and creator-owns placement both need
	// the real ordinal before any database work happens, so it must be set on the services before ReconcileOnBoot
	// runs — otherwise the reconcile sees the zero-default ordinal and restores every shard's databases onto this
	// pod. Defaults to 0 for single-pod (shard count 1) deployments.
	shardOrdinal := int16(0)
	if cfg.DatabasesPodName != "" {
		o, perr := dbapi.OrdinalFromPodName(cfg.DatabasesPodName)
		if perr != nil {
			closeTokenValidator()
			if cerr := sqliteClient.Close(); cerr != nil {
				logger.Warn().Err(cerr).Msg("databases: closing sqlitelocal client")
			}
			st.close()
			return nil, fmt.Errorf("databases: parsing shard ordinal from pod name %q: %w",
				cfg.DatabasesPodName, perr)
		}
		shardOrdinal = int16(o)
	}
	podColor := int16(cfg.DatabasesPodColor)
	dbSvc.SetShardOrdinal(shardOrdinal)
	dbSvc.SetPodColor(podColor)
	dbSvc.SetNewerPlatformSchemaMessage(opts.NewerPlatformSchemaMessage)

	// Optionally creates the snapshot backup pipeline: object-store adapter, backup service, and periodic
	// runner. An empty S3 endpoint means snapshot storage is not provisioned for this deployment — the module
	// runs without backups and the internal snapshot endpoints respond 503.
	var backupSvc *service.BackupService
	backupCancel := func() {}
	var backupWG sync.WaitGroup
	if cfg.DatabasesS3Endpoint != "" {
		store, err := s3store.NewClient(s3store.Config{
			Endpoint:        cfg.DatabasesS3Endpoint,
			Region:          cfg.DatabasesS3Region,
			Bucket:          cfg.DatabasesS3Bucket,
			Prefix:          cfg.DatabasesS3Prefix,
			AccessKeyID:     cfg.DatabasesS3AccessKeyID,
			SecretAccessKey: cfg.DatabasesS3SecretAccessKey,
			UsePathStyle:    cfg.DatabasesS3UsePathStyle,
		})
		if err != nil {
			closeTokenValidator()
			if cerr := sqliteClient.Close(); cerr != nil {
				logger.Warn().Err(cerr).Msg("databases: closing sqlitelocal client")
			}
			st.close()
			return nil, fmt.Errorf("databases: creating s3store client: %w", err)
		}

		// Stages snapshots on the same volume as the database files — a snapshot is a full database copy
		// and must not land on the container's small ephemeral filesystem.
		backupSvc, err = service.NewBackupService(
			dbRepo, sqliteClient, sqliteClient, store,
			filepath.Join(cfg.DatabasesDataDir, ".snapshots-tmp"),
			cfg.DatabasesBackupMaxSnapshotAge, logger,
		)
		if err != nil {
			closeTokenValidator()
			if cerr := sqliteClient.Close(); cerr != nil {
				logger.Warn().Err(cerr).Msg("databases: closing sqlitelocal client")
			}
			st.close()
			return nil, fmt.Errorf("databases: creating backup service: %w", err)
		}

		// Applies this pod's shard before the boot reconcile so it only restores databases this pod owns. Set
		// here (not later with the router) because ReconcileOnBoot runs next and reads the ordinal.
		backupSvc.SetShardOrdinal(shardOrdinal)
		backupSvc.SetPodColor(podColor)

		// Restores any database file missing on this volume from its latest snapshot before serving traffic
		// (the recovery path for a rescheduled pod on a fresh PVC). In strict mode a failed restore aborts
		// startup so a half-recovered instance never serves; databases with no snapshot at all are logged as
		// unrecoverable but never block startup — staying down would not bring the data back.
		reconcileStats, reconcileErr := backupSvc.ReconcileOnBoot(ctx)
		if cfg.DatabasesReconcileMode == "strict" && (reconcileErr != nil || reconcileStats.Failed > 0) {
			closeTokenValidator()
			if cerr := sqliteClient.Close(); cerr != nil {
				logger.Warn().Err(cerr).Msg("databases: closing sqlitelocal client")
			}
			st.close()
			return nil, fmt.Errorf("databases: strict boot reconcile failed (stats %+v): %w",
				reconcileStats, reconcileErr)
		}
		if reconcileErr != nil || reconcileStats.Failed > 0 {
			logger.Warn().Err(reconcileErr).
				Int("failed", reconcileStats.Failed).
				Msg("databases: boot reconcile incomplete; starting anyway (warn mode)")
		}

		if cfg.DatabasesBackupEnabled == nil || *cfg.DatabasesBackupEnabled {
			backupRunner := dbrunner.NewBackupRunner(backupSvc, cfg.DatabasesBackupInterval, logger)
			var runnerCtx context.Context
			runnerCtx, backupCancel = context.WithCancel(ctx)
			backupWG.Go(func() {
				backupRunner.Run(runnerCtx)
			})
		} else {
			logger.Warn().Msg("databases: periodic backup runner disabled via config; " +
				"on-demand snapshot endpoints stay available")
		}
	} else {
		logger.Info().Msg("databases: snapshot storage not configured; backups disabled")
	}

	// Backfills tracked database sizes for this pod's files before serving traffic, so size-based
	// enforcement starts truthful. Runs after the boot reconcile so freshly restored files are counted;
	// failures are logged per database and never block startup.
	dbSvc.BackfillStoredSizes(ctx)

	// Levels every database this pod owns to the current platform schema version, so an idle bdb doesn't
	// stay pinned on an old version until its next first touch. Runs in the background (a slow pass must
	// not hold up startup probes) and after the boot reconcile, so freshly restored files get migrated
	// too. The pass migrates each database in a serialized way, so it is safe alongside live traffic.
	migrateCtx, migrateCancel := context.WithCancel(ctx)
	var migrateWG sync.WaitGroup
	migrateWG.Go(func() {
		dbSvc.MigrateOwnedOnBoot(migrateCtx)
	})

	// Creates MCP server (tool handlers only, no transport).
	mcpServer := dbmcp.NewServer(querySvc, skillSvc, mcpFormulaSvc, logger)

	// In standalone mode, creates SSE transport and mounts MCP routes on the router.
	var sseServer *mcpserver.SSEServer
	if opts.IncludeMCPRoutes {
		sseServer = mcpserver.NewSSEServer(mcpServer,
			mcpserver.WithStaticBasePath("/mcp"),
			mcpserver.WithSSEContextFunc(mcputil.SSEContextFunc),
		)
	}

	// Shard routing: installed whenever this is a StatefulSet pod (POD_NAME set), so a pod forwards bdb-scoped
	// requests it doesn't own to the owner over per-pod DNS — including the cross-fleet (blue/green) case at a
	// single shard. Monolith / non-StatefulSet deployments (no POD_NAME) skip it and serve everything locally. The
	// (color, ordinal) identity was resolved and applied to the services above (before the boot reconcile).
	var shardRouter *dbapi.ShardRouter
	if cfg.DatabasesPodName != "" {
		peerURL := func(color, ordinal int16) string {
			return dbapi.BuildPeerURL(cfg.DatabasesPeerURLTemplate, color, ordinal)
		}
		shardRouter = dbapi.NewShardRouter(podColor, shardOrdinal, peerURL, dbSvc, logger)
	}

	// Creates handlers and router.
	handler := dbapi.NewHandler(dbSvc, querySvc, logger)
	skillHandler := dbapi.NewSkillHandler(skillSvc, logger)
	formulaHandler := dbapi.NewFormulaHandler(formulaSvc, logger)
	exportHandler := dbapi.NewExportHandler(exportSvc, logger)
	internalHandler := dbapi.NewInternalHandler(querySvc, skillSvc, formulaSvc, dbSvc, backupSvc, logger)
	router := dbapi.NewRouter(
		handler, skillHandler, formulaHandler, exportHandler, internalHandler, shardRouter,
		requireToken, opts.Policy, sseServer, cfg.MaxRequestBodySize, cfg.InternalAPISecret)

	cleanup := func() {
		// Stops the boot migration pass and the backup runner first, and waits for both to drain, before
		// closing the connections they work through.
		migrateCancel()
		backupCancel()
		migrateWG.Wait()
		backupWG.Wait()
		closeTokenValidator()
		if err := sqliteClient.Close(); err != nil {
			logger.Warn().Err(err).Msg("databases: closing sqlitelocal client")
		}
		st.close()
	}

	return &SetupResult{
		Router:         router,
		DatabaseSvc:    dbSvc,
		QuerySvc:       querySvc,
		SkillSvc:       skillSvc,
		FormulaSvc:     formulaSvc,
		ExportSvc:      exportSvc,
		ImportSvc:      importSvc,
		MCPServer:      mcpServer,
		TokenValidator: tokenValidator,
		ShardRouter:    shardRouter,
		Cleanup:        cleanup,
	}, nil
}
