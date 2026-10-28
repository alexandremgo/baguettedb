//go:build integration

package integrationtest

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlitelocal"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// readStoredSize reads the tracked size columns for a bdb's database straight from Postgres.
func readStoredSize(t *testing.T, bdbID string) (int64, *time.Time) {
	t.Helper()
	var sizeBytes int64
	var sizeUpdatedAt *time.Time
	err := testDB.QueryRow(context.Background(),
		`SELECT size_bytes, size_updated_at FROM databases WHERE bdb_id = $1`, bdbID,
	).Scan(&sizeBytes, &sizeUpdatedAt)
	if err != nil {
		t.Fatalf("reading stored size for bdb %s: %v", bdbID, err)
	}
	return sizeBytes, sizeUpdatedAt
}

// TestSizeTracking_CreationAndWriteRefreshStoredSize pins the tracked-size lifecycle around forward
// writes: creation populates size_bytes (the fresh file with platform tables is non-empty), and a DML
// write that grows the file is mirrored to Postgres.
func TestSizeTracking_CreationAndWriteRefreshStoredSize(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	// Auto-creates the database; creation + platform migrations refresh the stored size.
	status, _ := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""), token, nil)
	if status != http.StatusOK {
		t.Fatalf("auto-create status = %d, want %d", status, http.StatusOK)
	}
	createdSize, createdAt := readStoredSize(t, bdbID)
	if createdSize <= 0 {
		t.Fatalf("stored size after creation = %d, want > 0", createdSize)
	}
	if createdAt == nil {
		t.Fatal("size_updated_at is NULL after creation, want set")
	}

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, payload TEXT)"})

	// A 200KB payload guarantees the on-disk footprint (main file + WAL) grows past the baseline.
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO items (payload) VALUES (hex(zeroblob(100000)))"})
	if status != http.StatusOK {
		t.Fatalf("insert status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	writtenSize, writtenAt := readStoredSize(t, bdbID)
	if writtenSize <= createdSize {
		t.Errorf("stored size after 200KB insert = %d, want > creation size %d", writtenSize, createdSize)
	}
	if writtenAt == nil || !writtenAt.After(*createdAt) {
		t.Errorf("size_updated_at after insert = %v, want after creation stamp %v", writtenAt, createdAt)
	}
}

// TestSizeTracking_UndoRefreshesStoredSize pins that an undo re-stamps the tracked size: the file's
// byte count may not shrink (SQLite reclaims pages lazily), but the refresh itself must run.
func TestSizeTracking_UndoRefreshesStoredSize(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})
	_, afterWrite := readStoredSize(t, bdbID)
	if afterWrite == nil {
		t.Fatal("size_updated_at is NULL after write, want set")
	}

	// Guards the timestamp comparison against equal Postgres NOW() stamps on very fast machines.
	time.Sleep(5 * time.Millisecond)

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"), token, nil)
	if status != http.StatusOK {
		t.Fatalf("undo status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	sizeAfterUndo, afterUndo := readStoredSize(t, bdbID)
	if sizeAfterUndo <= 0 {
		t.Errorf("stored size after undo = %d, want > 0", sizeAfterUndo)
	}
	if afterUndo == nil || !afterUndo.After(*afterWrite) {
		t.Errorf("size_updated_at after undo = %v, want after write stamp %v", afterUndo, afterWrite)
	}
}

// TestSizeTracking_RefreshesCoalescedWithinInterval pins the coalescing guard: with a long refresh
// interval, the creation-time refresh stamps the database and an immediately following write skips
// its refresh, leaving the stored size at the creation-time value.
func TestSizeTracking_RefreshesCoalescedWithinInterval(t *testing.T) {
	truncateAll(t)
	router := setupRouterWithSizeRefreshInterval(t, time.Hour)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, payload TEXT)"})
	baseline, baselineAt := readStoredSize(t, bdbID)
	if baselineAt == nil {
		t.Fatal("size_updated_at is NULL after creation, want set")
	}

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO items (payload) VALUES (hex(zeroblob(100000)))"})
	if status != http.StatusOK {
		t.Fatalf("insert status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	coalesced, coalescedAt := readStoredSize(t, bdbID)
	if coalesced != baseline {
		t.Errorf("stored size after coalesced write = %d, want unchanged baseline %d", coalesced, baseline)
	}
	if coalescedAt == nil || !coalescedAt.Equal(*baselineAt) {
		t.Errorf("size_updated_at after coalesced write = %v, want unchanged stamp %v", coalescedAt, baselineAt)
	}
}

// expireChangeLogHistory backdates every _change_log entry past the retention window via the internal
// executor, standing in for the days a real deployment waits before the post-write cleanup prunes them.
func expireChangeLogHistory(t *testing.T, client *sqlitelocal.Client, bdbID string) {
	t.Helper()
	var databaseID string
	if err := testDB.QueryRow(context.Background(),
		`SELECT id FROM databases WHERE bdb_id = $1`, bdbID).Scan(&databaseID); err != nil {
		t.Fatalf("resolving database id for bdb %s: %v", bdbID, err)
	}
	backdate := fmt.Sprintf(
		"UPDATE _change_log SET changed_at = strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ', 'now', '-%d days')",
		domain.ChangeLogRetentionDays+1,
	)
	if _, err := client.ExecuteInternal(context.Background(), databaseID, backdate, nil); err != nil {
		t.Fatalf("backdating change log: %v", err)
	}
}

// TestSizeTracking_DeleteShrinksStoredSize pins the full reclamation chain: deleted rows first live on
// in the undo history (the tracked size does NOT drop), and once that history ages past retention the
// post-write cleanup prunes it, auto-vacuum frees the pages, and the eager checkpoint truncates the
// file — the tracked size finally shrinks.
func TestSizeTracking_DeleteShrinksStoredSize(t *testing.T) {
	truncateAll(t)
	router, client := setupStack(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, payload TEXT)"})

	// ~1 MB of payload across five commits.
	for range 5 {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
			map[string]string{"sql": "INSERT INTO items (payload) VALUES (hex(zeroblob(100000)))"})
		if status != http.StatusOK {
			t.Fatalf("insert status = %d, want %d, body: %v", status, http.StatusOK, resp)
		}
	}
	full, _ := readStoredSize(t, bdbID)

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "DELETE FROM items"})
	if status != http.StatusOK {
		t.Fatalf("delete status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	expireChangeLogHistory(t, client, bdbID)

	// First write prunes the expired history in its post-write cleanup; the second write's refresh
	// observes the truncated file (each refresh runs before its own request's cleanup).
	for range 2 {
		status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
			map[string]string{"sql": "DELETE FROM items WHERE 0"})
		if status != http.StatusOK {
			t.Fatalf("cleanup-trigger write status = %d, want %d, body: %v", status, http.StatusOK, resp)
		}
	}

	shrunk, _ := readStoredSize(t, bdbID)
	if shrunk >= full {
		t.Errorf("tracked size after delete + history expiry = %d, want < the full size %d", shrunk, full)
	}
}

// TestSizeTracking_DeleteRestoresWriteCapacity pins the recovery promise of the aggregate cap end to
// end: a blocked owner deletes data, the undo history expires, and — because deletes stay allowed and
// keep triggering the cleanup — the tracked total drops back under the cap and writes pass again.
func TestSizeTracking_DeleteRestoresWriteCapacity(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	token := userToken(t, userID)
	bdbID := uuid.New()

	limits := &mutableLimits{limits: quota.Limits{
		MaxDBSizeBytes:          10 * 1024 * 1024,
		MaxStatementsPerRequest: 100,
		MaxRowsPerRequest:       1000,
	}}
	bdbsApp := &ownedSetBdbsApp{
		ownerID:    uuid.New(),
		ownerRoles: []authz.Role{authz.RoleUser},
		ownedIDs:   []uuid.UUID{bdbID},
	}
	router, client := setupStackWithOptions(t, stackOptions{bdbsApp: bdbsApp, resolver: limits.resolve})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID.String(), "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, payload TEXT)"})
	for range 5 {
		testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID.String(), "data"), token,
			map[string]string{"sql": "INSERT INTO items (payload) VALUES (hex(zeroblob(100000)))"})
	}
	tracked, _ := readStoredSize(t, bdbID.String())

	// Caps the owner just under their current usage: growth is blocked, recovery must be possible.
	limits.limits.MaxTotalDBSizeBytes = tracked - 1

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID.String(), "data"), token,
		map[string]string{"sql": "INSERT INTO items (payload) VALUES ('blocked')"})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("over-cap insert status = %d, want %d, body: %v", status, http.StatusUnprocessableEntity, resp)
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID.String(), "data"), token,
		map[string]string{"sql": "DELETE FROM items"})
	if status != http.StatusOK {
		t.Fatalf("recovery delete status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	expireChangeLogHistory(t, client, bdbID.String())

	// Deletes stay allowed over cap, so they keep triggering the post-write cleanup: the first prunes
	// the expired history, the second's refresh records the shrunken size.
	for range 2 {
		status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID.String(), "data"), token,
			map[string]string{"sql": "DELETE FROM items WHERE 0"})
		if status != http.StatusOK {
			t.Fatalf("cleanup-trigger delete status = %d, want %d, body: %v", status, http.StatusOK, resp)
		}
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID.String(), "data"), token,
		map[string]string{"sql": "INSERT INTO items (payload) VALUES ('recovered')"})
	if status != http.StatusOK {
		t.Errorf("post-recovery insert status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
}

// TestSizeTracking_BackfillRepopulatesSizes pins the startup backfill: with tracked sizes wiped (the
// state of rows predating the size columns), a sweep re-stats this pod's files and repopulates them.
func TestSizeTracking_BackfillRepopulatesSizes(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, payload TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO items (payload) VALUES (hex(zeroblob(100000)))"})
	tracked, _ := readStoredSize(t, bdbID)
	if tracked <= 0 {
		t.Fatalf("stored size before wipe = %d, want > 0", tracked)
	}

	if err := testDB.Exec(context.Background(),
		`UPDATE databases SET size_bytes = 0, size_updated_at = NULL`); err != nil {
		t.Fatalf("wiping tracked sizes: %v", err)
	}

	// Builds a service on the same backing (repo + files) the router's stack writes through, mirroring
	// a fresh pod booting over existing state.
	dbRepo := newDatabaseRepo()
	backing := getOrCreateStackBacking(t, dbRepo)
	dbSvc := service.NewDatabaseService(
		dbRepo, backing.sqliteClient, backing.sqliteClient, &allowAllBdbsApp{}, quota.Fixed(quota.Limits{}),
		backing.kekProvider, testLogger,
	)

	updated, failed := dbSvc.BackfillStoredSizes(context.Background())
	if failed != 0 {
		t.Fatalf("backfill failed count = %d, want 0", failed)
	}
	if updated != 1 {
		t.Fatalf("backfill updated count = %d, want 1", updated)
	}

	backfilled, backfilledAt := readStoredSize(t, bdbID)
	if backfilled != tracked {
		t.Errorf("backfilled size = %d, want the pre-wipe tracked size %d", backfilled, tracked)
	}
	if backfilledAt == nil {
		t.Error("size_updated_at is NULL after backfill, want set")
	}
}
