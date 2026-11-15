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
	"regexp"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

var updateGolden = flag.Bool("update", false, "rewrite the internal-wire golden files from the current responses")

// wireTimestamp matches the timestamp fields of the Data API key shapes, whose values change with every run and whose
// offset and precision depend on the instance store.
var wireTimestamp = regexp.MustCompile(`"(created_at|last_used_at)":"[^"]*"`)

// TestInternalDataAPIKeyEndpoints_WireFormat pins the exact JSON of the internal Data API key endpoints — the bytes
// the bdbs and databases modules' HTTP adapters decode in split deployment — against golden files: a key created, the
// bdb's list holding it, the 422 of an empty name, the 404 of revoking a key the bdb doesn't have, and the check of a
// key, accepted and refused.
// Run with -update to regenerate the goldens after an intentional wire change.
func TestInternalDataAPIKeyEndpoints_WireFormat(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := createUser(t, "wire@example.com", "Str0ng-Passw0rd!")
	bdbID := uuid.NewString()

	created := postInternalWire(t, router, "/internal/api/auth/v1/data-api-keys/create",
		map[string]string{"user_id": userID, "bdb_id": bdbID, "name": "finance app"}, http.StatusCreated)
	var fields struct {
		Value string `json:"value"`
		Key   struct {
			ID          string `json:"id"`
			TokenPrefix string `json:"token_prefix"`
		} `json:"key"`
	}
	if err := json.Unmarshal(created, &fields); err != nil {
		t.Fatalf("decoding the created key: %v", err)
	}

	// Generated values are replaced by stable placeholders so the goldens compare across runs. The value goes first:
	// it contains the token prefix.
	placeholders := []struct{ value, placeholder string }{
		{fields.Value, "<value>"},
		{fields.Key.TokenPrefix, "<token-prefix>"},
		{fields.Key.ID, "<key-id>"},
		{userID, "<user-id>"},
		{bdbID, "<bdb-id>"},
	}
	stable := func(got []byte) []byte {
		for _, p := range placeholders {
			got = bytes.ReplaceAll(got, []byte(p.value), []byte(p.placeholder))
		}
		return wireTimestamp.ReplaceAll(got, []byte(`"$1":"<time>"`))
	}

	for _, tt := range []struct {
		golden string
		got    []byte
	}{
		{"data-api-keys-create", created},
		{"data-api-keys-list", postInternalWire(t, router, "/internal/api/auth/v1/data-api-keys",
			map[string]string{"bdb_id": bdbID}, http.StatusOK)},
		{"data-api-keys-create-invalid-name", postInternalWire(t, router, "/internal/api/auth/v1/data-api-keys/create",
			map[string]string{"user_id": userID, "bdb_id": bdbID, "name": " "}, http.StatusUnprocessableEntity)},
		{"data-api-keys-revoke-unknown", postInternalWire(t, router, "/internal/api/auth/v1/data-api-keys/revoke",
			map[string]string{"bdb_id": bdbID, "key_id": uuid.NewString()}, http.StatusNotFound)},
		{"data-api-keys-check", postInternalWire(t, router, "/internal/api/auth/v1/data-api-keys/check",
			map[string]string{"key": fields.Value}, http.StatusOK)},
		{"data-api-keys-check-refused", postInternalWire(t, router, "/internal/api/auth/v1/data-api-keys/check",
			map[string]string{"key": "bdbdk_unknown"}, http.StatusUnauthorized)},
	} {
		t.Run(tt.golden, func(t *testing.T) {
			assertGolden(t, filepath.Join("testdata", "internal_wire", tt.golden+".json"), stable(tt.got))
		})
	}
}

// postInternalWire calls an internal endpoint the way the bdbs HTTP adapter does and returns the raw response body,
// trimmed of the encoder's trailing newline, failing the test unless the status is want.
func postInternalWire(t *testing.T, router http.Handler, path string, body map[string]string, want int) []byte {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshaling internal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	authhttp.SetInternalSecretHeader(req.Header, testutil.InternalAPISecret)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("%s status = %d, want %d, body: %s", path, rec.Code, want, rec.Body.String())
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
