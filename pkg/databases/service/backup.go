package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// BackupStats reports the outcome of one backup sweep.
type BackupStats struct {
	Scanned            int
	Uploaded           int
	SkippedClean       int
	SkippedMissingFile int
	SkippedLowSpace    int
	SkippedForeign     int
	Failed             int

	// UploadedBytes is the total size of the snapshots uploaded during the sweep.
	UploadedBytes int64
}

// backupOutcome classifies what happened to one database during a backup attempt.
type backupOutcome int

const (
	backupUploaded backupOutcome = iota
	backupSkippedClean
	backupSkippedMissingFile
	backupSkippedLowSpace
	backupFailed
)

// backupDatabaseRepository is the metadata subset the backup service needs: enumerating databases for sweeps and
// resolving a single record for on-demand snapshots.
type backupDatabaseRepository interface {
	// ListAll returns every database record.
	ListAll(ctx context.Context) ([]*domain.Database, error)

	// FindByID returns the database with the given ID.
	// Returns domain.ErrDatabaseNotFound if not found.
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Database, error)
}

// snapshotRestorer installs downloaded snapshot files as live per-bdb database files.
type snapshotRestorer interface {
	// AdoptDatabaseFile installs the database file at srcPath as databaseID's live database file, consuming
	// srcPath on success.
	// Returns domain.ErrDatabaseFileExists when a live file is already present and force is false.
	AdoptDatabaseFile(ctx context.Context, databaseID string, srcPath string, force bool) error
}

// snapshotStore persists snapshot files in a durable object store.
type snapshotStore interface {
	// UploadSnapshot stores the snapshot file at srcPath for databaseID, keyed by takenAt.
	UploadSnapshot(
		ctx context.Context, databaseID string, srcPath string, takenAt time.Time,
	) (domain.SnapshotInfo, error)

	// LatestSnapshot returns the most recent stored snapshot for databaseID.
	// Returns domain.ErrNoSnapshots when none exist.
	LatestSnapshot(ctx context.Context, databaseID string) (domain.SnapshotInfo, error)

	// DownloadSnapshot downloads the object at key to destPath (which must not exist yet).
	// Returns domain.ErrSnapshotNotFound when the object does not exist.
	DownloadSnapshot(ctx context.Context, key string, destPath string) error
}

// BackupService snapshots per-bdb databases into durable object storage. A sweep backs up every database whose
// file changed since its last stored snapshot (plus a freshness floor for idle databases, so the store's lifecycle
// expiry never deletes a database's only snapshot). Per-database errors are logged and counted but never abort a
// sweep — a full disk or a broken store degrades backups, never user traffic.
type BackupService struct {
	repo           backupDatabaseRepository
	snapshots      snapshotter
	restorer       snapshotRestorer
	store          snapshotStore
	stagingDir     string
	maxSnapshotAge time.Duration
	logger         zerolog.Logger

	// shardOrdinal is this pod's shard. A database on another shard lives on another pod's volume, so this
	// pod reconciles only its own. 0 in single-pod deployments.
	shardOrdinal int16

	// podColor is this pod's blue/green fleet (0 = blue, 1 = green). Together with shardOrdinal it decides which
	// databases this pod owns. 0 in single-fleet deployments.
	podColor int16

	// lastBackup caches the taken-at of each database's newest stored snapshot, seeded lazily from the store.
	// Guarded by mu. Each database has a single owning instance, so the cache only lags the store across
	// transient ownership handoffs (instance restart, database moved between instances) — and staleness can
	// only make a backup more eager: the cost is a redundant snapshot, never a missed one.
	mu         sync.Mutex
	lastBackup map[uuid.UUID]time.Time
}

// NewBackupService creates a BackupService staging snapshots in stagingDir, which must sit on the same volume as
// the database files (a snapshot is a full database copy — container ephemeral storage is too small). The directory
// is created if needed and swept of leftovers from interrupted runs.
func NewBackupService(
	repo backupDatabaseRepository,
	snapshots snapshotter,
	restorer snapshotRestorer,
	store snapshotStore,
	stagingDir string,
	maxSnapshotAge time.Duration,
	logger zerolog.Logger,
) (*BackupService, error) {
	if stagingDir == "" {
		return nil, errors.New("backup: stagingDir is required")
	}
	if maxSnapshotAge <= 0 {
		return nil, errors.New("backup: maxSnapshotAge must be positive")
	}
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return nil, fmt.Errorf("backup: creating staging dir %q: %w", stagingDir, err)
	}

	s := &BackupService{
		repo:           repo,
		snapshots:      snapshots,
		restorer:       restorer,
		store:          store,
		stagingDir:     stagingDir,
		maxSnapshotAge: maxSnapshotAge,
		logger:         logger,
		lastBackup:     make(map[uuid.UUID]time.Time),
	}
	s.sweepStagingDir()
	return s, nil
}

// SetShardOrdinal sets this pod's shard ordinal.
func (s *BackupService) SetShardOrdinal(ordinal int16) {
	s.shardOrdinal = ordinal
}

// SetPodColor sets this pod's blue/green fleet color.
func (s *BackupService) SetPodColor(color int16) {
	s.podColor = color
}

// SweepOnce backs up every database that is dirty (file changed since its last stored snapshot) or stale (newest
// snapshot older than the freshness floor). Returns aggregate stats; per-database failures are logged, counted, and
// skipped over.
func (s *BackupService) SweepOnce(ctx context.Context, now time.Time) (BackupStats, error) {
	dbs, err := s.repo.ListAll(ctx)
	if err != nil {
		return BackupStats{}, fmt.Errorf("backup: listing databases: %w", err)
	}

	stats := BackupStats{Scanned: len(dbs)}
	for _, db := range dbs {
		// Skips databases owned by another pod (different fleet color or shard): their files live on other pods'
		// volumes. Without this they would land in SkippedMissingFile, conflating "not mine" with a genuinely
		// missing owned file.
		if !db.OwnedBy(s.podColor, s.shardOrdinal) {
			stats.SkippedForeign++
			continue
		}

		info, outcome, err := s.backupOne(ctx, db.ID, now, false)
		switch outcome {
		case backupUploaded:
			stats.Uploaded++
			stats.UploadedBytes += info.SizeBytes
		case backupSkippedClean:
			stats.SkippedClean++
		case backupSkippedMissingFile:
			stats.SkippedMissingFile++
		case backupSkippedLowSpace:
			stats.SkippedLowSpace++
		case backupFailed:
			stats.Failed++
			s.logger.Error().Err(err).
				Str("database_id", db.ID.String()).
				Msg("Backup: database backup failed; continuing sweep")
		}
	}
	return stats, nil
}

// BackupDatabase snapshots and uploads one database immediately, bypassing the dirty-check. Returns
// domain.ErrDatabaseNotFound for an unknown database and domain.ErrDatabaseFileMissing when the record exists but
// no file is on disk.
func (s *BackupService) BackupDatabase(
	ctx context.Context, databaseID uuid.UUID, now time.Time,
) (domain.SnapshotInfo, error) {
	if _, err := s.repo.FindByID(ctx, databaseID); err != nil {
		return domain.SnapshotInfo{}, fmt.Errorf("finding database: %w", err)
	}

	info, outcome, err := s.backupOne(ctx, databaseID, now, true)
	switch outcome {
	case backupUploaded:
		return info, nil
	case backupSkippedMissingFile:
		return domain.SnapshotInfo{}, fmt.Errorf("%w: %q", domain.ErrDatabaseFileMissing, databaseID)
	case backupSkippedLowSpace:
		return domain.SnapshotInfo{}, fmt.Errorf("backup: not enough free staging space for %q", databaseID)
	default:
		return domain.SnapshotInfo{}, err
	}
}

// LatestSnapshot returns the most recent stored snapshot for the database, or domain.ErrNoSnapshots when the
// store holds none. It recovers a move whose source no longer has the live file: the latest snapshot is the
// newest available copy, and a database is read_only for the duration of a move, so no writes are lost after it.
func (s *BackupService) LatestSnapshot(ctx context.Context, databaseID uuid.UUID) (domain.SnapshotInfo, error) {
	return s.store.LatestSnapshot(ctx, databaseID.String())
}

// backupOne runs the backup pipeline for a single database: due-check (unless forced), free-space guard, snapshot
// into the staging dir, upload, staging cleanup.
func (s *BackupService) backupOne(
	ctx context.Context, databaseID uuid.UUID, now time.Time, force bool,
) (domain.SnapshotInfo, backupOutcome, error) {
	dbStats, err := s.snapshots.GetDatabaseStats(ctx, databaseID.String())
	if err != nil {
		return domain.SnapshotInfo{}, backupFailed, fmt.Errorf("reading database stats: %w", err)
	}
	if dbStats.LastModifiedAt.IsZero() {
		// No file on disk — deleted concurrently, or this pod doesn't hold it.
		return domain.SnapshotInfo{}, backupSkippedMissingFile, nil
	}

	if !force {
		lastTaken, err := s.lastTakenAt(ctx, databaseID)
		if err != nil {
			return domain.SnapshotInfo{}, backupFailed, fmt.Errorf("resolving last snapshot time: %w", err)
		}
		dirty := dbStats.LastModifiedAt.After(lastTaken)
		stale := now.Sub(lastTaken) >= s.maxSnapshotAge // lastTaken zero value ⇒ stale, so new databases are due
		if !dirty && !stale {
			return domain.SnapshotInfo{}, backupSkippedClean, nil
		}
	}

	free, err := freeSpaceBytes(s.stagingDir)
	if err != nil {
		return domain.SnapshotInfo{}, backupFailed, fmt.Errorf("checking staging free space: %w", err)
	}
	if need := int64(dbStats.StorageBytesUsed); free < need {
		// Deliberately a skip, not a failure-and-retry-now: a full volume must degrade backups, never wedge
		// user writes. These warnings are the capacity alarm — see the deploy runbooks.
		s.logger.Warn().
			Str("database_id", databaseID.String()).
			Int64("free_bytes", free).
			Int64("needed_bytes", need).
			Msg("Backup: not enough free staging space; skipping database")
		return domain.SnapshotInfo{}, backupSkippedLowSpace, nil
	}

	stagingPath := s.stagingPath(databaseID, "snapshot")
	if err := s.snapshots.Snapshot(ctx, databaseID.String(), stagingPath); err != nil {
		if errors.Is(err, domain.ErrDatabaseFileMissing) {
			return domain.SnapshotInfo{}, backupSkippedMissingFile, nil
		}
		return domain.SnapshotInfo{}, backupFailed, fmt.Errorf("snapshotting: %w", err)
	}
	defer func() {
		if err := os.Remove(stagingPath); err != nil {
			s.logger.Warn().Err(err).Str("path", stagingPath).Msg("backup: removing staged snapshot")
		}
	}()

	info, err := s.store.UploadSnapshot(ctx, databaseID.String(), stagingPath, now)
	if err != nil {
		return domain.SnapshotInfo{}, backupFailed, fmt.Errorf("uploading: %w", err)
	}

	s.mu.Lock()
	s.lastBackup[databaseID] = now
	s.mu.Unlock()

	s.logger.Info().
		Str("database_id", databaseID.String()).
		Str("snapshot_key", info.Key).
		Int64("size_bytes", info.SizeBytes).
		Msg("Backup: snapshot uploaded")
	return info, backupUploaded, nil
}

// lastTakenAt returns the taken-at of the database's newest stored snapshot, from the in-process cache or, on
// first sight, from the store. Zero when no snapshot exists.
func (s *BackupService) lastTakenAt(ctx context.Context, databaseID uuid.UUID) (time.Time, error) {
	s.mu.Lock()
	last, ok := s.lastBackup[databaseID]
	s.mu.Unlock()
	if ok {
		return last, nil
	}

	info, err := s.store.LatestSnapshot(ctx, databaseID.String())
	switch {
	case errors.Is(err, domain.ErrNoSnapshots):
		last = time.Time{}
	case err != nil:
		return time.Time{}, err
	default:
		last = info.TakenAt
	}

	s.mu.Lock()
	s.lastBackup[databaseID] = last
	s.mu.Unlock()
	return last, nil
}

// stagingPath returns a unique path in the staging dir for one snapshot or restore attempt. Uniqueness (the
// nanosecond timestamp) is the only requirement — names are never parsed back, and the boot sweep removes
// leftovers regardless of shape; kind just keeps debugging legible.
func (s *BackupService) stagingPath(databaseID uuid.UUID, kind string) string {
	return filepath.Join(s.stagingDir, fmt.Sprintf("%s-%s-%d.db", databaseID, kind, time.Now().UnixNano()))
}

// sweepStagingDir removes leftover files from interrupted snapshot runs. Best-effort: failures are logged, the
// service still starts.
func (s *BackupService) sweepStagingDir() {
	entries, err := os.ReadDir(s.stagingDir)
	if err != nil {
		s.logger.Warn().Err(err).Str("dir", s.stagingDir).Msg("backup: reading staging dir for sweep")
		return
	}
	for _, entry := range entries {
		path := filepath.Join(s.stagingDir, entry.Name())
		if err := os.Remove(path); err != nil {
			s.logger.Warn().Err(err).Str("path", path).Msg("backup: removing leftover staged snapshot")
			continue
		}
		s.logger.Info().Str("path", path).Msg("backup: removed leftover staged snapshot")
	}
}

// freeSpaceBytes returns the bytes available to unprivileged writes on the filesystem holding path.
func freeSpaceBytes(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs %q: %w", path, err)
	}
	return int64(st.Bavail) * int64(st.Bsize), nil //nolint:unconvert // Bavail/Bsize types differ across platforms.
}
