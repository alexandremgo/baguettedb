package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
)

// purgeLock serializes the soft-delete purge sweep across replicas. TryAcquire takes a non-blocking,
// session-scoped lock that exactly one holder owns at a time, so two pods ticking at once don't both run the
// sweep — which would duplicate work and log spurious per-bdb failures as they race to delete the same rows.
// A caller that does not acquire skips its sweep; the holder must call the returned release once the sweep finishes.
type purgeLock interface {
	// TryAcquire attempts to take the sweep lock without blocking. When acquired is true the caller holds the lock
	// and must call release once the sweep is done; when false another replica already holds it.
	TryAcquire(ctx context.Context) (acquired bool, release func(context.Context), err error)
}

// PurgeStats reports the outcome of one PurgeExpired pass.
type PurgeStats struct {
	Scanned int
	Purged  int
	Failed  int
}

// PurgeExpired hard-deletes bdbs whose grace period has expired as of now. For each purgeable bdb the per-bdb
// database file is removed first, then the bdb row, so a transient file-delete failure leaves the row in place for
// the next pass instead of orphaning the file. Per-bdb errors are logged and counted in PurgeStats.Failed but never
// abort the sweep.
func (s *BdbService) PurgeExpired(ctx context.Context, now time.Time) (PurgeStats, error) {
	ctx, span := tracer.Start(ctx, "BdbService.PurgeExpired",
		trace.WithAttributes(attribute.String("now", now.Format(time.RFC3339))),
	)
	defer span.End()

	if s.dbApp == nil {
		s.logger.Warn().Msg("purge: databases module not yet wired; skipping sweep")
		return PurgeStats{}, nil
	}

	// Only one replica purges per tick: a session-scoped advisory lock makes the sweep single-flight across pods,
	// so a pod that doesn't get the lock no-ops cleanly instead of racing the holder to delete the same rows.
	if s.purgeLock != nil {
		acquired, release, err := s.purgeLock.TryAcquire(ctx)
		if err != nil {
			return PurgeStats{}, fmt.Errorf("acquiring purge lock: %w", err)
		}
		if !acquired {
			s.logger.Debug().Msg("purge: another replica holds the sweep lock; skipping this tick")
			return PurgeStats{}, nil
		}
		defer release(ctx)
	}

	cutoff := now.Add(-domain.GracePeriod)
	ids, err := s.repo.ListPurgeable(ctx, cutoff)
	if err != nil {
		return PurgeStats{}, fmt.Errorf("listing purgeable bdbs: %w", err)
	}

	stats := PurgeStats{Scanned: len(ids)}

	for _, id := range ids {
		if err := s.dbApp.DeleteDatabase(ctx, id); err != nil {
			stats.Failed++
			s.logger.Error().Err(err).
				Str("bdb_id", id.String()).
				Msg("Purge: failed to delete per-bdb database; keeping row for retry")
			continue
		}

		if err := s.repo.HardDelete(ctx, id); err != nil {
			stats.Failed++
			if errors.Is(err, domain.ErrBdbNotFound) {
				// Already purged by a previous sweep — treat as success but log.
				s.logger.Warn().
					Str("bdb_id", id.String()).
					Msg("Purge: bdb row already gone after database delete")
				stats.Failed--
				stats.Purged++
				continue
			}
			s.logger.Error().Err(err).
				Str("bdb_id", id.String()).
				Msg("Purge: failed to hard-delete bdb row after database delete")
			continue
		}

		stats.Purged++
		s.logger.Info().
			Str("bdb_id", id.String()).
			Msg("Purged soft-deleted bdb")
	}

	return stats, nil
}
