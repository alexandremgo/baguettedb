//go:build integration

package integrationtest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// shardOf reads a database row's pod_shard directly from Postgres.
func shardOf(t *testing.T, databaseID uuid.UUID) int16 {
	t.Helper()
	var shard int16
	if err := testDB.QueryRow(
		context.Background(), "SELECT pod_shard FROM databases WHERE id = $1", databaseID,
	).Scan(&shard); err != nil {
		t.Fatalf("reading pod_shard: %v", err)
	}
	return shard
}

// TestShardAssignment_CreatorOwns verifies creator-owns assignment: a database is created on the shard of the pod
// that creates it, so different pods produce different shards (the distribution that balances at creation).
func TestShardAssignment_CreatorOwns(t *testing.T) {
	ctx := context.Background()
	kek := newTestKEKProvider(t)
	dummyPeer := func(int16, int16) string { return "http://unused" }

	pod0 := newShardPod(t, 0, 0, kek, dummyPeer)
	pod5 := newShardPod(t, 0, 5, kek, dummyPeer)

	db0, err := pod0.dbSvc.CreateDatabase(ctx, uuid.New())
	if err != nil {
		t.Fatalf("create on pod0: %v", err)
	}
	db5, err := pod5.dbSvc.CreateDatabase(ctx, uuid.New())
	if err != nil {
		t.Fatalf("create on pod5: %v", err)
	}

	if got := shardOf(t, db0); got != 0 {
		t.Errorf("db created on pod0 got pod_shard %d, want 0", got)
	}
	if got := shardOf(t, db5); got != 5 {
		t.Errorf("db created on pod5 got pod_shard %d, want 5", got)
	}
}

// TestReconcileOnBoot_ShardAware verifies reconcile restores only databases this pod owns: with the pod set to
// shard 0, a missing shard-0 file is restored while a missing shard-1 (foreign) file is left alone and counted.
func TestReconcileOnBoot_ShardAware(t *testing.T) {
	stack := setupBackupStack(t, "reconcile-shard-aware", time.Hour)
	stack.backupSvc.SetShardOrdinal(0) // this pod owns shard 0
	ctx := context.Background()

	owned := provisionDatabase(t, stack)   // shard 0 (the stack's dbSvc ordinal defaults to 0)
	foreign := provisionDatabase(t, stack) // make it shard 1 below
	if err := testDB.Exec(ctx, "UPDATE databases SET pod_shard = 1 WHERE id = $1", foreign); err != nil {
		t.Fatalf("marking foreign database shard 1: %v", err)
	}

	// Both get snapshots, then both local files are removed (fresh-PVC shape).
	if _, err := stack.backupSvc.SweepOnce(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	removeDatabaseFile(t, stack, owned)
	removeDatabaseFile(t, stack, foreign)

	stats, err := stack.backupSvc.ReconcileOnBoot(ctx)
	if err != nil {
		t.Fatalf("ReconcileOnBoot: %v", err)
	}
	if stats.Restored != 1 || stats.SkippedForeign != 1 {
		t.Fatalf("stats = %+v, want Restored=1 SkippedForeign=1", stats)
	}

	// The owned file came back; the foreign file was left absent (it belongs to another pod's volume).
	if owstats, err := stack.sqliteClient.GetDatabaseStats(ctx, owned.String()); err != nil ||
		owstats.LastModifiedAt.IsZero() {
		t.Errorf("owned database not restored (stats=%+v err=%v)", owstats, err)
	}
	if fstats, err := stack.sqliteClient.GetDatabaseStats(ctx, foreign.String()); err != nil ||
		!fstats.LastModifiedAt.IsZero() {
		t.Errorf("foreign database should NOT have been restored locally (stats=%+v err=%v)", fstats, err)
	}
}
