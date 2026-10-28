// Package service holds the databases application services: the lifecycle of each bdb's database (provisioning under
// its own encryption key, access checks, platform schema migrations, size tracking, pod ownership and moves), query and
// schema execution with the change log and its undo, redo and revert, skills, formulas and their runs, exports,
// imports, and snapshot backups and restores. port.go holds the contracts several parties name.
package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/migrations"
)

// platformMigrationTimeout caps how long a single database's platform
// migration run may take. Sized for a slow disk + a database with many
// user tables to regenerate triggers for; well beyond observed real
// durations (typically <5s).
const platformMigrationTimeout = 5 * time.Minute

var tracer = otel.Tracer("baguettedb/databases/service")

// databaseManager defines operations for managing per-bdb SQLite databases.
type databaseManager interface {
	// CreateDatabase provisions a new isolated SQLite database.
	// dek is the 32-byte raw data-encryption key for the new database; the
	// adapter applies it at open time.
	CreateDatabase(ctx context.Context, databaseID string, dek []byte) error

	// SetDatabaseMaxSize configures the maximum size of the given database.
	SetDatabaseMaxSize(ctx context.Context, databaseID string, maxSizeBytes int64) error

	// GetDatabaseStats returns usage statistics for the given database.
	GetDatabaseStats(ctx context.Context, databaseID string) (*domain.DatabaseStats, error)

	// DeleteDatabase removes a database file and its data (file only — no metadata).
	DeleteDatabase(ctx context.Context, databaseID string) error

	// EvictDatabase drops any cached connection to the database without deleting its file, so the file is
	// quiescent (no open handles) — used to drain a database before moving it to another pod.
	EvictDatabase(ctx context.Context, databaseID string) error
}

// DatabaseService handles database lifecycle operations.
type DatabaseService struct {
	repo        DatabaseRepository
	dbMgr       databaseManager
	executor    queryExecutor
	bdbsApp     BdbsApp
	limits      quota.Resolver
	kekProvider KEKProvider
	logger      zerolog.Logger

	// shardOrdinal is this pod's shard. A new database is assigned to it ("creator owns"), keeping a
	// database's file and its pod_shard on the same pod. Cross-pod balance is the rebalancer's job, not
	// creation-time placement. 0 in single-pod deployments.
	shardOrdinal int16

	// podColor is this pod's blue/green fleet (0 = blue, 1 = green). A new database is stamped with it too
	// ("creator owns"), so the file and its (pod_color, pod_shard) ownership stay co-located. 0 single-fleet.
	podColor int16

	// migrationLocks serializes platform migration runs per database so
	// two concurrent first-touch requests can't both enter the runner. Keys
	// are database strings; values are *sync.Mutex created on first use.
	migrationLocks sync.Map

	// sizeRefreshInterval is the minimum delay between two stored-size refreshes of the same database. Write
	// bursts collapse into one stat + one Postgres update per interval; the tracked size lags the live file
	// by at most this long (plus the gap to the next write).
	sizeRefreshInterval time.Duration

	// lastSizeRefresh coalesces size refreshes per database. Keys are database uuid.UUIDs; values are the
	// time.Time of the last refresh attempt. In-memory is enough: the pod owning a database's file is the
	// only one refreshing its size.
	lastSizeRefresh sync.Map

	// newerPlatformSchemaMessage is the message a bdb file upgraded by a newer version is refused with; empty
	// refuses it with domain.ErrNewerPlatformSchema's.
	newerPlatformSchemaMessage string

	// ownershipCacheTTL bounds how long ownership lookups against the bdbs module are reused.
	// 0 disables caching.
	ownershipCacheTTL time.Duration

	// ownedBdbIDsCache (per owner) and bdbOwnerCache (per bdb) cache ownership lookups, guarded by
	// ownershipMu and created lazily on first store. Per-pod staleness up to the TTL is fine:
	// aggregate enforcement is lag-tolerant by design.
	ownershipMu      sync.Mutex
	ownedBdbIDsCache map[uuid.UUID]ownedBdbIDsEntry
	bdbOwnerCache    map[uuid.UUID]bdbOwnerEntry
}

// NewDatabaseService creates a new DatabaseService.
func NewDatabaseService(
	repo DatabaseRepository,
	dbMgr databaseManager,
	executor queryExecutor,
	bdbsApp BdbsApp,
	limits quota.Resolver,
	kekProvider KEKProvider,
	logger zerolog.Logger,
) *DatabaseService {
	return &DatabaseService{
		repo:                repo,
		dbMgr:               dbMgr,
		executor:            executor,
		bdbsApp:             bdbsApp,
		limits:              limits,
		kekProvider:         kekProvider,
		logger:              logger,
		sizeRefreshInterval: defaultSizeRefreshInterval,
		ownershipCacheTTL:   defaultOwnershipCacheTTL,
	}
}

// SetNewerPlatformSchemaMessage sets the message a bdb file upgraded by a newer version is refused with. Empty keeps
// domain.ErrNewerPlatformSchema's.
func (s *DatabaseService) SetNewerPlatformSchemaMessage(message string) {
	s.newerPlatformSchemaMessage = message
}

// SetShardOrdinal sets this pod's shard ordinal.
func (s *DatabaseService) SetShardOrdinal(ordinal int16) {
	s.shardOrdinal = ordinal
}

// SetPodColor sets this pod's blue/green fleet color.
func (s *DatabaseService) SetPodColor(color int16) {
	s.podColor = color
}

// AuthorizeAndResolveDatabase checks the user's permission on the bdb, then returns
// the bdb's database — creating it lazily if none exists yet.
//
// Convenience wrapper around the three-phase split (CheckDatabasePermission +
// ResolveBdbDatabase). Most callers — handlers that don't need to
// classify SQL between phases — should use this. QueryService.Execute
// uses the split form so it can classify against the resolved
// connection before checking the operation-specific permission.
//
// Returns domain.ErrDatabaseReadOnly when the database is read-only and perm is anything other than DBPermRead.
func (s *DatabaseService) AuthorizeAndResolveDatabase(
	ctx context.Context, userID, bdbID uuid.UUID, perm authz.DatabasePermission,
) (*domain.Database, error) {
	ctx, span := tracer.Start(ctx, "DatabaseService.AuthorizeAndResolveDatabase",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
			attribute.String("permission", string(perm)),
		),
	)
	defer span.End()

	if err := s.CheckDatabasePermission(ctx, userID, bdbID, perm); err != nil {
		return nil, err
	}

	db, err := s.ResolveBdbDatabase(ctx, bdbID)
	if err != nil {
		return nil, err
	}
	if readOnlyBlocks(db, perm) {
		return nil, domain.ErrDatabaseReadOnly
	}

	return db, nil
}

// readOnlyBlocks reports whether db's read-only mode forbids an operation requiring perm. A read-only database
// (set during a rebalance drain) still serves reads, so the non-mutating permissions — DBPermRead and
// DBPermExport (WAL gives the export copy a stable view) — are allowed; every other permission is blocked.
func readOnlyBlocks(db *domain.Database, perm authz.DatabasePermission) bool {
	return db.ReadOnly && perm != authz.DBPermRead && perm != authz.DBPermExport
}

// OwnerForBdb returns the (fleet color, shard ordinal) that owns the bdb's database. found is false when no
// database exists yet for the bdb (so the caller serves locally and lets lazy creation assign an owner),
// distinguishing that from a real lookup error.
func (s *DatabaseService) OwnerForBdb(
	ctx context.Context, bdbID uuid.UUID,
) (color, shard int16, found bool, err error) {
	db, err := s.repo.FindByBdb(ctx, bdbID)
	if errors.Is(err, domain.ErrDatabaseNotFound) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, err
	}
	return db.PodColor, db.PodShard, true, nil
}

// ActiveColor returns the currently active blue/green fleet color (0 = blue, 1 = green).
func (s *DatabaseService) ActiveColor(ctx context.Context) (int16, error) {
	color, err := s.repo.GetActiveColor(ctx)
	if err != nil {
		return 0, fmt.Errorf("reading active fleet color: %w", err)
	}
	return color, nil
}

// --- Move primitives (used by the rebalancer's per-pod internal endpoints) ---

// SetReadOnly toggles a database's read_only flag. Set true to drain a database before moving it (writes are then
// rejected with domain.ErrDatabaseReadOnly); the hand-off clears it on the new owner via CompleteMove.
func (s *DatabaseService) SetReadOnly(ctx context.Context, databaseID uuid.UUID, readOnly bool) error {
	if err := s.repo.SetReadOnly(ctx, databaseID, readOnly); err != nil {
		return fmt.Errorf("setting read_only=%t on %s: %w", readOnly, databaseID, err)
	}
	return nil
}

// EvictDatabase drops this pod's cached connection to the database (no file change), leaving the file quiescent
// before it is snapshotted for a move.
func (s *DatabaseService) EvictDatabase(ctx context.Context, databaseID uuid.UUID) error {
	if err := s.dbMgr.EvictDatabase(ctx, databaseID.String()); err != nil {
		return fmt.Errorf("evicting %s: %w", databaseID, err)
	}
	return nil
}

// DeleteLocalFile removes the database's file from this pod's volume WITHOUT touching its Postgres metadata — the
// bdb still exists and is owned by the new pod; only the source pod's now-orphaned copy is removed. Guards
// against data loss: refuses (domain.ErrDatabaseOwnedHere) while this shard still owns the database, since the
// file is only an orphan to delete once the hand-off has moved ownership elsewhere.
func (s *DatabaseService) DeleteLocalFile(ctx context.Context, databaseID uuid.UUID) error {
	db, err := s.repo.FindByID(ctx, databaseID)
	if err != nil {
		return fmt.Errorf("finding database %s: %w", databaseID, err)
	}
	if db.OwnedBy(s.podColor, s.shardOrdinal) {
		return fmt.Errorf("%w: refusing to delete local file for %s", domain.ErrDatabaseOwnedHere, databaseID)
	}
	if err := s.dbMgr.DeleteDatabase(ctx, databaseID.String()); err != nil {
		return fmt.Errorf("deleting local file for %s: %w", databaseID, err)
	}
	return nil
}

// VerifyDatabaseOpens confirms the database file opens and decrypts with its DEK by reading the schema page (a
// wrong key or corrupt file fails here). Used after adopting a moved file, before the hand-off completes.
func (s *DatabaseService) VerifyDatabaseOpens(ctx context.Context, databaseID uuid.UUID) error {
	const probe = "SELECT count(*) FROM sqlite_master"
	if _, err := s.executor.ExecuteInternal(ctx, databaseID.String(), probe, nil); err != nil {
		return fmt.Errorf("verifying %s opens with its DEK: %w", databaseID, err)
	}
	return nil
}

// CompleteMove commits a database's move to its new owner (newColor, newShard): it transfers ownership (pod_color
// + pod_shard) and clears read_only so the new pod both owns the bdb and serves writes — in one atomic update,
// so there's never a window where the bdb is writable but still owned by the old pod.
func (s *DatabaseService) CompleteMove(ctx context.Context, databaseID uuid.UUID, newColor, newShard int16) error {
	if err := s.repo.UpdateOwnerAndReadOnly(ctx, databaseID, newColor, newShard, false); err != nil {
		return fmt.Errorf("completing move of %s to (color %d, shard %d): %w", databaseID, newColor, newShard, err)
	}
	return nil
}

// CheckDatabasePermission verifies that the user has the given
// permission on the bdb's database. Returns domain.ErrAccessDenied
// if not. This is a pure permission check — the database doesn't have
// to exist (and isn't resolved) for this call to succeed.
func (s *DatabaseService) CheckDatabasePermission(
	ctx context.Context, userID, bdbID uuid.UUID, perm authz.DatabasePermission,
) error {
	allowed, err := s.bdbsApp.CheckMemberPermission(ctx, userID, bdbID, perm)
	if err != nil {
		return fmt.Errorf("checking permission: %w", err)
	}
	if !allowed {
		return domain.ErrAccessDenied
	}
	return nil
}

// AuthorizeMembership verifies that the user is a member of the bdb
// (any role). Returns domain.ErrNotMember if not. Cheaper than the
// per-operation permission check; intended as the early gate before
// classifying user SQL — non-members can't even probe a bdb.
//
// Implemented as a DBPermRead check: any role that grants membership
// also grants read access in the current role model.
func (s *DatabaseService) AuthorizeMembership(ctx context.Context, userID, bdbID uuid.UUID) error {
	err := s.CheckDatabasePermission(ctx, userID, bdbID, authz.DBPermRead)
	if errors.Is(err, domain.ErrAccessDenied) {
		return domain.ErrNotMember
	}
	return err
}

// ResolveBdbDatabase returns the bdb's database, creating it
// lazily if none exists yet, and runs any pending platform migrations.
// Does NOT check permissions — callers must gate access separately
// (typically via AuthorizeMembership before classifying, then
// CheckDatabasePermission after).
func (s *DatabaseService) ResolveBdbDatabase(ctx context.Context, bdbID uuid.UUID) (*domain.Database, error) {
	db, err := s.repo.FindByBdb(ctx, bdbID)
	if errors.Is(err, domain.ErrDatabaseNotFound) {
		// Lazy provisioning: creates on first access.
		dbID, createErr := s.CreateDatabase(ctx, bdbID)
		if createErr != nil {
			return nil, fmt.Errorf("provisioning database for bdb %s: %w", bdbID, createErr)
		}
		db, err = s.repo.FindByID(ctx, dbID)
	}
	if err != nil {
		return nil, fmt.Errorf("finding database for bdb %s: %w", bdbID, err)
	}

	if err := s.EnsurePlatformSchema(ctx, db); err != nil {
		return nil, err
	}

	return db, nil
}

// CreateDatabase provisions a new database: creates the per-bdb SQLite database, then persists metadata.
// Returns the database ID. If metadata insertion fails, it attempts to clean up the database.
func (s *DatabaseService) CreateDatabase(ctx context.Context, bdbID uuid.UUID) (uuid.UUID, error) {
	ctx, span := tracer.Start(ctx, "DatabaseService.CreateDatabase",
		trace.WithAttributes(attribute.String("bdb.id", bdbID.String())),
	)
	defer span.End()
	// Resolves the owner up front: the aggregate storage check must reject before any file exists, and
	// the same roles drive the per-database size limit below.
	ownerID, ownerRoles, err := s.bdbOwner(ctx, bdbID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("getting bdb owner: %w", err)
	}
	if err := s.checkOwnerStorageQuota(ctx, ownerID, ownerRoles); err != nil {
		return uuid.Nil, err
	}

	db := &domain.Database{
		ID:        uuid.New(),
		BdbID:     bdbID,
		PodShard:  s.shardOrdinal, // creator owns: the file is written on this pod, so it must own the shard too
		PodColor:  s.podColor,     // ...and the fleet color, so (pod_color, pod_shard) identifies the owning pod
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	databaseID := db.ID.String()

	dek, err := domain.GenerateDEK()
	if err != nil {
		return uuid.Nil, fmt.Errorf("generating DEK: %w", err)
	}

	wrapStart := time.Now()
	wrapped, err := s.kekProvider.Wrap(dek)
	wrapDuration := time.Since(wrapStart)
	if err != nil {
		s.logger.Error().Err(err).
			Str("op", "wrap").
			Str("database_id", databaseID).
			Int64("duration_ms", wrapDuration.Milliseconds()).
			Msg("KEK wrap failed")
		return uuid.Nil, fmt.Errorf("wrapping DEK: %w", err)
	}
	s.logger.Info().
		Str("op", "wrap").
		Str("database_id", databaseID).
		Int64("duration_ms", wrapDuration.Milliseconds()).
		Msg("KEK wrap succeeded")
	db.EncryptionKeyWrapped = wrapped

	if err := s.dbMgr.CreateDatabase(ctx, databaseID, dek); err != nil {
		return uuid.Nil, fmt.Errorf("%w: %w", domain.ErrDatabaseCreation, err)
	}

	// Configures the database size limit based on the bdb owner's roles (resolved before creation above).
	maxSize := s.limits(ownerRoles).MaxDBSizeBytes
	if maxSize > 0 {
		if err := s.dbMgr.SetDatabaseMaxSize(ctx, databaseID, maxSize); err != nil {
			s.logger.Error().Err(err).
				Str("database_id", databaseID).
				Int64("max_size_bytes", maxSize).
				Msg("Failed to set database size limit, cleaning up database file")

			if cleanupErr := s.dbMgr.DeleteDatabase(ctx, databaseID); cleanupErr != nil {
				s.logger.Error().Err(cleanupErr).
					Str("database_id", databaseID).
					Msg("Failed to clean up database file after size limit failure")
			}

			return uuid.Nil, fmt.Errorf("setting database size limit: %w", err)
		}
	}

	if err := s.repo.Create(ctx, db); err != nil {
		s.logger.Error().Err(err).
			Str("database_id", databaseID).
			Msg("Failed to persist database metadata, cleaning up database file")

		if cleanupErr := s.dbMgr.DeleteDatabase(ctx, databaseID); cleanupErr != nil {
			s.logger.Error().Err(cleanupErr).
				Str("database_id", databaseID).
				Msg("Failed to clean up database file after metadata failure")
		}

		return uuid.Nil, fmt.Errorf("persisting database metadata: %w", err)
	}

	s.RefreshStoredSize(ctx, databaseID)

	s.logger.Info().
		Str("database_id", db.ID.String()).
		Str("bdb_id", bdbID.String()).
		Msg("Created new database")

	return db.ID, nil
}

// GetDatabaseStats returns the bdb database's on-disk usage and the size limit resolved from the
// bdb owner's roles (0 = unlimited).
func (s *DatabaseService) GetDatabaseStats(
	ctx context.Context, bdbID uuid.UUID,
) (usedBytes uint64, limitBytes int64, err error) {
	ctx, span := tracer.Start(ctx, "DatabaseService.GetDatabaseStats",
		trace.WithAttributes(attribute.String("bdb.id", bdbID.String())),
	)
	defer span.End()

	db, err := s.repo.FindByBdb(ctx, bdbID)
	if err != nil {
		return 0, 0, fmt.Errorf("finding database for bdb %s: %w", bdbID, err)
	}

	databaseID := db.ID.String()
	stats, err := s.dbMgr.GetDatabaseStats(ctx, databaseID)
	if err != nil {
		return 0, 0, fmt.Errorf("getting database stats: %w", err)
	}

	_, ownerRoles, err := s.bdbOwner(ctx, bdbID)
	if err != nil {
		return 0, 0, fmt.Errorf("getting bdb owner: %w", err)
	}

	return stats.StorageBytesUsed, s.limits(ownerRoles).MaxDBSizeBytes, nil
}

// DeleteBdbDatabase removes the database belonging to the given bdb. Idempotent: returns nil when no database
// exists for the bdb (e.g. the bdb was created and never provisioned a database before being purged).
func (s *DatabaseService) DeleteBdbDatabase(ctx context.Context, bdbID uuid.UUID) error {
	db, err := s.repo.FindByBdb(ctx, bdbID)
	if errors.Is(err, domain.ErrDatabaseNotFound) {
		s.logger.Info().
			Str("bdb_id", bdbID.String()).
			Msg("DeleteBdbDatabase: no database existed for bdb; nothing to delete")
		return nil
	}
	if err != nil {
		return fmt.Errorf("finding database for bdb: %w", err)
	}
	return s.DeleteDatabase(ctx, db.ID)
}

// DeleteDatabase removes the database record and its per-bdb SQLite database.
func (s *DatabaseService) DeleteDatabase(ctx context.Context, databaseID uuid.UUID) error {
	if _, err := s.repo.FindByID(ctx, databaseID); err != nil {
		return fmt.Errorf("finding database: %w", err)
	}

	if err := s.repo.Delete(ctx, databaseID); err != nil {
		return fmt.Errorf("deleting database record: %w", err)
	}

	if err := s.dbMgr.DeleteDatabase(ctx, databaseID.String()); err != nil {
		s.logger.Error().Err(err).
			Str("database_id", databaseID.String()).
			Msg("Failed to delete database file after database record deletion")
	}

	return nil
}

// CurrentPlatformSchemaVersion is the latest platform schema version.
// Increment this when adding a new .up.sql file in migrations/platform/.
const CurrentPlatformSchemaVersion = 8

type platformMigration struct {
	Version int
	SQL     string
}

// EnsurePlatformSchema runs any pending platform migrations on the bdb's
// SQLite database. The Postgres column is a best-effort mirror; the source
// of truth is SQLite's PRAGMA user_version.
//
// A database a newer version upgraded past CurrentPlatformSchemaVersion is refused, by the mirror or by its
// user_version, with a *domain.NewerPlatformSchemaError carrying the message SetNewerPlatformSchemaMessage set, or
// domain.ErrNewerPlatformSchema when none is set: this version would read and write it in an older shape, missing
// what the newer one records.
//
// Migrations are serialized per database via migrationLocks so two
// concurrent first-touch requests don't both enter the runner. The
// migration itself uses a ctx derived with context.WithoutCancel +
// timeout, so a client-side cancellation of the triggering request does
// not abort the migration mid-apply (a partial apply would leave the
// database stuck at a non-idempotent failure on the next attempt).
func (s *DatabaseService) EnsurePlatformSchema(ctx context.Context, db *domain.Database) error {
	if db.PlatformSchemaVersion > CurrentPlatformSchemaVersion {
		return s.newerPlatformSchemaError()
	}
	if db.PlatformSchemaVersion == CurrentPlatformSchemaVersion {
		return nil
	}

	databaseID := db.ID.String()

	mu := s.migrationLockFor(databaseID)
	mu.Lock()
	defer mu.Unlock()

	// Detaches the migration ctx from the request ctx's cancellation chain.
	// Tracing/logging values still propagate via context.WithoutCancel.
	migCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), platformMigrationTimeout)
	defer cancel()

	finalVersion, err := runPlatformMigrations(migCtx, s.executor, databaseID, migrations.PlatformFS, s.logger)
	if errors.Is(err, domain.ErrNewerPlatformSchema) {
		return s.newerPlatformSchemaError()
	}
	if err != nil {
		return fmt.Errorf("running platform migrations for database %s: %w", databaseID, err)
	}

	// Mirrors to Postgres (best-effort). Uses the request ctx — a cancelled
	// request leaves the mirror stale, but the next request re-runs the
	// runner (no-op now that user_version matches) and retries the mirror.
	if finalVersion > db.PlatformSchemaVersion {
		if updateErr := s.repo.UpdatePlatformSchemaVersion(ctx, db.ID, finalVersion); updateErr != nil {
			s.logger.Error().Err(updateErr).
				Str("database_id", databaseID).
				Int("version", finalVersion).
				Msg("Failed to mirror platform schema version to Postgres")
		}
		s.RefreshStoredSize(ctx, databaseID)
	}

	return nil
}

// newerPlatformSchemaError returns the error a database upgraded by a newer version is refused with.
func (s *DatabaseService) newerPlatformSchemaError() error {
	if s.newerPlatformSchemaMessage == "" {
		return domain.ErrNewerPlatformSchema
	}
	return &domain.NewerPlatformSchemaError{Message: s.newerPlatformSchemaMessage}
}

// migrationLockFor returns the per-database mutex used to serialize
// platform migration runs, creating one on first use.
func (s *DatabaseService) migrationLockFor(databaseID string) *sync.Mutex {
	if v, ok := s.migrationLocks.Load(databaseID); ok {
		return v.(*sync.Mutex)
	}
	mu := &sync.Mutex{}
	actual, _ := s.migrationLocks.LoadOrStore(databaseID, mu)
	return actual.(*sync.Mutex)
}

// runPlatformMigrations reads PRAGMA user_version from the SQLite database,
// runs any pending .up.sql migrations from the given filesystem, and sets
// the new user_version. Returns the final version reached.
//
// A user_version past CurrentPlatformSchemaVersion, set by a newer version, returns domain.ErrNewerPlatformSchema.
//
// Each version's SQL statements, post-migration hook, and user_version bump
// run inside a single transaction (WithTx). A failure at any point rolls
// the whole version back, so the next attempt sees a clean pre-migration
// state. The transaction re-reads user_version first and skips a version
// another process applied since the read above, so several processes
// opening the same file never run a version twice. Migration SQL is also written to be idempotent (IF NOT EXISTS /
// IF EXISTS) as a second line of defence: if a prior version of the runner
// left partial state on disk (no transaction wrap), the new runner still
// recovers cleanly.
func runPlatformMigrations(
	ctx context.Context,
	executor migrationExecutor,
	databaseID string,
	migrationFS fs.FS,
	logger zerolog.Logger,
) (int, error) {
	currentVersion, err := readUserVersion(ctx, executor, databaseID)
	if err != nil {
		return 0, fmt.Errorf("reading user_version: %w", err)
	}

	if currentVersion > CurrentPlatformSchemaVersion {
		return currentVersion, domain.ErrNewerPlatformSchema
	}
	if currentVersion == CurrentPlatformSchemaVersion {
		return currentVersion, nil
	}

	loaded, err := loadPlatformMigrations(migrationFS)
	if err != nil {
		return currentVersion, fmt.Errorf("loading migrations: %w", err)
	}

	for _, m := range loaded {
		if m.Version <= currentVersion {
			continue
		}
		if m.Version > CurrentPlatformSchemaVersion {
			break
		}

		stmts := splitStatements(m.SQL)
		started := time.Now()
		logger.Info().
			Str("database_id", databaseID).
			Int("from_version", currentVersion).
			Int("to_version", m.Version).
			Int("statement_count", len(stmts)).
			Msg("Platform migration starting")

		var appliedElsewhere bool
		applyErr := executor.WithTx(ctx, databaseID, true, func(ctx context.Context, tx Tx) error {
			// The write transaction excludes every other writer, so the version read here is final until commit.
			version, err := readUserVersion(ctx, txQueryExecutor{tx: tx}, databaseID)
			if err != nil {
				return fmt.Errorf("re-reading user_version: %w", err)
			}
			if version >= m.Version {
				appliedElsewhere = true
				return nil
			}

			for _, stmt := range stmts {
				if _, err := tx.Exec(ctx, stmt, nil); err != nil {
					return fmt.Errorf("platform migration v%d: %w", m.Version, err)
				}
			}

			if err := postMigrationHook(ctx, txQueryExecutor{tx: tx}, databaseID, m.Version); err != nil {
				return fmt.Errorf("post-migration hook v%d: %w", m.Version, err)
			}

			// PRAGMA user_version is part of the database header; the write
			// is staged inside the transaction and committed atomically.
			if _, err := tx.Exec(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.Version), nil); err != nil {
				return fmt.Errorf("setting user_version to %d: %w", m.Version, err)
			}
			return nil
		})
		if applyErr != nil {
			// Classifies the ctx-cancellation reason so triage can tell apart
			// a request-side cancel from our own timeout firing from a
			// non-ctx SQL error. "none" means the failure was not driven by
			// ctx cancellation (e.g. a SQL constraint violation).
			ctxReason := "none"
			switch {
			case errors.Is(ctx.Err(), context.DeadlineExceeded):
				ctxReason = "deadline_exceeded"
			case errors.Is(ctx.Err(), context.Canceled):
				ctxReason = "canceled"
			}
			logger.Error().
				Err(applyErr).
				Str("database_id", databaseID).
				Int("from_version", currentVersion).
				Int("to_version", m.Version).
				Dur("duration", time.Since(started)).
				Str("ctx_reason", ctxReason).
				Msg("Platform migration failed")
			return currentVersion, applyErr
		}

		if appliedElsewhere {
			logger.Info().
				Str("database_id", databaseID).
				Int("version", m.Version).
				Msg("Platform migration already applied by another connection")
			currentVersion = m.Version
			continue
		}

		logger.Info().
			Str("database_id", databaseID).
			Int("version", m.Version).
			Dur("duration", time.Since(started)).
			Msg("Platform migration completed")
		currentVersion = m.Version
	}

	return currentVersion, nil
}

// migrationExecutor runs platform-table SQL: single statements with the reserved-prefix deny rules bypassed,
// and multi-statement sequences wrapped in one transaction.
type migrationExecutor interface {
	// ExecuteInternal runs a SQL statement with reserved-prefix deny rules bypassed.
	ExecuteInternal(ctx context.Context, databaseID string, sql string,
		args []domain.TypedValue) (*domain.QueryResult, error)

	// WithTx opens a transaction on the database's connection and runs fn inside it, committing on nil and
	// rolling back on error. internal controls whether tenant deny rules apply to tx.Exec calls inside fn.
	WithTx(ctx context.Context, databaseID string, internal bool, fn func(ctx context.Context, tx Tx) error) error
}

// txQueryExecutor adapts a Tx to the internalExecutor interface, so work parameterized on internalExecutor
// runs inside an already-open transaction instead of routing around it.
type txQueryExecutor struct {
	tx Tx
}

func (t txQueryExecutor) ExecuteInternal(
	ctx context.Context, _ string, sql string, args []domain.TypedValue,
) (*domain.QueryResult, error) {
	return t.tx.ExecInternal(ctx, sql, args)
}

// readUserVersion reads the current PRAGMA user_version from the SQLite database.
func readUserVersion(ctx context.Context, executor internalExecutor, databaseID string) (int, error) {
	result, err := executor.ExecuteInternal(ctx, databaseID, "PRAGMA user_version", nil)
	if err != nil {
		return 0, err
	}
	if len(result.Rows) == 0 || len(result.Rows[0]) == 0 {
		return 0, nil
	}

	val := result.Rows[0][0]
	switch v := val.Value.(type) {
	case int64:
		return int(v), nil
	case float64:
		return int(v), nil
	default:
		return 0, nil
	}
}

// loadPlatformMigrations reads .up.sql files from the given filesystem and
// returns them sorted by version number.
func loadPlatformMigrations(migrationFS fs.FS) ([]platformMigration, error) {
	// PlatformFS embeds "platform/*.sql", so files are under the "platform" directory.
	subFS, err := fs.Sub(migrationFS, "platform")
	if err != nil {
		return nil, fmt.Errorf("accessing platform subdirectory: %w", err)
	}

	entries, err := fs.ReadDir(subFS, ".")
	if err != nil {
		return nil, fmt.Errorf("reading migration directory: %w", err)
	}

	var result []platformMigration
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}

		// Parses version from filename: "000001_description.up.sql" → 1.
		parts := strings.SplitN(name, "_", 2)
		if len(parts) < 2 {
			continue
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}

		data, err := fs.ReadFile(subFS, name)
		if err != nil {
			return nil, fmt.Errorf("reading migration file %s: %w", name, err)
		}

		result = append(result, platformMigration{Version: version, SQL: string(data)})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Version < result[j].Version
	})

	return result, nil
}

// splitStatements splits a SQL string into individual statements at
// top-level semicolons, ignoring ; inside line comments (-- ... \n),
// block comments (/* ... */), and single-quoted string literals (with
// SQL's ” escape). Trims each statement and drops chunks that contain
// no runnable SQL — whitespace- or comment-only — since feeding those
// to sqlite3_prepare_v2 produces no statement and surfaces as SQLITE_MISUSE.
//
// SQLite's bracketed and backtick-quoted identifiers ([...], `...`)
// cannot contain ; or ' so they don't need separate state. Dollar-
// quoted strings ($tag$ ... $tag$) are unsupported by SQLite and not
// recognised here.
func splitStatements(sql string) []string {
	var stmts []string
	var current strings.Builder
	// hasContent tracks whether the current chunk holds any runnable SQL —
	// non-whitespace characters seen in default state (or any character
	// inside a string literal). Comment characters do not count, so
	// chunks made entirely of comments + whitespace are dropped (feeding
	// those to sqlite3_prepare_v2 produces no statement and surfaces as
	// SQLITE_MISUSE).
	hasContent := false
	flush := func() {
		s := strings.TrimSpace(current.String())
		current.Reset()
		if hasContent && s != "" {
			stmts = append(stmts, s)
		}
		hasContent = false
	}

	const (
		stateDefault = iota
		stateLineComment
		stateBlockComment
		stateStringLiteral
	)
	state := stateDefault

	for i := 0; i < len(sql); i++ {
		c := sql[i]
		switch state {
		case stateLineComment:
			current.WriteByte(c)
			if c == '\n' {
				state = stateDefault
			}
		case stateBlockComment:
			current.WriteByte(c)
			if c == '*' && i+1 < len(sql) && sql[i+1] == '/' {
				i++
				current.WriteByte(sql[i])
				state = stateDefault
			}
		case stateStringLiteral:
			current.WriteByte(c)
			if c == '\'' {
				// `''` is an escaped quote; stays inside the literal.
				if i+1 < len(sql) && sql[i+1] == '\'' {
					i++
					current.WriteByte(sql[i])
				} else {
					state = stateDefault
				}
			}
		default:
			if c == '-' && i+1 < len(sql) && sql[i+1] == '-' {
				current.WriteByte(c)
				i++
				current.WriteByte(sql[i])
				state = stateLineComment
				continue
			}
			if c == '/' && i+1 < len(sql) && sql[i+1] == '*' {
				current.WriteByte(c)
				i++
				current.WriteByte(sql[i])
				state = stateBlockComment
				continue
			}
			if c == '\'' {
				current.WriteByte(c)
				state = stateStringLiteral
				hasContent = true
				continue
			}
			if c == ';' {
				flush()
				continue
			}
			current.WriteByte(c)
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				hasContent = true
			}
		}
	}
	flush()
	return stmts
}

// InitPlatformSchema runs all platform migrations on a database.
// Intended for integration tests that need platform tables without a full DatabaseService.
func InitPlatformSchema(ctx context.Context, executor migrationExecutor, databaseID string) error {
	_, err := runPlatformMigrations(ctx, executor, databaseID, migrations.PlatformFS, zerolog.Nop())
	return err
}
