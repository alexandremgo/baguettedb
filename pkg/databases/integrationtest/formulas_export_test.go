//go:build integration

package integrationtest

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// doCSVExportRequest performs the formula CSV export GET (query is the raw query string, without "?") and
// returns the raw response recorder — the body is a file download, so JSON decoding does not apply.
func doCSVExportRequest(t *testing.T, router http.Handler, bdbID, ref, query, token string) *httptest.ResponseRecorder {
	t.Helper()
	path := formulasPath(bdbID, ref+"/export.csv")
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

// parseCSVExport strips the UTF-8 BOM (failing if absent) and returns the parsed CSV records.
func parseCSVExport(t *testing.T, body []byte) [][]string {
	t.Helper()
	bom := []byte{0xEF, 0xBB, 0xBF}
	if !bytes.HasPrefix(body, bom) {
		t.Fatalf("export does not start with the UTF-8 BOM, got % x", body[:min(3, len(body))])
	}
	records, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(body, bom))).ReadAll()
	if err != nil {
		t.Fatalf("parsing export CSV: %v", err)
	}
	return records
}

// seedItemsTable creates a table covering text with CSV specials and NULL. No stored BLOB — the change log
// records row payloads as JSON, which cannot hold BLOB values, so blob rendering is exercised through a
// computed view column instead (see the happy-path view SQL).
func seedItemsTable(t *testing.T, router http.Handler, bdbID, userID string) {
	t.Helper()
	token := userToken(t, userID)
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]any{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, qty INTEGER, note TEXT)"})
	if status != http.StatusOK {
		t.Fatalf("create items table status = %d, body: %v", status, resp)
	}
	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]any{"sql": `INSERT INTO items (name, qty, note) VALUES
			('plain', 5, 'hello'),
			('tricky', 2, 'a,b "q"' || char(10) || 'line2'),
			('excluded', 0, 'below the filter')`})
	if status != http.StatusOK {
		t.Fatalf("seed items status = %d, body: %v", status, resp)
	}
}

// TestExportFormulaCSV_HappyPathWithParams covers the CSV export end to end: a query-string parameter
// filtering the view, download headers, the BOM, the header row, RFC 4180 escaping of an embedded
// comma/quote/newline, NULL as an empty field, BLOB as base64, the audit row, and the spool cleanup.
func TestExportFormulaCSV_HappyPathWithParams(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New()
	bdbID := uuid.New().String()
	token := userToken(t, userID.String())
	seedItemsTable(t, router, bdbID, userID.String())

	// The computed data column yields a real BLOB for one row and NULL for the other, covering both
	// renderings through the live engine (stored BLOBs are unsupported by the JSON change log).
	createFormulaAPI(t, router, bdbID, userID.String(), map[string]any{
		"name":        "stocked-items",
		"description": "Items at or above a minimum quantity.",
		"sql": "SELECT name, qty, note, CASE WHEN qty >= 5 THEN X'0001FF' END AS data " +
			"FROM items WHERE qty >= :minq ORDER BY id",
		"params": []map[string]any{{"name": "minq", "type": "integer", "source": "prompt"}},
	})

	rr := doCSVExportRequest(t, router, bdbID, "stocked-items", "minq=1", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("export status = %d, body: %s", rr.Code, rr.Body.String())
	}

	if ct := rr.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	cd := rr.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, `attachment; filename="stocked-items-`) || !strings.Contains(cd, ".csv") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	body := rr.Body.Bytes()
	if cl, _ := strconv.Atoi(rr.Header().Get("Content-Length")); cl != len(body) {
		t.Errorf("Content-Length = %d, body length = %d", cl, len(body))
	}

	// qty >= 1 keeps rows 1-2 and drops 'excluded' — proving the query-string param reached the bind.
	want := [][]string{
		{"name", "qty", "note", "data"},
		{"plain", "5", "hello", "AAH/"},
		{"tricky", "2", "a,b \"q\"\nline2", ""},
	}
	if records := parseCSVExport(t, body); !reflect.DeepEqual(records, want) {
		t.Errorf("export records = %q, want %q", records, want)
	}

	var kind, outcome string
	var recordedBytes int64
	var finishedAt *time.Time
	err := testDB.QueryRow(context.Background(),
		`SELECT kind, outcome, bytes, finished_at FROM database_exports WHERE user_id = $1`, userID,
	).Scan(&kind, &outcome, &recordedBytes, &finishedAt)
	if err != nil {
		t.Fatalf("reading audit row: %v", err)
	}
	if kind != "formula_csv" || outcome != "success" {
		t.Errorf("audit row = (%s, %s), want (formula_csv, success)", kind, outcome)
	}
	if recordedBytes != int64(len(body)) {
		t.Errorf("audit bytes = %d, want %d", recordedBytes, len(body))
	}
	if finishedAt == nil {
		t.Error("audit finished_at is NULL, want set")
	}

	entries, err := os.ReadDir(exportStagingDirFor(t))
	if err != nil {
		t.Fatalf("reading export staging dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("staging dir has %d leftover entries after the download", len(entries))
	}
}

// TestExportFormulaCSV_PaginatedViewExportsAllRows verifies a view declaring :cursor/:limit sweeps its full
// result in one export — past MaxPageSize, which caps any single app run of the same view.
func TestExportFormulaCSV_PaginatedViewExportsAllRows(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	const total = 205 // > domain.MaxPageSize (200)
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]any{"sql": "CREATE TABLE many (id INTEGER PRIMARY KEY, v INTEGER)"})
	if status != http.StatusOK {
		t.Fatalf("create table status = %d, body: %v", status, resp)
	}
	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]any{"sql": fmt.Sprintf(
			`WITH RECURSIVE cnt(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM cnt WHERE x < %d)
			 INSERT INTO many (v) SELECT x FROM cnt`, total)})
	if status != http.StatusOK {
		t.Fatalf("seed status = %d, body: %v", status, resp)
	}

	createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "browse-many",
		"description": "Paginated sweep of many.",
		"sql":         "SELECT rowid AS rowid, v FROM many WHERE rowid > :cursor ORDER BY rowid LIMIT :limit",
		"params": []map[string]any{
			{"name": "cursor", "type": "integer", "source": "pagination"},
			{"name": "limit", "type": "integer", "source": "pagination"},
		},
	})

	rr := doCSVExportRequest(t, router, bdbID, "browse-many", "", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("export status = %d, body: %s", rr.Code, rr.Body.String())
	}
	records := parseCSVExport(t, rr.Body.Bytes())
	if len(records) != total+1 {
		t.Fatalf("export has %d records, want %d (header + %d rows)", len(records), total+1, total)
	}
	if last := records[len(records)-1]; last[1] != strconv.Itoa(total) {
		t.Errorf("last row v = %q, want %d", last[1], total)
	}
}

// TestExportFormulaCSV_Rejections covers the 4xx guards: action formulas, unknown params, caller-supplied
// pagination, bulk-selection formulas, and a missing formula.
func TestExportFormulaCSV_Rejections(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)
	seedItemsTable(t, router, bdbID, userID)

	createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "restock",
		"description": "Bump every quantity.",
		"sql":         "UPDATE items SET qty = qty + 1",
	})
	createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "all-items",
		"description": "Every item.",
		"sql":         "SELECT name FROM items",
	})
	createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "browse-items",
		"description": "Paginated items.",
		"sql":         "SELECT rowid AS rowid, name FROM items WHERE rowid > :cursor ORDER BY rowid LIMIT :limit",
		"params": []map[string]any{
			{"name": "cursor", "type": "integer", "source": "pagination"},
			{"name": "limit", "type": "integer", "source": "pagination"},
		},
	})
	createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "selected-items",
		"description": "The selected items.",
		"sql":         "SELECT name FROM items WHERE rowid IN (:rowids)",
		"params":      []map[string]any{{"name": "rowids", "type": "integer", "source": "selection-bulk"}},
	})

	tests := []struct {
		name       string
		ref        string
		query      string
		wantStatus int
	}{
		{"action formula", "restock", "", http.StatusBadRequest},
		{"unknown param", "all-items", "bogus=1", http.StatusBadRequest},
		{"caller-supplied cursor", "browse-items", "cursor=5", http.StatusBadRequest},
		{"caller-supplied limit", "browse-items", "limit=10", http.StatusBadRequest},
		{"bulk-selection formula", "selected-items", "", http.StatusBadRequest},
		{"missing formula", "does-not-exist", "", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := doCSVExportRequest(t, router, bdbID, tt.ref, tt.query, token)
			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d, body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
		})
	}
}

// TestExportFormulaCSV_RowCapExceeded trips a tiny export row cap and verifies the loud 413 (naming the
// cap), the failed audit row, and the staging cleanup — never a silently truncated file.
func TestExportFormulaCSV_RowCapExceeded(t *testing.T) {
	truncateAll(t)
	router := setupRouterWithExportRowCap(t, 2)
	userID := uuid.New()
	bdbID := uuid.New().String()
	token := userToken(t, userID.String())
	seedItemsTable(t, router, bdbID, userID.String())

	createFormulaAPI(t, router, bdbID, userID.String(), map[string]any{
		"name":        "all-items",
		"description": "Every item.",
		"sql":         "SELECT name FROM items",
	})

	rr := doCSVExportRequest(t, router, bdbID, "all-items", "", token)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("export status = %d, want 413, body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "2") {
		t.Errorf("413 body should name the cap, got: %s", rr.Body.String())
	}

	var outcome string
	err := testDB.QueryRow(context.Background(),
		`SELECT outcome FROM database_exports WHERE user_id = $1 AND kind = 'formula_csv'`, userID,
	).Scan(&outcome)
	if err != nil {
		t.Fatalf("reading audit row: %v", err)
	}
	if outcome != "failed" {
		t.Errorf("audit outcome = %q, want failed", outcome)
	}

	entries, err := os.ReadDir(exportStagingDirFor(t))
	if err != nil {
		t.Fatalf("reading export staging dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("staging dir has %d leftover entries after the rejected export", len(entries))
	}
}

// TestExportFormulaCSV_ViewerAllowed verifies read permission suffices: a viewer exports a view another
// member created — unlike the database export, which stays owner-only.
func TestExportFormulaCSV_ViewerAllowed(t *testing.T) {
	truncateAll(t)
	ownerRouter := setupRouter(t)
	ownerID := uuid.New().String()
	bdbID := uuid.New().String()
	seedItemsTable(t, ownerRouter, bdbID, ownerID)
	createFormulaAPI(t, ownerRouter, bdbID, ownerID, map[string]any{
		"name":        "all-items",
		"description": "Every item.",
		"sql":         "SELECT name FROM items ORDER BY id",
	})

	// Shares the on-disk database with ownerRouter via the per-test stack backing.
	viewerRouter := setupRouterWithRole(t, authz.BdbRoleViewer)
	rr := doCSVExportRequest(t, viewerRouter, bdbID, "all-items", "", userToken(t, uuid.New().String()))
	if rr.Code != http.StatusOK {
		t.Fatalf("viewer export status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	records := parseCSVExport(t, rr.Body.Bytes())
	if len(records) != 4 {
		t.Errorf("viewer export has %d records, want 4 (header + 3 rows)", len(records))
	}
}

// TestExportFormulaCSV_MultiSelectParam pins the multi-select surface on exports: the value travels
// the query string as a JSON array literal (URL-encoded), an omitting export resolves the "all"
// default to every lookup value, and a non-array literal is rejected.
func TestExportFormulaCSV_MultiSelectParam(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New()
	bdbID := uuid.New().String()
	token := userToken(t, userID.String())
	seedItemsTable(t, router, bdbID, userID.String())

	createFormulaAPI(t, router, bdbID, userID.String(), map[string]any{
		"name":        "picked-items",
		"description": "Items narrowed to the picked names.",
		"sql": "SELECT name, qty FROM items " +
			"WHERE name IN (SELECT je.value FROM json_each(:names) je) ORDER BY id",
		"params": []map[string]any{{
			"name": "names", "type": "text", "source": "lookup", "multiple": true, "default": "all",
			"lookup": map[string]any{"table": "items", "label_column": "name", "value_column": "name"},
		}},
	})

	rr := doCSVExportRequest(t, router, bdbID, "picked-items",
		"names="+url.QueryEscape(`["plain","excluded"]`), token)
	if rr.Code != http.StatusOK {
		t.Fatalf("picked export status = %d, body: %s", rr.Code, rr.Body.String())
	}
	want := [][]string{{"name", "qty"}, {"plain", "5"}, {"excluded", "0"}}
	if records := parseCSVExport(t, rr.Body.Bytes()); !reflect.DeepEqual(records, want) {
		t.Errorf("picked export records = %q, want %q", records, want)
	}

	rr = doCSVExportRequest(t, router, bdbID, "picked-items", "", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("omitting export status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if records := parseCSVExport(t, rr.Body.Bytes()); len(records) != 4 {
		t.Errorf("omitting export rows = %d, want the header plus all three items", len(records))
	}

	rr = doCSVExportRequest(t, router, bdbID, "picked-items", "names=plain", token)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("non-array literal status = %d, want 400, body: %s", rr.Code, rr.Body.String())
	}
}

// TestExportFormulaCSV_GuardsSpreadsheetFormulas covers formula injection end to end: a text cell that a spreadsheet
// would evaluate is exported with a leading quote, while plain text and negative numbers are untouched.
func TestExportFormulaCSV_GuardsSpreadsheetFormulas(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]any{"sql": "CREATE TABLE cells (id INTEGER PRIMARY KEY, label TEXT, delta INTEGER)"})
	if status != http.StatusOK {
		t.Fatalf("create cells table status = %d, body: %v", status, resp)
	}
	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]any{"sql": `INSERT INTO cells (label, delta) VALUES
			('=HYPERLINK("http://evil.example")', -1),
			('+33 6 12 34 56 78', 0),
			('-5', -5),
			('@SUM(A1:A9)', 1),
			('plain', 2)`})
	if status != http.StatusOK {
		t.Fatalf("seed cells status = %d, body: %v", status, resp)
	}
	createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "all-cells",
		"description": "Every cell.",
		"sql":         "SELECT label, delta FROM cells ORDER BY id",
	})

	rr := doCSVExportRequest(t, router, bdbID, "all-cells", "", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("export status = %d, body: %s", rr.Code, rr.Body.String())
	}
	want := [][]string{
		{"label", "delta"},
		{"'=HYPERLINK(\"http://evil.example\")", "-1"},
		{"'+33 6 12 34 56 78", "0"},
		{"'-5", "-5"},
		{"'@SUM(A1:A9)", "1"},
		{"plain", "2"},
	}
	if records := parseCSVExport(t, rr.Body.Bytes()); !reflect.DeepEqual(records, want) {
		t.Errorf("export records = %q, want %q", records, want)
	}
}
