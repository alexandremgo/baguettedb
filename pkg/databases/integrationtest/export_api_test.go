//go:build integration

package integrationtest

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlitelocal"
)

// exportPath returns the API path for the full-database export endpoint.
func exportPath(bdbID string) string {
	return fmt.Sprintf("/api/databases/v1/bdbs/%s/database/export", bdbID)
}

// doExportRequest performs the export GET and returns the raw response recorder — the body is binary, so
// testutil.DoRequest's JSON decoding does not apply.
func doExportRequest(t *testing.T, router http.Handler, bdbID, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, exportPath(bdbID), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

// exportStagingDirFor returns the export staging dir wired into this test's stacks.
func exportStagingDirFor(t *testing.T) string {
	t.Helper()
	v, ok := stackBackings.Load(t)
	if !ok {
		t.Fatal("no stack backing; call a setupRouter/setupStack helper first")
	}
	return v.(*stackTestBacking).exportStagingDir
}

// openedExportRowCount writes a downloaded export to disk, opens it keyed with the returned passphrase
// (the 32-char passphrase doubles as a 32-byte key input, going through SQLCipher's standard passphrase
// processing), and returns the row count of table t.
func openedExportRowCount(t *testing.T, body []byte, passphrase string) int64 {
	t.Helper()
	path := filepath.Join(t.TempDir(), "downloaded.sqlite")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("writing downloaded export: %v", err)
	}

	// Opened the way an external SQLCipher tool would — the export is passphrase-keyed, unlike the live
	// database files Open reads.
	conn, err := sqlitelocal.OpenWithPassphrase(path, passphrase)
	if err != nil {
		t.Fatalf("opening export with its passphrase: %v", err)
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("closing export copy: %v", cerr)
		}
	}()

	rows, err := conn.Query(context.Background(), "SELECT count(*) FROM t")
	if err != nil {
		t.Fatalf("counting rows in export copy: %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Errorf("closing rows: %v", cerr)
		}
	}()
	if !rows.Next() {
		t.Fatalf("count query returned no rows: %v", rows.Err())
	}
	var count int64
	if err := rows.Scan(&count); err != nil {
		t.Fatalf("scanning count: %v", err)
	}
	return count
}

// seedTable creates table t with n rows through the REST API.
func seedTable(t *testing.T, router http.Handler, bdbID, token string, n int) {
	t.Helper()
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)"})
	if status != http.StatusOK {
		t.Fatalf("create table status = %d, body: %v", status, resp)
	}
	for i := range n {
		status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
			map[string]string{"sql": fmt.Sprintf("INSERT INTO t (v) VALUES ('row-%d')", i)})
		if status != http.StatusOK {
			t.Fatalf("insert status = %d, body: %v", status, resp)
		}
	}
}

// TestExportDatabase_OwnerDownloadsEncryptedCopy covers the export happy path end to end: headers, an
// encrypted body that opens with the one-time passphrase and matches the live data, the audit row, and the
// spool cleanup.
func TestExportDatabase_OwnerDownloadsEncryptedCopy(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New()
	bdbID := uuid.New().String()
	token := userToken(t, userID.String())
	seedTable(t, router, bdbID, token, 3)

	rr := doExportRequest(t, router, bdbID, token)
	if rr.Code != http.StatusOK {
		t.Fatalf("export status = %d, body: %s", rr.Code, rr.Body.String())
	}

	if ct := rr.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	cd := rr.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, `attachment; filename="bdb-`+bdbID) || !strings.Contains(cd, ".sqlite") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	passphrase := rr.Header().Get("X-Baguettedb-Export-Key")
	if len(passphrase) != 32 {
		t.Fatalf("export key header length = %d, want 32", len(passphrase))
	}

	body := rr.Body.Bytes()
	if cl, _ := strconv.Atoi(rr.Header().Get("Content-Length")); cl != len(body) {
		t.Errorf("Content-Length = %d, body length = %d", cl, len(body))
	}
	if bytes.HasPrefix(body, []byte("SQLite format 3\x00")) {
		t.Error("export starts with the plaintext SQLite magic — it is NOT encrypted")
	}

	if count := openedExportRowCount(t, body, passphrase); count != 3 {
		t.Errorf("export row count = %d, want 3", count)
	}

	// The audit row is finalized as a success with the streamed byte count.
	var kind, outcome string
	var recordedBytes int64
	var finishedAt *time.Time
	err := testDB.QueryRow(context.Background(),
		`SELECT kind, outcome, bytes, finished_at FROM database_exports WHERE user_id = $1`, userID,
	).Scan(&kind, &outcome, &recordedBytes, &finishedAt)
	if err != nil {
		t.Fatalf("reading audit row: %v", err)
	}
	if kind != "database" || outcome != "success" {
		t.Errorf("audit row = (%s, %s), want (database, success)", kind, outcome)
	}
	if recordedBytes != int64(len(body)) {
		t.Errorf("audit bytes = %d, want %d", recordedBytes, len(body))
	}
	if finishedAt == nil {
		t.Error("audit finished_at is NULL, want set")
	}

	// The spool file is deleted once the download completes.
	entries, err := os.ReadDir(exportStagingDirFor(t))
	if err != nil {
		t.Fatalf("reading export staging dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("staging dir has %d leftover entries after the download", len(entries))
	}
}

// TestExportDatabase_EditorAndViewerForbidden verifies DBPermExport is owner-only through the real role
// mapping.
func TestExportDatabase_EditorAndViewerForbidden(t *testing.T) {
	truncateAll(t)
	for _, role := range []authz.BdbRole{authz.BdbRoleEditor, authz.BdbRoleViewer} {
		router := setupRouterWithRole(t, role)
		rr := doExportRequest(t, router, uuid.New().String(), userToken(t, uuid.New().String()))
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s export status = %d, want 403", role, rr.Code)
		}
	}
}

// TestExportDatabase_RateLimitWindow plants a backdated success 23h into the 24h window and verifies the
// 429 + Retry-After ≈ 1h, exercising the repo SQL, the service math, and the header mapping together. A
// failed attempt must not count: flipping the planted row to failed frees the slot.
func TestExportDatabase_RateLimitWindow(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New()
	bdbID := uuid.New().String()
	token := userToken(t, userID.String())
	ctx := context.Background()

	// Resolves (lazily creates) the database so its ID exists for the planted audit row.
	if status, resp := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""), token,
		nil); status != http.StatusOK {
		t.Fatalf("resolving database: status = %d, body: %v", status, resp)
	}
	var databaseID uuid.UUID
	if err := testDB.QueryRow(ctx, `SELECT id FROM databases WHERE bdb_id = $1`, bdbID).Scan(&databaseID); err != nil {
		t.Fatalf("reading database id: %v", err)
	}
	err := testDB.Exec(ctx,
		`INSERT INTO database_exports (id, database_id, user_id, kind, outcome, bytes, started_at, finished_at)
		 VALUES ($1, $2, $3, 'database', 'success', 42, $4, $4)`,
		uuid.New(), databaseID, userID, time.Now().UTC().Add(-23*time.Hour))
	if err != nil {
		t.Fatalf("planting backdated export: %v", err)
	}

	rr := doExportRequest(t, router, bdbID, token)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("export status = %d, want 429, body: %s", rr.Code, rr.Body.String())
	}
	retryAfter, err := strconv.Atoi(rr.Header().Get("Retry-After"))
	if err != nil {
		t.Fatalf("Retry-After = %q, want seconds: %v", rr.Header().Get("Retry-After"), err)
	}
	// 23h into a 24h window: the slot frees in ~1h (50–70 min tolerance).
	if retryAfter < 3000 || retryAfter > 4200 {
		t.Errorf("Retry-After = %ds, want ~3600s", retryAfter)
	}

	// Only successes count toward the window: a failed attempt frees the slot.
	if err := testDB.Exec(ctx,
		`UPDATE database_exports SET outcome = 'failed' WHERE database_id = $1`, databaseID); err != nil {
		t.Fatalf("flipping planted export to failed: %v", err)
	}
	if rr := doExportRequest(t, router, bdbID, token); rr.Code != http.StatusOK {
		t.Errorf("export after failed-only history: status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
}

// TestExportStatus covers the quota-status endpoint: a fresh owner sees a free slot, a planted success
// consumes it and surfaces next_available_at, a failed attempt does not count, and a viewer reads the row
// cap without the export permission.
func TestExportStatus(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New()
	bdbID := uuid.New().String()
	token := userToken(t, userID.String())
	ctx := context.Background()
	statusPath := dbPath(bdbID, "export/status")

	readStatus := func(rtr http.Handler, tok string) (map[string]any, map[string]any) {
		t.Helper()
		status, resp := testutil.DoRequest(t, rtr, http.MethodGet, statusPath, tok, nil)
		if status != http.StatusOK {
			t.Fatalf("export status endpoint = %d, body: %v", status, resp)
		}
		dbExp, _ := resp["database_export"].(map[string]any)
		fExp, _ := resp["formula_export"].(map[string]any)
		if dbExp == nil || fExp == nil {
			t.Fatalf("malformed status response: %v", resp)
		}
		return dbExp, fExp
	}

	dbExp, fExp := readStatus(router, token)
	if dbExp["allowed"] != true || dbExp["used_in_window"] != float64(0) || dbExp["next_available_at"] != nil {
		t.Errorf("fresh owner status = %v, want allowed with a free slot", dbExp)
	}
	if dbExp["max_per_window"] != float64(1) || dbExp["window_seconds"] != float64(86400) {
		t.Errorf("quota = %v, want max 1 per 86400s", dbExp)
	}
	if _, ok := fExp["max_rows"].(float64); !ok {
		t.Errorf("formula_export = %v, want a max_rows number", fExp)
	}

	// A planted 23h-old success consumes the slot and pins next_available_at ~1h out.
	var databaseID uuid.UUID
	if err := testDB.QueryRow(ctx, `SELECT id FROM databases WHERE bdb_id = $1`, bdbID).Scan(&databaseID); err != nil {
		t.Fatalf("reading database id: %v", err)
	}
	if err := testDB.Exec(ctx,
		`INSERT INTO database_exports (id, database_id, user_id, kind, outcome, bytes, started_at, finished_at)
		 VALUES ($1, $2, $3, 'database', 'success', 42, $4, $4)`,
		uuid.New(), databaseID, userID, time.Now().UTC().Add(-23*time.Hour)); err != nil {
		t.Fatalf("planting backdated export: %v", err)
	}
	dbExp, _ = readStatus(router, token)
	if dbExp["used_in_window"] != float64(1) {
		t.Errorf("used_in_window = %v, want 1", dbExp["used_in_window"])
	}
	nextStr, _ := dbExp["next_available_at"].(string)
	next, err := time.Parse(time.RFC3339, nextStr)
	if err != nil {
		t.Fatalf("next_available_at = %v, want a timestamp: %v", dbExp["next_available_at"], err)
	}
	if until := time.Until(next); until < 50*time.Minute || until > 70*time.Minute {
		t.Errorf("next_available_at in %v, want ~1h", until)
	}

	// Failed attempts do not consume the window.
	if err := testDB.Exec(ctx,
		`UPDATE database_exports SET outcome = 'failed' WHERE database_id = $1`, databaseID); err != nil {
		t.Fatalf("flipping planted export to failed: %v", err)
	}
	dbExp, _ = readStatus(router, token)
	if dbExp["used_in_window"] != float64(0) || dbExp["next_available_at"] != nil {
		t.Errorf("status after failed-only history = %v, want a free slot", dbExp)
	}

	// A viewer reads the caps but is not allowed to run the database export.
	viewerRouter := setupRouterWithRole(t, authz.BdbRoleViewer)
	dbExp, fExp = readStatus(viewerRouter, userToken(t, uuid.New().String()))
	if dbExp["allowed"] != false {
		t.Errorf("viewer allowed = %v, want false", dbExp["allowed"])
	}
	if _, ok := fExp["max_rows"].(float64); !ok {
		t.Errorf("viewer formula_export = %v, want a max_rows number", fExp)
	}
}

// TestExportDatabase_FreshCopyPerRequest verifies every export snapshots the database at request time — a
// row inserted between two exports appears only in the second copy.
func TestExportDatabase_FreshCopyPerRequest(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New()
	bdbID := uuid.New().String()
	token := userToken(t, userID.String())
	seedTable(t, router, bdbID, token, 1)

	rr1 := doExportRequest(t, router, bdbID, token)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first export status = %d, body: %s", rr1.Code, rr1.Body.String())
	}
	if count := openedExportRowCount(t, rr1.Body.Bytes(), rr1.Header().Get("X-Baguettedb-Export-Key")); count != 1 {
		t.Errorf("first export row count = %d, want 1", count)
	}

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO t (v) VALUES ('later')"})
	if status != http.StatusOK {
		t.Fatalf("insert status = %d, body: %v", status, resp)
	}

	// Pushes the first export's success out of the rolling window to free the rate-limit slot.
	if err := testDB.Exec(context.Background(),
		`UPDATE database_exports SET started_at = $1`, time.Now().UTC().Add(-25*time.Hour)); err != nil {
		t.Fatalf("backdating first export: %v", err)
	}

	rr2 := doExportRequest(t, router, bdbID, token)
	if rr2.Code != http.StatusOK {
		t.Fatalf("second export status = %d, body: %s", rr2.Code, rr2.Body.String())
	}
	if count := openedExportRowCount(t, rr2.Body.Bytes(), rr2.Header().Get("X-Baguettedb-Export-Key")); count != 2 {
		t.Errorf("second export row count = %d, want 2 (must reflect the insert)", count)
	}
}
