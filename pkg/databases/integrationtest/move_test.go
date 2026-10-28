//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// TestMove_Release_DrainsAndSnapshots verifies the source side of a move: release marks the database read_only and
// uploads a fresh snapshot whose ref it returns.
func TestMove_Release_DrainsAndSnapshots(t *testing.T) {
	stack := setupBackupStack(t, "move-release", time.Hour)
	ctx := context.Background()
	dbID := provisionDatabase(t, stack)

	status, resp := testutil.DoRequest(t, stack.router, http.MethodPost,
		"/internal/api/databases/v1/release", "", map[string]string{"database_id": dbID.String()})
	if status != http.StatusOK {
		t.Fatalf("release status = %d, want 200 (resp: %v)", status, resp)
	}
	if key, _ := resp["key"].(string); key == "" {
		t.Fatalf("release returned no snapshot key: %v", resp)
	}
	if n := snapshotCount(t, stack, dbID); n < 1 {
		t.Errorf("expected >=1 snapshot after release, got %d", n)
	}

	db, err := newDatabaseRepo().FindByID(ctx, dbID)
	if err != nil {
		t.Fatalf("finding database: %v", err)
	}
	if !db.ReadOnly {
		t.Errorf("expected database read_only after release")
	}
}

// TestMove_Release_RecoversFromLatestSnapshotWhenFileMissing verifies the idempotent recovery path: when the
// source no longer has the live file (already drained, or a pod on a wiped volume) but a snapshot exists, release
// returns the latest snapshot's ref instead of failing — so the move still completes rather than the source being
// re-selected for the drain on every retry.
func TestMove_Release_RecoversFromLatestSnapshotWhenFileMissing(t *testing.T) {
	stack := setupBackupStack(t, "move-release-recover", time.Hour)
	ctx := context.Background()
	dbID := provisionDatabase(t, stack)
	if _, err := stack.sqliteClient.Execute(ctx, dbID.String(), "INSERT INTO t (v) VALUES ('kept')", nil); err != nil {
		t.Fatalf("seeding row: %v", err)
	}
	info, err := stack.backupSvc.BackupDatabase(ctx, dbID, time.Now().UTC())
	if err != nil {
		t.Fatalf("snapshotting: %v", err)
	}
	removeDatabaseFile(t, stack, dbID) // the source has lost its live file

	status, resp := testutil.DoRequest(t, stack.router, http.MethodPost,
		"/internal/api/databases/v1/release", "", map[string]string{"database_id": dbID.String()})
	if status != http.StatusOK {
		t.Fatalf("release with a missing file status = %d, want 200 (resp: %v)", status, resp)
	}
	if key, _ := resp["key"].(string); key != info.Key {
		t.Errorf("release returned key %q, want the latest snapshot %q", resp["key"], info.Key)
	}

	db, err := newDatabaseRepo().FindByID(ctx, dbID)
	if err != nil {
		t.Fatalf("finding database: %v", err)
	}
	if !db.ReadOnly {
		t.Errorf("expected database read_only after release")
	}
}

// TestMove_Release_UnrecoverableWhenNoFileAndNoSnapshot verifies the fail-loud path: a database with neither a
// live file nor any snapshot is genuine data loss, so release fails with a distinct 410 (Gone) — letting the
// orchestrator stop and surface the unrecoverable database rather than looping on it forever.
func TestMove_Release_UnrecoverableWhenNoFileAndNoSnapshot(t *testing.T) {
	stack := setupBackupStack(t, "move-release-lost", time.Hour)
	dbID := provisionDatabase(t, stack)
	removeDatabaseFile(t, stack, dbID) // no file, and no snapshot was ever taken

	status, resp := testutil.DoRequest(t, stack.router, http.MethodPost,
		"/internal/api/databases/v1/release", "", map[string]string{"database_id": dbID.String()})
	if status != http.StatusGone {
		t.Fatalf("release with no file and no snapshot status = %d, want 410 (resp: %v)", status, resp)
	}
}

// TestMove_Adopt_InstallsSnapshotByRef verifies the target side: adopt downloads the given snapshot ref, installs
// it as the live file, and verifies it opens — the data survives the snapshot → remove → adopt cycle.
func TestMove_Adopt_InstallsSnapshotByRef(t *testing.T) {
	stack := setupBackupStack(t, "move-adopt", time.Hour)
	ctx := context.Background()
	dbID := provisionDatabase(t, stack)
	if _, err := stack.sqliteClient.Execute(ctx, dbID.String(), "INSERT INTO t (v) VALUES ('moved')", nil); err != nil {
		t.Fatalf("seeding row: %v", err)
	}

	info, err := stack.backupSvc.BackupDatabase(ctx, dbID, time.Now().UTC())
	if err != nil {
		t.Fatalf("snapshotting: %v", err)
	}
	removeDatabaseFile(t, stack, dbID) // simulate the target pod with no local file yet
	// This stack is the move target: the bdb is still owned by the source shard (1), not this pod (ordinal 0),
	// so the adopt ownership guard allows installing the file.
	if err := testDB.Exec(ctx, "UPDATE databases SET pod_shard = 1 WHERE id = $1", dbID); err != nil {
		t.Fatalf("marking database owned by source shard: %v", err)
	}

	status, resp := testutil.DoRequest(t, stack.router, http.MethodPost,
		"/internal/api/databases/v1/adopt", "",
		map[string]string{"database_id": dbID.String(), "key": info.Key})
	if status != http.StatusOK {
		t.Fatalf("adopt status = %d, want 200 (resp: %v)", status, resp)
	}

	// The file is back and the seeded row survived.
	result, err := stack.sqliteClient.Execute(ctx, dbID.String(), "SELECT v FROM t", nil)
	if err != nil {
		t.Fatalf("querying adopted database: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Errorf("adopted database returned %d rows, want 1", len(result.Rows))
	}
}

// TestMove_Adopt_UnknownSnapshotIs404 verifies adopting a missing ref surfaces a clean 404.
func TestMove_Adopt_UnknownSnapshotIs404(t *testing.T) {
	stack := setupBackupStack(t, "move-adopt-404", time.Hour)
	ctx := context.Background()
	dbID := provisionDatabase(t, stack)
	// Owned by another shard so the ownership guard passes and we reach the download (which 404s).
	if err := testDB.Exec(ctx, "UPDATE databases SET pod_shard = 1 WHERE id = $1", dbID); err != nil {
		t.Fatalf("marking database owned by source shard: %v", err)
	}

	status, _ := testutil.DoRequest(t, stack.router, http.MethodPost,
		"/internal/api/databases/v1/adopt", "",
		map[string]string{"database_id": dbID.String(), "key": "snapshots/" + dbID.String() + "/nonexistent.db"})
	if status != http.StatusNotFound {
		t.Fatalf("adopt of unknown snapshot status = %d, want 404", status)
	}
}

// TestMove_Adopt_RefusedWhenOwnedHere verifies the guard: adopting onto a pod that still owns the bdb is
// rejected (409) rather than force-overwriting its live file with an older snapshot.
func TestMove_Adopt_RefusedWhenOwnedHere(t *testing.T) {
	stack := setupBackupStack(t, "move-adopt-owned", time.Hour)
	ctx := context.Background()
	dbID := provisionDatabase(t, stack) // pod_shard 0 == this stack's ordinal 0 → still owned here
	info, err := stack.backupSvc.BackupDatabase(ctx, dbID, time.Now().UTC())
	if err != nil {
		t.Fatalf("snapshotting: %v", err)
	}

	status, _ := testutil.DoRequest(t, stack.router, http.MethodPost,
		"/internal/api/databases/v1/adopt", "",
		map[string]string{"database_id": dbID.String(), "key": info.Key})
	if status != http.StatusConflict {
		t.Fatalf("adopt while still owned status = %d, want 409", status)
	}
}

// TestMove_DeleteLocal_RemovesFileKeepsMetadata verifies cleanup removes only the local file — the Postgres
// metadata (now pointing at the new owner) is untouched, so the moved bdb is unaffected.
func TestMove_DeleteLocal_RemovesFileKeepsMetadata(t *testing.T) {
	stack := setupBackupStack(t, "move-delete-local", time.Hour)
	ctx := context.Background()
	dbID := provisionDatabase(t, stack)

	if st, _ := stack.sqliteClient.GetDatabaseStats(ctx, dbID.String()); st.LastModifiedAt.IsZero() {
		t.Fatal("precondition: database file should be present")
	}
	// Post-hand-off: the bdb now belongs to another shard (1), so this pod's copy is a deletable orphan.
	if err := testDB.Exec(ctx, "UPDATE databases SET pod_shard = 1 WHERE id = $1", dbID); err != nil {
		t.Fatalf("marking database moved to another shard: %v", err)
	}

	status, _ := testutil.DoRequest(t, stack.router, http.MethodPost,
		"/internal/api/databases/v1/delete-local", "", map[string]string{"database_id": dbID.String()})
	if status != http.StatusOK {
		t.Fatalf("delete-local status = %d, want 200", status)
	}

	if st, _ := stack.sqliteClient.GetDatabaseStats(ctx, dbID.String()); !st.LastModifiedAt.IsZero() {
		t.Errorf("expected local file removed after delete-local")
	}
	if _, err := newDatabaseRepo().FindByID(ctx, dbID); err != nil {
		t.Errorf("expected metadata to persist after delete-local, got %v", err)
	}
}

// TestMove_DeleteLocal_RefusedWhenOwnedHere verifies the guard: deleting the local file while this shard still
// owns the bdb is rejected (409) and the file is left intact — preventing data loss from a premature or
// misdirected cleanup.
func TestMove_DeleteLocal_RefusedWhenOwnedHere(t *testing.T) {
	stack := setupBackupStack(t, "move-delete-owned", time.Hour)
	ctx := context.Background()
	dbID := provisionDatabase(t, stack) // pod_shard 0 == this stack's ordinal 0 → still owned here

	status, _ := testutil.DoRequest(t, stack.router, http.MethodPost,
		"/internal/api/databases/v1/delete-local", "", map[string]string{"database_id": dbID.String()})
	if status != http.StatusConflict {
		t.Fatalf("delete-local while still owned status = %d, want 409", status)
	}
	if st, _ := stack.sqliteClient.GetDatabaseStats(ctx, dbID.String()); st.LastModifiedAt.IsZero() {
		t.Errorf("expected local file to be left intact when delete-local is refused")
	}
}

// TestMove_CompleteMove_FlipsOwnerAndClearsReadOnly checks the move hand-off flips the full owner (pod_color +
// pod_shard) to the new fleet/pod AND clears read_only so the moved bdb is served (and writable) on the new
// owner. Provisioned at (color 0, shard 0); moved to (color 1, shard 1).
func TestMove_CompleteMove_FlipsOwnerAndClearsReadOnly(t *testing.T) {
	stack := setupBackupStack(t, "move-complete", time.Hour)
	ctx := context.Background()
	repo := newDatabaseRepo()
	dbID := provisionDatabase(t, stack) // (color 0, shard 0) by default
	if err := repo.SetReadOnly(ctx, dbID, true); err != nil {
		t.Fatalf("marking read_only (post-release state): %v", err)
	}

	status, resp := testutil.DoRequest(t, stack.router, http.MethodPost,
		"/internal/api/databases/v1/complete-move", "",
		map[string]any{"database_id": dbID.String(), "color": 1, "shard": 1})
	if status != http.StatusOK {
		t.Fatalf("complete-move status = %d, want 200 (resp: %v)", status, resp)
	}

	db, err := repo.FindByID(ctx, dbID)
	if err != nil {
		t.Fatalf("finding database: %v", err)
	}
	if db.PodColor != 1 {
		t.Errorf("pod_color = %d, want 1", db.PodColor)
	}
	if db.PodShard != 1 {
		t.Errorf("pod_shard = %d, want 1", db.PodShard)
	}
	if db.ReadOnly {
		t.Errorf("read_only should be cleared after complete-move")
	}
}
