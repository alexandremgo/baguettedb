package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// defaultSizeRefreshInterval coalesces stored-size refreshes: a database's size is re-stated and persisted
// at most once per interval, so write bursts cost one stat + one Postgres update instead of one per batch.
const defaultSizeRefreshInterval = 5 * time.Second

// defaultOwnershipCacheTTL bounds how long ownership lookups against the bdbs module (a user's owned
// bdb IDs, a bdb's owner + roles) are reused. No invalidation needed: a just-created bdb missing from
// a cached list, or a stale role set, only lags enforcement by up to the TTL, which the lag-tolerant
// semantics accept.
const defaultOwnershipCacheTTL = 30 * time.Second

// ownershipCacheMaxEntries caps each ownership cache map so it cannot grow unbounded across many
// distinct keys; crossing the cap wipes that cache — correctness never depends on a hit, and entries
// rebuild on the next lookup at one cheap bdbs call each.
const ownershipCacheMaxEntries = 4096

// ownedBdbIDsEntry is one cached owned-bdb-ids lookup.
type ownedBdbIDsEntry struct {
	ids       []uuid.UUID
	fetchedAt time.Time
}

// bdbOwnerEntry is one cached bdb-owner lookup.
type bdbOwnerEntry struct {
	ownerID   uuid.UUID
	roles     []authz.Role
	fetchedAt time.Time
}

// SetSizeRefreshInterval overrides the minimum delay between two stored-size refreshes of the same
// database. 0 disables coalescing so every refresh hits the metadata store.
func (s *DatabaseService) SetSizeRefreshInterval(d time.Duration) {
	s.sizeRefreshInterval = d
}

// RefreshStoredSize re-stats the database's on-disk size and mirrors it to the metadata store,
// coalesced per database (skipped when the last refresh is younger than the refresh interval).
// Best-effort: failures are logged, never propagated — enforcement reads tolerate a stale size,
// and the next write retries.
func (s *DatabaseService) RefreshStoredSize(ctx context.Context, databaseID string) {
	// Housekeeping only — skipped on a partially wired service (query-path-only test constructions
	// carry no database manager or metadata store).
	if s.dbMgr == nil || s.repo == nil {
		return
	}

	id, err := uuid.Parse(databaseID)
	if err != nil {
		s.logger.Error().Err(err).Str("database_id", databaseID).Msg("size refresh: invalid database ID")
		return
	}

	last, ok := s.lastSizeRefresh.Load(id)
	if ok && time.Since(last.(time.Time)) < s.sizeRefreshInterval {
		return
	}

	stats, err := s.dbMgr.GetDatabaseStats(ctx, databaseID)
	if err != nil {
		s.logger.Error().Err(err).Str("database_id", databaseID).Msg("size refresh: reading database stats failed")
		return
	}

	s.lastSizeRefresh.Store(id, time.Now())
	if err := s.repo.UpdateSize(ctx, id, int64(stats.StorageBytesUsed)); err != nil {
		s.logger.Error().Err(err).
			Str("database_id", databaseID).
			Uint64("size_bytes", stats.StorageBytesUsed).
			Msg("Size refresh: persisting size failed")
		s.lastSizeRefresh.Delete(id)
	}
}

// SetOwnershipCacheTTL overrides how long ownership lookups (owned bdb IDs, bdb owner + roles) are
// reused. 0 disables caching so every check reads through to the bdbs module.
func (s *DatabaseService) SetOwnershipCacheTTL(d time.Duration) {
	s.ownershipCacheTTL = d
}

// TotalOwnedSizeBytes returns the summed tracked size of every database whose bdb the user owns,
// including soft-deleted bdbs still awaiting purge. Near-real-time, not byte-exact: the per-database
// sizes refresh with a lag and the owned-bdb-ids lookup is cached briefly.
func (s *DatabaseService) TotalOwnedSizeBytes(ctx context.Context, ownerID uuid.UUID) (int64, error) {
	ids, err := s.ownedBdbIDs(ctx, ownerID)
	if err != nil {
		return 0, fmt.Errorf("resolving owned bdb ids: %w", err)
	}

	total, err := s.repo.SumSizeBytesByBdbIDs(ctx, ids)
	if err != nil {
		return 0, fmt.Errorf("summing owned database sizes: %w", err)
	}
	return total, nil
}

// ownedBdbIDs returns the user's owned bdb IDs, reusing a cached lookup while it is younger than the
// cache TTL.
func (s *DatabaseService) ownedBdbIDs(ctx context.Context, ownerID uuid.UUID) ([]uuid.UUID, error) {
	s.ownershipMu.Lock()
	entry, ok := s.ownedBdbIDsCache[ownerID]
	s.ownershipMu.Unlock()
	if ok && time.Since(entry.fetchedAt) < s.ownershipCacheTTL {
		return entry.ids, nil
	}

	ids, err := s.bdbsApp.GetOwnedBdbIDs(ctx, ownerID)
	if err != nil {
		return nil, err
	}

	s.ownershipMu.Lock()
	if s.ownedBdbIDsCache == nil || len(s.ownedBdbIDsCache) >= ownershipCacheMaxEntries {
		s.ownedBdbIDsCache = make(map[uuid.UUID]ownedBdbIDsEntry)
	}
	s.ownedBdbIDsCache[ownerID] = ownedBdbIDsEntry{ids: ids, fetchedAt: time.Now()}
	s.ownershipMu.Unlock()
	return ids, nil
}

// bdbOwner returns the bdb owner's user ID and global roles, reusing a cached lookup while it is
// younger than the cache TTL. Cached because the aggregate quota check runs on every size-growing
// write; a role change (e.g. a plan upgrade) takes effect within the TTL.
func (s *DatabaseService) bdbOwner(ctx context.Context, bdbID uuid.UUID) (uuid.UUID, []authz.Role, error) {
	s.ownershipMu.Lock()
	entry, ok := s.bdbOwnerCache[bdbID]
	s.ownershipMu.Unlock()
	if ok && time.Since(entry.fetchedAt) < s.ownershipCacheTTL {
		return entry.ownerID, entry.roles, nil
	}

	ownerID, roles, err := s.bdbsApp.GetBdbOwner(ctx, bdbID)
	if err != nil {
		return uuid.Nil, nil, err
	}

	s.ownershipMu.Lock()
	if s.bdbOwnerCache == nil || len(s.bdbOwnerCache) >= ownershipCacheMaxEntries {
		s.bdbOwnerCache = make(map[uuid.UUID]bdbOwnerEntry)
	}
	s.bdbOwnerCache[bdbID] = bdbOwnerEntry{ownerID: ownerID, roles: roles, fetchedAt: time.Now()}
	s.ownershipMu.Unlock()
	return ownerID, roles, nil
}

// CheckOwnerStorageQuota rejects with domain.ErrStorageQuotaExceeded when the bdb owner's total
// tracked storage has reached their aggregate cap. Callers gate size-growing operations with it;
// reads, deletes, and drops must not be gated so an over-budget owner can always free space.
func (s *DatabaseService) CheckOwnerStorageQuota(ctx context.Context, bdbID uuid.UUID) error {
	ownerID, ownerRoles, err := s.bdbOwner(ctx, bdbID)
	if err != nil {
		return fmt.Errorf("getting bdb owner: %w", err)
	}
	return s.checkOwnerStorageQuota(ctx, ownerID, ownerRoles)
}

// checkOwnerStorageQuota is CheckOwnerStorageQuota for callers that already resolved the owner.
func (s *DatabaseService) checkOwnerStorageQuota(
	ctx context.Context, ownerID uuid.UUID, ownerRoles []authz.Role,
) error {
	limit := s.limits(ownerRoles).MaxTotalDBSizeBytes
	if limit <= 0 {
		return nil
	}

	total, err := s.TotalOwnedSizeBytes(ctx, ownerID)
	if err != nil {
		return fmt.Errorf("computing owner storage total: %w", err)
	}
	if total >= limit {
		return fmt.Errorf("%w (%d bytes tracked, limit %d)", domain.ErrStorageQuotaExceeded, total, limit)
	}
	return nil
}

// AccountStorage returns the user's account-wide storage usage: the tracked total across every bdb
// they own, and their aggregate cap (0 = unlimited). roles are the caller's own (the account is
// always the calling user's), so no ownership lookup is needed for the limit.
func (s *DatabaseService) AccountStorage(
	ctx context.Context, userID uuid.UUID, roles []authz.Role,
) (usedBytes, limitBytes int64, err error) {
	total, err := s.TotalOwnedSizeBytes(ctx, userID)
	if err != nil {
		return 0, 0, fmt.Errorf("computing account storage total: %w", err)
	}
	return total, s.limits(roles).MaxTotalDBSizeBytes, nil
}

// BackfillStoredSizes stats every database file this pod owns and persists the sizes, so tracked sizes
// start truthful after boot (new column, restored volume, or refreshes missed while the pod was down).
// Failures are logged per database and reported in the returned count; the sweep never aborts.
func (s *DatabaseService) BackfillStoredSizes(ctx context.Context) (updated, failed int) {
	dbs, err := s.repo.ListAll(ctx)
	if err != nil {
		s.logger.Error().Err(err).Msg("size backfill: listing databases failed")
		return 0, 1
	}

	for _, db := range dbs {
		if !db.OwnedBy(s.podColor, s.shardOrdinal) {
			continue
		}
		databaseID := db.ID.String()
		stats, err := s.dbMgr.GetDatabaseStats(ctx, databaseID)
		if err != nil {
			failed++
			s.logger.Error().Err(err).Str("database_id", databaseID).Msg("size backfill: reading stats failed")
			continue
		}
		if err := s.repo.UpdateSize(ctx, db.ID, int64(stats.StorageBytesUsed)); err != nil {
			failed++
			s.logger.Error().Err(err).Str("database_id", databaseID).Msg("size backfill: persisting size failed")
			continue
		}
		updated++
	}

	s.logger.Info().
		Int("scanned", len(dbs)).
		Int("updated", updated).
		Int("failed", failed).
		Msg("Size backfill: pass complete")
	return updated, failed
}
