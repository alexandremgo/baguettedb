//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// TestInternalDataAPIKeyFormulas checks the internal endpoints the databases app calls for Data API keys. The check
// allows a formula on the key and no other, and no formula once the bdb is soft-deleted, even with its link still
// stored. Forgetting a formula removes it from every key of the bdb, and is not an error for a formula no key has. A
// malformed ID is a 400.
func TestInternalDataAPIKeyFormulas(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	token := userToken(t, seedUser(t, "internal-keys@example.com"))
	status, resp := createBdb(t, router, token, "Keys", "")
	if status != http.StatusCreated {
		t.Fatalf("creating a bdb = %d %v, want 201", status, resp)
	}
	bdbID, _ := resp["id"].(string)
	keyID, otherKeyID, formulaID := uuid.NewString(), uuid.NewString(), uuid.NewString()

	link := func(key string) {
		t.Helper()
		if err := testDB.Exec(context.Background(),
			`INSERT INTO data_api_key_formulas (key_id, bdb_id, formula_id, created_at) VALUES ($1, $2, $3, $4)`,
			key, bdbID, formulaID, time.Now().UTC()); err != nil {
			t.Fatalf("storing a link: %v", err)
		}
	}
	allowed := func(key, formula string) bool {
		t.Helper()
		status, resp := testutil.DoRequest(t, router, http.MethodPost,
			"/internal/api/bdbs/v1/data-api-key-formulas/check", "",
			map[string]string{"key_id": key, "bdb_id": bdbID, "formula_id": formula})
		if status != http.StatusOK {
			t.Fatalf("check = %d %v, want 200", status, resp)
		}
		got, ok := resp["allowed"].(bool)
		if !ok {
			t.Fatalf("check = %v, want an allowed field", resp)
		}
		return got
	}

	link(keyID)
	link(otherKeyID)
	if !allowed(keyID, formulaID) {
		t.Error("a formula on the key is refused")
	}
	if allowed(keyID, uuid.NewString()) || allowed(uuid.NewString(), formulaID) {
		t.Error("a formula not on the key, or another key, is allowed")
	}

	forget := func(formula string) {
		t.Helper()
		status, resp := testutil.DoRequest(t, router, http.MethodPost,
			"/internal/api/bdbs/v1/data-api-key-formulas/forget", "",
			map[string]string{"bdb_id": bdbID, "formula_id": formula})
		if status != http.StatusNoContent {
			t.Fatalf("forget = %d %v, want 204", status, resp)
		}
	}
	forget(uuid.NewString())
	if !allowed(keyID, formulaID) {
		t.Error("forgetting another formula removed this one")
	}
	forget(formulaID)
	if allowed(keyID, formulaID) || allowed(otherKeyID, formulaID) {
		t.Error("a forgotten formula is still allowed on a key")
	}

	link(keyID)
	if status, resp := testutil.DoRequest(t, router, http.MethodDelete, "/api/bdbs/v1/bdbs/"+bdbID, token,
		nil); status != http.StatusNoContent {
		t.Fatalf("soft-deleting the bdb = %d %v, want 204", status, resp)
	}
	if allowed(keyID, formulaID) {
		t.Error("a formula is allowed on a soft-deleted bdb")
	}

	for _, body := range []map[string]string{
		{"key_id": "nope", "bdb_id": bdbID, "formula_id": formulaID},
		{"key_id": keyID, "bdb_id": "nope", "formula_id": formulaID},
	} {
		if status, _ := testutil.DoRequest(t, router, http.MethodPost,
			"/internal/api/bdbs/v1/data-api-key-formulas/check", "", body); status != http.StatusBadRequest {
			t.Errorf("check of %v = %d, want 400", body, status)
		}
	}
}
