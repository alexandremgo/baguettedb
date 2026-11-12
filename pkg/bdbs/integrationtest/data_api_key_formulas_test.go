//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// TestInternalDataAPIKeyFormulas checks the internal endpoints the databases app calls for Data API keys. The check
// allows a formula on the key and no other, and the list gives the key's formulas; neither gives any once the bdb is
// soft-deleted, even with its links still stored. Forgetting a formula removes it from every key of the bdb, and is not
// an error for a formula no key has. A malformed ID is a 400.
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

	listed := func(key string) []any {
		t.Helper()
		status, resp := testutil.DoRequest(t, router, http.MethodPost, "/internal/api/bdbs/v1/data-api-key-formulas",
			"", map[string]string{"key_id": key, "bdb_id": bdbID})
		if status != http.StatusOK {
			t.Fatalf("list = %d %v, want 200", status, resp)
		}
		ids, ok := resp["formula_ids"].([]any)
		if !ok {
			t.Fatalf("list = %v, want a formula_ids array", resp)
		}
		return ids
	}

	link(keyID)
	link(otherKeyID)
	if ids := listed(keyID); len(ids) != 1 || ids[0] != formulaID {
		t.Errorf("listed formulas of the key = %v, want its one formula", ids)
	}
	if ids := listed(uuid.NewString()); len(ids) != 0 {
		t.Errorf("listed formulas of a key without any = %v, want none", ids)
	}
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
	if allowed(keyID, formulaID) || len(listed(keyID)) != 0 {
		t.Error("a formula is allowed, or listed, on a soft-deleted bdb")
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

// TestInternalFormulaDataAPIKeys checks the internal endpoint listing a bdb's Data API keys by formula: the owner and
// an editor get each formula's keys, in the order auth lists the keys, and a formula on no key is absent; a viewer and
// a non-member get none, and so does everyone once the bdb is soft-deleted. The links of a key auth doesn't list, as a
// revoked one, are left out. A malformed ID is a 400.
func TestInternalFormulaDataAPIKeys(t *testing.T) {
	truncateAll(t)
	newer, older, revoked := uuid.New(), uuid.New(), uuid.New()
	router := setupRouterWithAuth(t, defaultLimits(), &testAuthApp{dataAPIKeys: []apptypes.DataAPIKey{
		{ID: newer, Name: "phone app"}, {ID: older, Name: "web app"},
	}})
	ownerID := seedUser(t, "formula-keys-owner@example.com")
	owner := userToken(t, ownerID)
	status, resp := createBdb(t, router, owner, "Keys", "")
	if status != http.StatusCreated {
		t.Fatalf("creating a bdb = %d %v, want 201", status, resp)
	}
	bdbID, _ := resp["id"].(string)
	members := map[string]string{"owner": ownerID}
	for _, role := range []string{"editor", "viewer"} {
		members[role] = seedUser(t, "formula-keys-"+role+"@example.com")
		joinBdbAs(t, router, owner, userToken(t, members[role]), bdbID, role)
	}
	members["non-member"] = seedUser(t, "formula-keys-stranger@example.com")

	formulaID, otherFormulaID := uuid.NewString(), uuid.NewString()
	for _, link := range []struct {
		key     uuid.UUID
		formula string
	}{{older, formulaID}, {newer, formulaID}, {newer, otherFormulaID}, {revoked, formulaID}} {
		if err := testDB.Exec(context.Background(),
			`INSERT INTO data_api_key_formulas (key_id, bdb_id, formula_id, created_at) VALUES ($1, $2, $3, $4)`,
			link.key, bdbID, link.formula, time.Now().UTC()); err != nil {
			t.Fatalf("storing a link: %v", err)
		}
	}

	// keyNames returns each formula's key names, as the user userID gets them.
	keyNames := func(userID string) map[string][]string {
		t.Helper()
		status, resp := testutil.DoRequest(t, router, http.MethodPost, "/internal/api/bdbs/v1/formula-data-api-keys",
			"", map[string]string{"user_id": userID, "bdb_id": bdbID})
		formulas, ok := resp["formulas"].(map[string]any)
		if status != http.StatusOK || !ok {
			t.Fatalf("listing the keys by formula = %d %v, want 200 with a formulas object", status, resp)
		}
		names := make(map[string][]string, len(formulas))
		for formula, keys := range formulas {
			list, _ := keys.([]any)
			for _, raw := range list {
				key, _ := raw.(map[string]any)
				name, _ := key["name"].(string)
				names[formula] = append(names[formula], name)
			}
		}
		return names
	}

	for _, role := range []string{"owner", "editor"} {
		names := keyNames(members[role])
		if len(names) != 2 || !slices.Equal(names[formulaID], []string{"phone app", "web app"}) ||
			!slices.Equal(names[otherFormulaID], []string{"phone app"}) {
			t.Errorf("the %s's keys by formula = %v, want both formulas' listed keys, newest first", role, names)
		}
	}
	for _, role := range []string{"viewer", "non-member"} {
		if names := keyNames(members[role]); len(names) != 0 {
			t.Errorf("a %s's keys by formula = %v, want none", role, names)
		}
	}

	if status, resp := testutil.DoRequest(t, router, http.MethodDelete, "/api/bdbs/v1/bdbs/"+bdbID, owner,
		nil); status != http.StatusNoContent {
		t.Fatalf("soft-deleting the bdb = %d %v, want 204", status, resp)
	}
	if names := keyNames(ownerID); len(names) != 0 {
		t.Errorf("the keys by formula of a soft-deleted bdb = %v, want none", names)
	}

	for _, body := range []map[string]string{
		{"user_id": "nope", "bdb_id": bdbID},
		{"user_id": ownerID, "bdb_id": "nope"},
	} {
		if status, _ := testutil.DoRequest(t, router, http.MethodPost, "/internal/api/bdbs/v1/formula-data-api-keys",
			"", body); status != http.StatusBadRequest {
			t.Errorf("listing the keys by formula with %v = %d, want 400", body, status)
		}
	}
}
