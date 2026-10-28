package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// reconcileFakeRepo serves a fixed set of database records to ReconcileOnBoot.
type reconcileFakeRepo struct {
	dbs []*domain.Database
}

func (r *reconcileFakeRepo) ListAll(context.Context) ([]*domain.Database, error) { return r.dbs, nil }

func (r *reconcileFakeRepo) FindByID(context.Context, uuid.UUID) (*domain.Database, error) {
	return nil, domain.ErrDatabaseNotFound
}

// reconcileFakeSnapshotter records which database IDs had their on-disk stats read, so a test can assert that a
// database owned by another shard is skipped before it is ever examined (and therefore never restored locally).
type reconcileFakeSnapshotter struct {
	statsCalls []string
}

func (s *reconcileFakeSnapshotter) Snapshot(context.Context, string, string) error { return nil }

func (s *reconcileFakeSnapshotter) Export(context.Context, string, string, string) error { return nil }

func (s *reconcileFakeSnapshotter) GetDatabaseStats(_ context.Context, id string) (*domain.DatabaseStats, error) {
	s.statsCalls = append(s.statsCalls, id)
	// A non-zero LastModifiedAt means a live file exists, so reconcile counts it present and never restores it —
	// keeping the test focused on the ownership filter rather than the restore path.
	return &domain.DatabaseStats{LastModifiedAt: time.Unix(1, 0)}, nil
}

// TestReconcileOnBoot_SkipsForeignShards verifies the boot reconcile only touches databases this pod owns: a
// database whose pod_shard differs from this pod's ordinal is skipped before its stats are read, so it is never
// restored onto the wrong pod. This is the per-shard guarantee that, combined with setting the ordinal before the
// reconcile runs, prevents a pod from pulling the whole fleet's databases onto its volume.
func TestReconcileOnBoot_SkipsForeignShards(t *testing.T) {
	shard0DB := &domain.Database{ID: uuid.New(), PodShard: 0}
	shard1DB := &domain.Database{ID: uuid.New(), PodShard: 1}

	tests := []struct {
		name        string
		ordinal     int16
		wantOwned   uuid.UUID
		wantForeign uuid.UUID
	}{
		{
			name:        "pod on shard 0 skips the shard-1 database",
			ordinal:     0,
			wantOwned:   shard0DB.ID,
			wantForeign: shard1DB.ID,
		},
		{
			name:        "pod on shard 1 skips the shard-0 database",
			ordinal:     1,
			wantOwned:   shard1DB.ID,
			wantForeign: shard0DB.ID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snaps := &reconcileFakeSnapshotter{}
			svc := &BackupService{
				repo:         &reconcileFakeRepo{dbs: []*domain.Database{shard0DB, shard1DB}},
				snapshots:    snaps,
				shardOrdinal: tt.ordinal,
				logger:       zerolog.Nop(),
			}

			stats, err := svc.ReconcileOnBoot(context.Background())
			if err != nil {
				t.Fatalf("ReconcileOnBoot: %v", err)
			}

			if stats.Scanned != 2 {
				t.Errorf("Scanned = %d, want 2", stats.Scanned)
			}
			if stats.SkippedForeign != 1 {
				t.Errorf("SkippedForeign = %d, want 1", stats.SkippedForeign)
			}
			if stats.Restored != 0 {
				t.Errorf("Restored = %d, want 0 (foreign db must never be restored here)", stats.Restored)
			}

			// The foreign database must be skipped before any examination: if its stats were read at all, the
			// reconcile would go on to restore it locally — exactly the bug this guards against.
			if len(snaps.statsCalls) != 1 || snaps.statsCalls[0] != tt.wantOwned.String() {
				t.Errorf("GetDatabaseStats calls = %v, want exactly [%s]", snaps.statsCalls, tt.wantOwned)
			}
			for _, id := range snaps.statsCalls {
				if id == tt.wantForeign.String() {
					t.Errorf("foreign database %s was examined; it must be skipped entirely", tt.wantForeign)
				}
			}
		})
	}
}
