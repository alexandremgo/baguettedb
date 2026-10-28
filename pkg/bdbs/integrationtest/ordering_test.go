//go:build integration

package integrationtest

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// reorder submits a new arrangement of the caller's bdbs and returns the response status.
func reorder(t *testing.T, router chi.Router, token string, bdbIDs ...string) int {
	t.Helper()
	status, _ := testutil.DoRequest(t, router, http.MethodPatch, "/api/bdbs/v1/bdbs/order", token,
		map[string]any{"bdb_ids": bdbIDs})
	return status
}

// joinBdb walks an invite through creation and acceptance so the joiner ends up an editor of the bdb.
func joinBdb(t *testing.T, router chi.Router, ownerToken, joinerToken, bdbID string) {
	t.Helper()
	joinBdbAs(t, router, ownerToken, joinerToken, bdbID, "editor")
}

// joinBdbAs walks an invite through creation and acceptance with the given role.
func joinBdbAs(t *testing.T, router chi.Router, ownerToken, joinerToken, bdbID, role string) {
	t.Helper()
	_, invite := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites", bdbID), ownerToken,
		map[string]any{"role": role})
	status, resp := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/invites/%s/accept", invite["token"].(string)), joinerToken, nil)
	if status != http.StatusOK {
		t.Fatalf("accepting invite: status = %d, want %d: %v", status, http.StatusOK, resp)
	}
}

// listedNames returns the names of the caller's bdbs in the order the API returns them.
func listedNames(t *testing.T, router chi.Router, token string) []string {
	t.Helper()
	status, resp := testutil.DoRequestList(t, router, http.MethodGet, "/api/bdbs/v1/bdbs", token, nil)
	if status != http.StatusOK {
		t.Fatalf("listing bdbs: status = %d, want %d", status, http.StatusOK)
	}
	names := make([]string, 0, len(resp))
	for _, c := range resp {
		names = append(names, c["name"].(string))
	}
	return names
}

func assertOrder(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// TestListByUser_NewestBdbLandsOnTop checks that a freshly created bdb takes the top slot, which is what
// the per-user display_order has to reproduce now that it, and no longer created_at, drives the ordering.
func TestListByUser_NewestBdbLandsOnTop(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	token := userToken(t, seedUser(t, "order-new@test.com"))

	createBdb(t, router, token, "First", "")
	createBdb(t, router, token, "Second", "")
	createBdb(t, router, token, "Third", "")

	assertOrder(t, listedNames(t, router, token), "Third", "Second", "First")
}

// TestReorderBdbs_AppliesTheGivenOrder checks a reorder round-trip, and that a bdb created afterwards
// still lands on top of the arrangement the user just made.
func TestReorderBdbs_AppliesTheGivenOrder(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	token := userToken(t, seedUser(t, "order-reorder@test.com"))

	_, first := createBdb(t, router, token, "First", "")
	_, second := createBdb(t, router, token, "Second", "")
	_, third := createBdb(t, router, token, "Third", "")

	status := reorder(t, router, token,
		second["id"].(string), third["id"].(string), first["id"].(string))
	if status != http.StatusNoContent {
		t.Fatalf("reorder status = %d, want %d", status, http.StatusNoContent)
	}

	assertOrder(t, listedNames(t, router, token), "Second", "Third", "First")

	createBdb(t, router, token, "Fourth", "")
	assertOrder(t, listedNames(t, router, token), "Fourth", "Second", "Third", "First")
}

// TestReorderBdbs_IgnoresBdbsTheCallerCannotSee checks that a bdb soft-deleted by its owner, which
// drops out of every other member's list, does not have to appear in their reorder request.
func TestReorderBdbs_IgnoresBdbsTheCallerCannotSee(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	ownerToken := userToken(t, seedUser(t, "order-hidden-owner@test.com"))
	guestToken := userToken(t, seedUser(t, "order-hidden-guest@test.com"))

	_, doomed := createBdb(t, router, ownerToken, "Doomed", "")
	_, kept := createBdb(t, router, ownerToken, "Kept", "")
	joinBdb(t, router, ownerToken, guestToken, doomed["id"].(string))
	joinBdb(t, router, ownerToken, guestToken, kept["id"].(string))

	status, _ := testutil.DoRequest(t, router, http.MethodDelete,
		"/api/bdbs/v1/bdbs/"+doomed["id"].(string), ownerToken, nil)
	if status != http.StatusNoContent {
		t.Fatalf("deleting bdb: status = %d, want %d", status, http.StatusNoContent)
	}

	assertOrder(t, listedNames(t, router, guestToken), "Kept")
	if status := reorder(t, router, guestToken, kept["id"].(string)); status != http.StatusNoContent {
		t.Fatalf("reorder status = %d, want %d — the hidden bdb must not be required", status,
			http.StatusNoContent)
	}
}

// TestReorderBdbs_RouteIsNotReadAsABdbID guards the literal /bdbs/order segment against the
// /bdbs/{bdbID} pattern it sits next to: if chi ever preferred the wildcard, "order" would be parsed
// as a bdb id and the request would land in UpdateBdb instead.
func TestReorderBdbs_RouteIsNotReadAsABdbID(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	token := userToken(t, seedUser(t, "order-route@test.com"))

	_, only := createBdb(t, router, token, "Only", "")

	if status := reorder(t, router, token, only["id"].(string)); status != http.StatusNoContent {
		t.Fatalf("reorder status = %d, want %d — request may have matched /bdbs/{bdbID}",
			status, http.StatusNoContent)
	}
	assertOrder(t, listedNames(t, router, token), "Only")
}

// TestReorderBdbs_LeavesOtherMembersAlone checks that ordering is genuinely per-user: reordering one
// member's list must not move the same bdbs in another member's list.
func TestReorderBdbs_LeavesOtherMembersAlone(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	ownerToken := userToken(t, seedUser(t, "order-owner@test.com"))
	guestToken := userToken(t, seedUser(t, "order-guest@test.com"))

	_, shared1 := createBdb(t, router, ownerToken, "Shared One", "")
	_, shared2 := createBdb(t, router, ownerToken, "Shared Two", "")
	joinBdb(t, router, ownerToken, guestToken, shared1["id"].(string))
	joinBdb(t, router, ownerToken, guestToken, shared2["id"].(string))

	guestBefore := listedNames(t, router, guestToken)

	if status := reorder(t, router, ownerToken,
		shared1["id"].(string), shared2["id"].(string)); status != http.StatusNoContent {
		t.Fatalf("reorder status = %d, want %d", status, http.StatusNoContent)
	}

	assertOrder(t, listedNames(t, router, ownerToken), "Shared One", "Shared Two")
	assertOrder(t, listedNames(t, router, guestToken), guestBefore...)
}

// TestReorderBdbs_RejectsArrangementsThatAreNotAPermutation checks that an arrangement naming
// anything other than each of the caller's bdbs exactly once is refused outright, so a client can
// never leave part of its list unordered — and that a refused request changes nothing.
func TestReorderBdbs_RejectsArrangementsThatAreNotAPermutation(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	token := userToken(t, seedUser(t, "order-invalid@test.com"))
	strangerToken := userToken(t, seedUser(t, "order-stranger@test.com"))

	_, one := createBdb(t, router, token, "Order One", "")
	_, two := createBdb(t, router, token, "Order Two", "")
	_, foreign := createBdb(t, router, strangerToken, "Not Mine", "")

	oneID, twoID, foreignID := one["id"].(string), two["id"].(string), foreign["id"].(string)

	tests := []struct {
		name   string
		bdbIDs []string
	}{
		{"omits one of the caller's bdbs", []string{oneID}},
		{"empty while the caller has bdbs", []string{}},
		{"repeats an id instead of naming both", []string{oneID, oneID}},
		{"repeats an id on top of the full set", []string{oneID, twoID, twoID}},
		{"names a bdb the caller is not a member of", []string{oneID, twoID, foreignID}},
		{"swaps in a foreign id for one of the caller's", []string{oneID, foreignID}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, resp := testutil.DoRequest(t, router, http.MethodPatch, "/api/bdbs/v1/bdbs/order",
				token, map[string]any{"bdb_ids": tt.bdbIDs})
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d: %v", status, http.StatusBadRequest, resp)
			}
			if msg, _ := resp["error"].(string); msg == "" {
				t.Errorf("response carries no error message: %v", resp)
			}
			// The rejection must be total: nothing renumbered, nothing half-applied.
			assertOrder(t, listedNames(t, router, token), "Order Two", "Order One")
		})
	}

	// The stranger's own list is untouched by every rejected attempt above.
	assertOrder(t, listedNames(t, router, strangerToken), "Not Mine")
}

// TestReorderBdbs_ViewerMayArrangeTheirOwnList checks that arranging your sidebar needs no write
// permission on the bdbs in it: it is a personal preference, and it changes nothing another member
// can observe.
func TestReorderBdbs_ViewerMayArrangeTheirOwnList(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	ownerToken := userToken(t, seedUser(t, "order-viewer-owner@test.com"))
	viewerToken := userToken(t, seedUser(t, "order-viewer@test.com"))

	_, first := createBdb(t, router, ownerToken, "Viewer First", "")
	_, second := createBdb(t, router, ownerToken, "Viewer Second", "")
	joinBdbAs(t, router, ownerToken, viewerToken, first["id"].(string), "viewer")
	joinBdbAs(t, router, ownerToken, viewerToken, second["id"].(string), "viewer")

	ownerBefore := listedNames(t, router, ownerToken)

	if status := reorder(t, router, viewerToken,
		first["id"].(string), second["id"].(string)); status != http.StatusNoContent {
		t.Fatalf("reorder status = %d, want %d", status, http.StatusNoContent)
	}

	assertOrder(t, listedNames(t, router, viewerToken), "Viewer First", "Viewer Second")
	assertOrder(t, listedNames(t, router, ownerToken), ownerBefore...)
}
