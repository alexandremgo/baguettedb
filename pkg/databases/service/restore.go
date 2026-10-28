package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// ReconcileStats reports the outcome of one ReconcileOnBoot pass.
type ReconcileStats struct {
	Scanned        int
	FilePresent    int
	Restored       int
	NoSnapshot     int
	SkippedForeign int
	Failed         int
}

// RestoreDatabase downloads the database's latest stored snapshot and installs it as the live database file.
// force overwrites an existing live file (losing any writes newer than the snapshot); without force an existing
// file yields domain.ErrDatabaseFileExists. Returns domain.ErrNoSnapshots when the store holds nothing to restore.
func (s *BackupService) RestoreDatabase(
	ctx context.Context, databaseID uuid.UUID, force bool,
) (domain.SnapshotInfo, error) {
	info, err := s.store.LatestSnapshot(ctx, databaseID.String())
	if err != nil {
		return domain.SnapshotInfo{}, fmt.Errorf("resolving latest snapshot: %w", err)
	}

	stagingPath := s.stagingPath(databaseID, "restore")
	if err := s.store.DownloadSnapshot(ctx, info.Key, stagingPath); err != nil {
		return domain.SnapshotInfo{}, fmt.Errorf("downloading snapshot %q: %w", info.Key, err)
	}
	// Adoption consumes the staged file on success; this removal only fires on failure paths.
	defer func() {
		if err := os.Remove(stagingPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.logger.Warn().Err(err).Str("path", stagingPath).Msg("restore: removing staged snapshot")
		}
	}()

	if err := s.restorer.AdoptDatabaseFile(ctx, databaseID.String(), stagingPath, force); err != nil {
		return domain.SnapshotInfo{}, fmt.Errorf("adopting snapshot %q: %w", info.Key, err)
	}

	s.logger.Info().
		Str("database_id", databaseID.String()).
		Str("snapshot_key", info.Key).
		Time("taken_at", info.TakenAt).
		Bool("force", force).
		Msg("Restore: database restored from snapshot")
	return info, nil
}

// AdoptSnapshot downloads the snapshot at key and installs it as databaseID's live file, overwriting any existing
// file. Unlike RestoreDatabase (which picks the latest snapshot for fresh-volume recovery) this takes an explicit
// snapshot ref, so a move adopts exactly the snapshot release produced. The DEK is verified separately, after the
// file is in place.
func (s *BackupService) AdoptSnapshot(ctx context.Context, databaseID uuid.UUID, key string) error {
	// Guard against clobbering live data: adopt force-overwrites the local file, so refuse while this shard
	// already owns the database — adopt is only safe on the move target, which doesn't own it yet.
	db, err := s.repo.FindByID(ctx, databaseID)
	if err != nil {
		return fmt.Errorf("finding database %s: %w", databaseID, err)
	}
	if db.OwnedBy(s.podColor, s.shardOrdinal) {
		return fmt.Errorf("%w: refusing to adopt %s", domain.ErrDatabaseOwnedHere, databaseID)
	}

	stagingPath := s.stagingPath(databaseID, "adopt")
	if err := s.store.DownloadSnapshot(ctx, key, stagingPath); err != nil {
		return fmt.Errorf("downloading snapshot %q: %w", key, err)
	}
	// Adoption consumes the staged file on success; this removal only fires on failure paths.
	defer func() {
		if err := os.Remove(stagingPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.logger.Warn().Err(err).Str("path", stagingPath).Msg("adopt: removing staged snapshot")
		}
	}()

	if err := s.restorer.AdoptDatabaseFile(ctx, databaseID.String(), stagingPath, true); err != nil {
		return fmt.Errorf("adopting snapshot %q: %w", key, err)
	}
	s.logger.Info().Str("database_id", databaseID.String()).Str("snapshot_key", key).
		Msg("Adopt: snapshot installed from ref")
	return nil
}

// ReconcileOnBoot restores every database record this pod owns whose file is missing on disk from its latest
// stored snapshot — the recovery path for an instance starting on a fresh or wiped volume. Restored data is as
// old as the last backup (accepted RPO = the backup interval). Databases owned by other shards are skipped (their
// files live on other pods' volumes) and counted in SkippedForeign. Per-database problems are logged and counted,
// never abort the pass: a database with no snapshot at all is unrecoverable here and counted in NoSnapshot,
// transient errors in Failed.
func (s *BackupService) ReconcileOnBoot(ctx context.Context) (ReconcileStats, error) {
	dbs, err := s.repo.ListAll(ctx)
	if err != nil {
		return ReconcileStats{}, fmt.Errorf("reconcile: listing databases: %w", err)
	}

	stats := ReconcileStats{Scanned: len(dbs)}
	for _, db := range dbs {
		// Skips databases owned by another pod (different fleet color or shard): their files live on other pods'
		// volumes, so restoring them here would land a copy on the wrong pod.
		if !db.OwnedBy(s.podColor, s.shardOrdinal) {
			stats.SkippedForeign++
			continue
		}

		dbStats, err := s.snapshots.GetDatabaseStats(ctx, db.ID.String())
		if err != nil {
			stats.Failed++
			s.logger.Error().Err(err).
				Str("database_id", db.ID.String()).
				Msg("Reconcile: reading database stats failed")
			continue
		}
		if !dbStats.LastModifiedAt.IsZero() {
			stats.FilePresent++
			continue
		}

		_, err = s.RestoreDatabase(ctx, db.ID, false)
		switch {
		case err == nil:
			stats.Restored++
		case errors.Is(err, domain.ErrNoSnapshots):
			// Unrecoverable from here: the database was created but never backed up before the file was
			// lost. Surfaced loudly but not fatally — failing startup forever would not bring the data back.
			stats.NoSnapshot++
			s.logger.Error().
				Str("database_id", db.ID.String()).
				Msg("Reconcile: database file missing and no snapshot exists; data is unrecoverable")
		default:
			stats.Failed++
			s.logger.Error().Err(err).
				Str("database_id", db.ID.String()).
				Msg("Reconcile: restoring database failed")
		}
	}

	if stats.Restored > 0 || stats.NoSnapshot > 0 || stats.Failed > 0 {
		s.logger.Info().
			Int("scanned", stats.Scanned).
			Int("file_present", stats.FilePresent).
			Int("restored", stats.Restored).
			Int("no_snapshot", stats.NoSnapshot).
			Int("skipped_foreign", stats.SkippedForeign).
			Int("failed", stats.Failed).
			Msg("Reconcile: boot pass complete")
	}
	return stats, nil
}
