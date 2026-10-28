//go:build integration

package integrationtest

import (
	"net/http"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// TestInternalOwnedBdbIDs pins the internal owned-bdb-ids lookup: it returns every bdb the user owns —
// including soft-deleted ones still awaiting purge — and excludes bdbs where the user is only a member.
func TestInternalOwnedBdbIDs(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)

	ownerID := seedUser(t, "owner@test.com")
	otherID := seedUser(t, "other@test.com")
	ownerToken := userToken(t, ownerID)
	otherToken := userToken(t, otherID)

	_, createdA := createBdb(t, router, ownerToken, "Owned Active", "")
	bdbA := createdA["id"].(string)
	_, createdB := createBdb(t, router, ownerToken, "Owned Deleted", "")
	bdbB := createdB["id"].(string)

	status, _ := testutil.DoRequest(t, router, http.MethodDelete, "/api/bdbs/v1/bdbs/"+bdbB, ownerToken, nil)
	if status != http.StatusNoContent {
		t.Fatalf("soft delete: status = %d, want %d", status, http.StatusNoContent)
	}

	// A bdb owned by someone else where our user is only an editor must not be counted as owned.
	_, createdC := createBdb(t, router, otherToken, "Membership Only", "")
	bdbC := createdC["id"].(string)
	addMemberViaInvite(t, router, otherToken, ownerToken, bdbC, "editor")

	status, resp := testutil.DoRequest(t, router, http.MethodPost, "/internal/api/bdbs/v1/owned-bdb-ids", "",
		map[string]string{"user_id": ownerID})
	if status != http.StatusOK {
		t.Fatalf("owned-bdb-ids status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rawIDs := resp["bdb_ids"].([]any)
	got := make(map[string]bool, len(rawIDs))
	for _, id := range rawIDs {
		got[id.(string)] = true
	}
	if len(got) != 2 || !got[bdbA] || !got[bdbB] {
		t.Errorf("owned bdb ids = %v, want exactly {%s, %s} (active + soft-deleted, no memberships)",
			rawIDs, bdbA, bdbB)
	}
}
