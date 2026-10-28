//go:build integration

package integrationtest

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

var updateGolden = flag.Bool("update", false, "rewrite the internal-wire golden files from the current responses")

// TestInternalEndpoints_WireFormat pins the exact JSON of the four bdb-scoped internal endpoints — the bytes the
// bdbs module's HTTP DatabasesApp adapter decodes in split deployment — against golden files, for a fixture bdb
// covering the shapes involved: columns with defaults and NOT NULL, a foreign key, a CHECK constraint, a view, a
// table-targeted skill, and both formula kinds (one labeled and hidden).
// Run with -update to regenerate the goldens after an intentional wire change.
func TestInternalEndpoints_WireFormat(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	for _, sql := range []string{
		`CREATE TABLE authors (id INTEGER PRIMARY KEY, name TEXT NOT NULL CHECK (length(name) > 0))`,
		`CREATE TABLE books (id INTEGER PRIMARY KEY, title TEXT DEFAULT 'untitled', ` +
			`author_id INTEGER REFERENCES authors(id) ON DELETE CASCADE)`,
		`CREATE VIEW titles AS SELECT title FROM books`,
	} {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
			map[string]string{"sql": sql})
		if status != http.StatusOK {
			t.Fatalf("schema %q status = %d, want 200, body: %v", sql, status, resp)
		}
	}

	status, resp := testutil.DoRequest(t, router, http.MethodPost, skillsPath(bdbID, ""), token, map[string]any{
		"name":          "cataloguing",
		"description":   "How to file books",
		"body":          "# Cataloguing\nOne row per edition.",
		"target_tables": []string{"books"},
	})
	if status != http.StatusCreated {
		t.Fatalf("create skill status = %d, want 201, body: %v", status, resp)
	}
	skillID, _ := resp["id"].(string)

	formulaIDs := make([]string, 0, 2)
	for _, body := range []map[string]any{
		{
			"name": "recent-books", "label": "Recent books", "description": "Newest first",
			"sql": "SELECT title FROM books ORDER BY id DESC", "params": []any{}, "hidden": true,
		},
		{
			"name": "add-anonymous-author", "description": "Adds a placeholder author",
			"sql": "INSERT INTO authors (name) VALUES ('anonymous')", "params": []any{},
		},
	} {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""), token, body)
		if status != http.StatusCreated {
			t.Fatalf("create formula %q status = %d, want 201, body: %v", body["name"], status, resp)
		}
		id, _ := resp["id"].(string)
		formulaIDs = append(formulaIDs, id)
	}

	// Generated IDs are replaced by stable placeholders so the goldens compare across runs.
	placeholders := map[string]string{skillID: "<skill-id>", formulaIDs[0]: "<formula-1>", formulaIDs[1]: "<formula-2>"}

	for _, endpoint := range []string{"tables", "tables-with-schemas", "skills", "formulas"} {
		t.Run(endpoint, func(t *testing.T) {
			got := postInternal(t, router, "/internal/api/databases/v1/"+endpoint, userID, bdbID)
			for id, placeholder := range placeholders {
				got = bytes.ReplaceAll(got, []byte(id), []byte(placeholder))
			}
			assertGolden(t, filepath.Join("testdata", "internal_wire", endpoint+".json"), got)
		})
	}
}

// postInternal calls a bdb-scoped internal endpoint the way the bdbs HTTP adapter does and returns the raw
// response body, trimmed of the encoder's trailing newline.
func postInternal(t *testing.T, router http.Handler, path, userID, bdbID string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]string{"user_id": userID, "bdb_id": bdbID})
	if err != nil {
		t.Fatalf("marshaling internal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	authhttp.SetInternalSecretHeader(req.Header, testutil.InternalAPISecret)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status = %d, want 200, body: %s", path, rec.Code, rec.Body.String())
	}
	return bytes.TrimSpace(rec.Body.Bytes())
}

// assertGolden compares the indented form of got with the golden file at path, rewriting the file instead when the
// -update flag is set. Indenting only inserts whitespace, so field names, field order, and omitted fields are still
// pinned exactly while the goldens stay readable and diff per field.
func assertGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	var indented bytes.Buffer
	if err := json.Indent(&indented, got, "", "  "); err != nil {
		t.Fatalf("indenting response for %s: %v", path, err)
	}
	got = indented.Bytes()
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("creating golden dir: %v", err)
		}
		if err := os.WriteFile(path, append(got, '\n'), 0o600); err != nil {
			t.Fatalf("writing golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden %s (run with -update to create it): %v", path, err)
	}
	if want = bytes.TrimSpace(want); !bytes.Equal(got, want) {
		t.Errorf("%s wire format changed\n got: %s\nwant: %s", path, got, want)
	}
}
