package service

import (
	"context"
)

// MigrateStats reports the outcome of one MigrateOwnedOnBoot pass.
type MigrateStats struct {
	Scanned         int
	SkippedReadOnly int
	AtHead          int
	Migrated        int
	Failed          int
}

// MigrateOwnedOnBoot walks every database record this pod owns and runs any pending platform migrations, so a
// database that no request ever touches still reaches CurrentPlatformSchemaVersion instead of staying pinned on
// an old version indefinitely. Only this pod's records are listed — other pods' files live on other volumes.
//
// Read-only databases (mid-move hand-off) are skipped: their file is about to be adopted elsewhere, and they
// level lazily once the move completes and clears the flag. Databases whose mirrored version is already at head
// are counted without opening their file, so a fully-leveled fleet makes this pass close to free.
//
// The pass is safe alongside live traffic: migration runs serialize per database (the same lock
// EnsurePlatformSchema always takes), so a concurrent first touch of a database mid-migration waits briefly on
// that database's lock and then no-ops — it never sees a half-migrated schema.
//
// Per-database failures are logged and counted, never abort the pass: a database that fails here fails
// identically on its next lazy first touch, so there is nothing better to do than surface it. Cancelling ctx
// stops the pass between databases; an in-flight migration still completes, since its apply context is detached
// from cancellation.
func (s *DatabaseService) MigrateOwnedOnBoot(ctx context.Context) MigrateStats {
	stats := MigrateStats{}
	dbs, err := s.repo.ListOwned(ctx, s.podColor, s.shardOrdinal)
	if err != nil {
		s.logger.Error().Err(err).Msg("platform migrate: listing owned databases failed")
		return stats
	}
	stats.Scanned = len(dbs)

	for _, db := range dbs {
		if ctx.Err() != nil {
			s.logger.Warn().Msg("platform migrate: boot pass cancelled before completion")
			break
		}
		switch {
		case db.ReadOnly:
			stats.SkippedReadOnly++
		case db.PlatformSchemaVersion >= CurrentPlatformSchemaVersion:
			stats.AtHead++
		default:
			if err := s.EnsurePlatformSchema(ctx, db); err != nil {
				stats.Failed++
				s.logger.Error().Err(err).
					Str("database_id", db.ID.String()).
					Msg("Platform migrate: migrating database failed")
			} else {
				stats.Migrated++
			}
		}
	}

	s.logger.Info().
		Int("scanned", stats.Scanned).
		Int("skipped_read_only", stats.SkippedReadOnly).
		Int("at_head", stats.AtHead).
		Int("migrated", stats.Migrated).
		Int("failed", stats.Failed).
		Msg("Platform migrate: boot pass complete")
	return stats
}
