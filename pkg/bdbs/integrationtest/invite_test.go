//go:build integration

package integrationtest

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

func TestCreateInvite_Success(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)

	ownerID := seedUser(t, "owner@test.com")
	token := userToken(t, ownerID)

	_, bdbResp := createBdb(t, router, token, "Test Bdb", "desc")
	bdbID := bdbResp["id"].(string)

	status, resp := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites", bdbID), token,
		map[string]any{"role": "editor", "expires_in_hours": 48})

	if status != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %v", status, resp)
	}
	if resp["token"] == nil || resp["token"] == "" {
		t.Fatal("response should contain a token")
	}
	if resp["invite_url"] == nil || resp["invite_url"] == "" {
		t.Fatal("response should contain an invite_url")
	}
	if resp["role"] != "editor" {
		t.Fatalf("expected role editor, got %v", resp["role"])
	}
}

func TestCreateInvite_NonOwner(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)

	ownerID := seedUser(t, "owner@test.com")
	editorID := seedUser(t, "editor@test.com")
	ownerToken := userToken(t, ownerID)
	editorToken := userToken(t, editorID)

	_, bdbResp := createBdb(t, router, ownerToken, "Test Bdb", "desc")
	bdbID := bdbResp["id"].(string)

	// Add editor as member via invite first.
	_, invResp := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites", bdbID), ownerToken,
		map[string]any{"role": "editor"})
	inviteToken := invResp["token"].(string)

	testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/invites/%s/accept", inviteToken), editorToken, nil)

	// Editor tries to create invite — should fail.
	status, _ := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites", bdbID), editorToken,
		map[string]any{"role": "viewer"})

	if status != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", status)
	}
}

func TestCreateInvite_InvalidRole(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)

	ownerID := seedUser(t, "owner@test.com")
	token := userToken(t, ownerID)

	_, bdbResp := createBdb(t, router, token, "Test Bdb", "desc")
	bdbID := bdbResp["id"].(string)

	status, _ := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites", bdbID), token,
		map[string]any{"role": "owner"})

	if status != http.StatusBadRequest && status != http.StatusUnprocessableEntity {
		t.Fatalf("expected 400 or 422, got %d", status)
	}
}

func TestAcceptInvite_Success(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)

	ownerID := seedUser(t, "owner@test.com")
	joinerID := seedUser(t, "joiner@test.com")
	ownerToken := userToken(t, ownerID)
	joinerToken := userToken(t, joinerID)

	_, bdbResp := createBdb(t, router, ownerToken, "Test Bdb", "desc")
	bdbID := bdbResp["id"].(string)

	// Owner creates invite.
	_, invResp := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites", bdbID), ownerToken,
		map[string]any{"role": "editor"})
	inviteToken := invResp["token"].(string)

	// Joiner accepts.
	status, resp := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/invites/%s/accept", inviteToken), joinerToken, nil)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", status, resp)
	}

	// Verify joiner is now a member.
	members, ok := resp["members"].([]any)
	if !ok {
		t.Fatal("expected members in response")
	}
	found := false
	for _, m := range members {
		member := m.(map[string]any)
		if member["user_id"] == joinerID && member["role"] == "editor" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("joiner should be a member with editor role")
	}
}

func TestAcceptInvite_Expired(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)

	ownerID := seedUser(t, "owner@test.com")
	joinerID := seedUser(t, "joiner@test.com")
	ownerToken := userToken(t, ownerID)
	joinerToken := userToken(t, joinerID)

	_, bdbResp := createBdb(t, router, ownerToken, "Test Bdb", "desc")
	bdbID := bdbResp["id"].(string)

	// Create invite, then expire it in the DB.
	_, invResp := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites", bdbID), ownerToken,
		map[string]any{"role": "editor"})
	inviteToken := invResp["token"].(string)

	// Manually expire the invite.
	past := time.Now().UTC().Add(-1 * time.Hour)
	err := testDB.Exec(t.Context(), "UPDATE bdb_invites SET expires_at = $1 WHERE token = $2", past, inviteToken)
	if err != nil {
		t.Fatalf("expiring invite: %v", err)
	}

	status, _ := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/invites/%s/accept", inviteToken), joinerToken, nil)

	if status != http.StatusGone {
		t.Fatalf("expected 410, got %d", status)
	}
}

func TestAcceptInvite_AlreadyUsed(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)

	ownerID := seedUser(t, "owner@test.com")
	joinerID := seedUser(t, "joiner@test.com")
	thirdID := seedUser(t, "third@test.com")
	ownerToken := userToken(t, ownerID)
	joinerToken := userToken(t, joinerID)
	thirdToken := userToken(t, thirdID)

	_, bdbResp := createBdb(t, router, ownerToken, "Test Bdb", "desc")
	bdbID := bdbResp["id"].(string)

	_, invResp := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites", bdbID), ownerToken,
		map[string]any{"role": "editor"})
	inviteToken := invResp["token"].(string)

	// First accept succeeds.
	testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/invites/%s/accept", inviteToken), joinerToken, nil)

	// Second accept fails.
	status, _ := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/invites/%s/accept", inviteToken), thirdToken, nil)

	if status != http.StatusConflict {
		t.Fatalf("expected 409, got %d", status)
	}
}

func TestAcceptInvite_AlreadyMember(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)

	ownerID := seedUser(t, "owner@test.com")
	joinerID := seedUser(t, "joiner@test.com")
	ownerToken := userToken(t, ownerID)
	joinerToken := userToken(t, joinerID)

	_, bdbResp := createBdb(t, router, ownerToken, "Test Bdb", "desc")
	bdbID := bdbResp["id"].(string)

	// Add joiner first.
	_, inv1 := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites", bdbID), ownerToken,
		map[string]any{"role": "editor"})
	testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/invites/%s/accept", inv1["token"].(string)), joinerToken, nil)

	// Create another invite and try to accept as already-member.
	_, inv2 := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites", bdbID), ownerToken,
		map[string]any{"role": "viewer"})

	status, _ := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/invites/%s/accept", inv2["token"].(string)), joinerToken, nil)

	if status != http.StatusConflict {
		t.Fatalf("expected 409, got %d", status)
	}
}

func TestAcceptInvite_Revoked(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)

	ownerID := seedUser(t, "owner@test.com")
	joinerID := seedUser(t, "joiner@test.com")
	ownerToken := userToken(t, ownerID)
	joinerToken := userToken(t, joinerID)

	_, bdbResp := createBdb(t, router, ownerToken, "Test Bdb", "desc")
	bdbID := bdbResp["id"].(string)

	_, invResp := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites", bdbID), ownerToken,
		map[string]any{"role": "editor"})
	inviteToken := invResp["token"].(string)
	inviteID := invResp["id"].(string)

	// Revoke.
	testutil.DoRequest(t, router, http.MethodDelete,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites/%s", bdbID, inviteID), ownerToken, nil)

	// Try to accept.
	status, _ := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/invites/%s/accept", inviteToken), joinerToken, nil)

	if status != http.StatusConflict {
		t.Fatalf("expected 409, got %d", status)
	}
}

func TestRevokeInvite_Success(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)

	ownerID := seedUser(t, "owner@test.com")
	token := userToken(t, ownerID)

	_, bdbResp := createBdb(t, router, token, "Test Bdb", "desc")
	bdbID := bdbResp["id"].(string)

	_, invResp := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites", bdbID), token,
		map[string]any{"role": "editor"})
	inviteID := invResp["id"].(string)

	status, _ := testutil.DoRequest(t, router, http.MethodDelete,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites/%s", bdbID, inviteID), token, nil)

	if status != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", status)
	}
}

func TestListBdbInvites(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)

	ownerID := seedUser(t, "owner@test.com")
	token := userToken(t, ownerID)

	_, bdbResp := createBdb(t, router, token, "Test Bdb", "desc")
	bdbID := bdbResp["id"].(string)

	// Create two invites.
	testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites", bdbID), token,
		map[string]any{"role": "editor"})
	testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites", bdbID), token,
		map[string]any{"role": "viewer"})

	// List invites.
	status, invites := testutil.DoRequestList(t, router, http.MethodGet,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites", bdbID), token, nil)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if len(invites) != 2 {
		t.Fatalf("expected 2 invites, got %d", len(invites))
	}
}

// --- Soft-deleted bdb invite invalidation ---

func TestAcceptInvite_BdbSoftDeleted(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)

	ownerID := seedUser(t, "owner@test.com")
	joinerID := seedUser(t, "joiner@test.com")
	ownerToken := userToken(t, ownerID)
	joinerToken := userToken(t, joinerID)

	_, bdbResp := createBdb(t, router, ownerToken, "Going Away", "")
	bdbID := bdbResp["id"].(string)

	_, invResp := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites", bdbID), ownerToken,
		map[string]any{"role": "editor"})
	inviteToken := invResp["token"].(string)

	// Owner soft-deletes the bdb before the invitee accepts.
	status, _ := testutil.DoRequest(t, router, http.MethodDelete,
		"/api/bdbs/v1/bdbs/"+bdbID, ownerToken, nil)
	if status != http.StatusNoContent {
		t.Fatalf("soft delete: status = %d, want %d", status, http.StatusNoContent)
	}

	status, resp := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/invites/%s/accept", inviteToken), joinerToken, nil)
	if status != http.StatusGone {
		t.Fatalf("accept on soft-deleted bdb: status = %d, want %d, body: %v",
			status, http.StatusGone, resp)
	}
}

func TestGetInvite_BdbSoftDeleted(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)

	ownerID := seedUser(t, "owner@test.com")
	joinerID := seedUser(t, "joiner@test.com")
	ownerToken := userToken(t, ownerID)
	joinerToken := userToken(t, joinerID)

	_, bdbResp := createBdb(t, router, ownerToken, "Going Away", "")
	bdbID := bdbResp["id"].(string)

	_, invResp := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites", bdbID), ownerToken,
		map[string]any{"role": "editor"})
	inviteToken := invResp["token"].(string)

	status, _ := testutil.DoRequest(t, router, http.MethodDelete,
		"/api/bdbs/v1/bdbs/"+bdbID, ownerToken, nil)
	if status != http.StatusNoContent {
		t.Fatalf("soft delete: status = %d, want %d", status, http.StatusNoContent)
	}

	status, resp := testutil.DoRequest(t, router, http.MethodGet,
		"/api/bdbs/v1/invites/"+inviteToken, joinerToken, nil)
	if status != http.StatusGone {
		t.Fatalf("get invite on soft-deleted bdb: status = %d, want %d, body: %v",
			status, http.StatusGone, resp)
	}
}
