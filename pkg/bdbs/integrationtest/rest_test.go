//go:build integration

package integrationtest

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	bdbapi "github.com/alexandremgo/baguettedb/pkg/bdbs/api"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// --- test wiring helpers ---

// defaultLimits returns permissive limits for tests that don't exercise quotas.
func defaultLimits() quota.Resolver {
	return quota.Fixed(quota.Limits{
		MaxBdbs:          100,
		MaxMembersPerBdb: 100,
	})
}

// setupRouter wires up the full bdbs HTTP stack backed by the shared test containers.
func setupRouter(t *testing.T) chi.Router {
	return setupRouterWithLimits(t, defaultLimits())
}

// setupRouterWithLimits wires up the bdbs HTTP stack with the given quota limits.
func setupRouterWithLimits(t *testing.T, limits quota.Resolver) chi.Router {
	t.Helper()

	testAuth := &testAuthApp{}
	bdbSvc := newBdbService(testAuth, limits)
	inviteSvc := newInviteService(testAuth, limits)
	authChecker := newAuthChecker(nil)

	tv, err := authhttp.NewTokenValidator(context.Background(), testJWKS.JWKSURL(), testJWKS.Issuer(), "", testLogger)
	if err != nil {
		t.Fatalf("creating token validator: %v", err)
	}
	t.Cleanup(tv.Close)

	handler := bdbapi.NewHandler(bdbSvc, testLogger)
	inviteHandler := bdbapi.NewInviteHandler(inviteSvc, bdbSvc, "http://test.local", testLogger)
	internalHandler := bdbapi.NewInternalHandler(authChecker, testLogger)
	return bdbapi.NewRouter(
		handler, inviteHandler, internalHandler, tv.RequireToken(), 1024*1024, testutil.InternalAPISecret)
}

// testAuthApp implements service.AuthApp for tests.
type testAuthApp struct{}

func (a *testAuthApp) GetUserRoles(_ context.Context, _ uuid.UUID) ([]authz.Role, error) {
	return []authz.Role{"user"}, nil
}

func (a *testAuthApp) GetUserProfiles(
	_ context.Context, userIDs []uuid.UUID,
) (map[uuid.UUID]*apptypes.UserProfile, error) {
	out := make(map[uuid.UUID]*apptypes.UserProfile, len(userIDs))
	for _, id := range userIDs {
		out[id] = &apptypes.UserProfile{
			Email:     id.String() + "@test.com",
			FirstName: "Test",
			LastName:  "User",
		}
	}
	return out, nil
}

// seedUser creates a test user in the database and returns its ID string.
func seedUser(t *testing.T, email string) string {
	t.Helper()
	return testDB.SeedUser(t, email, []string{"user"}).String()
}

// userToken builds a JWT for the given user ID.
func userToken(t *testing.T, userID string) string {
	t.Helper()
	tok, err := testJWKS.BuildToken(userID, []string{"user"})
	if err != nil {
		t.Fatalf("building user token: %v", err)
	}
	return tok
}

// createBdb creates a bdb via the API and returns the response.
func createBdb(t *testing.T, router chi.Router, token, name, description string) (int, map[string]any) {
	t.Helper()
	return testutil.DoRequest(t, router, http.MethodPost, "/api/bdbs/v1/bdbs", token,
		map[string]string{"name": name, "description": description})
}

// addMemberViaInvite creates an invite as the owner and accepts it as the target user.
// Returns the bdb response after the member was added.
func addMemberViaInvite(
	t *testing.T, router chi.Router, ownerToken, targetToken, bdbID, role string,
) (int, map[string]any) {
	t.Helper()

	// Owner creates invite.
	_, invResp := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites", bdbID), ownerToken,
		map[string]any{"role": role})
	token := invResp["token"].(string)

	// Target accepts invite.
	return testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/invites/%s/accept", token), targetToken, nil)
}

// --- Bdb CRUD tests ---

func TestCreateBdb_Success(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := seedUser(t, "owner@test.com")

	status, resp := createBdb(t, router, userToken(t, userID), "My Bdb", "A test bdb")
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusCreated, resp)
	}

	if resp["name"] != "My Bdb" {
		t.Errorf("name = %v, want My Bdb", resp["name"])
	}
	if resp["description"] != "A test bdb" {
		t.Errorf("description = %v, want A test bdb", resp["description"])
	}
	if resp["my_role"] != "owner" {
		t.Errorf("my_role = %v, want owner", resp["my_role"])
	}

	// Owner should be the only member.
	members := resp["members"].([]any)
	if len(members) != 1 {
		t.Fatalf("expected 1 member, got %d", len(members))
	}
	member := members[0].(map[string]any)
	if member["user_id"] != userID {
		t.Errorf("member user_id = %v, want %s", member["user_id"], userID)
	}
	if member["role"] != "owner" {
		t.Errorf("member role = %v, want owner", member["role"])
	}
}

func TestCreateBdb_EmptyName(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := seedUser(t, "owner@test.com")

	status, _ := createBdb(t, router, userToken(t, userID), "", "desc")
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestCreateBdb_Unauthorized(t *testing.T) {
	router := setupRouter(t)

	status, _ := createBdb(t, router, "", "bdb", "desc")
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestGetBdb_Success(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := seedUser(t, "owner@test.com")
	token := userToken(t, userID)

	_, created := createBdb(t, router, token, "Get Bdb", "desc")
	bdbID := created["id"].(string)

	status, resp := testutil.DoRequest(t, router, http.MethodGet,
		"/api/bdbs/v1/bdbs/"+bdbID, token, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
	if resp["name"] != "Get Bdb" {
		t.Errorf("name = %v, want Get Bdb", resp["name"])
	}
	if resp["my_role"] != "owner" {
		t.Errorf("my_role = %v, want owner", resp["my_role"])
	}
	owner, ok := resp["owner"].(map[string]any)
	if !ok {
		t.Fatalf("owner = %v, want the owner member object", resp["owner"])
	}
	if owner["user_id"] != userID || owner["role"] != "owner" {
		t.Errorf("owner = %v, want user_id %s with role owner", owner, userID)
	}
}

func TestGetBdb_NotFound(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := seedUser(t, "owner@test.com")

	status, _ := testutil.DoRequest(t, router, http.MethodGet,
		"/api/bdbs/v1/bdbs/"+uuid.New().String(), userToken(t, userID), nil)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestGetBdb_NonMember(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	ownerID := seedUser(t, "owner@test.com")
	otherID := seedUser(t, "other@test.com")

	_, created := createBdb(t, router, userToken(t, ownerID), "Private Bdb", "")
	bdbID := created["id"].(string)

	status, _ := testutil.DoRequest(t, router, http.MethodGet,
		"/api/bdbs/v1/bdbs/"+bdbID, userToken(t, otherID), nil)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

func TestListBdbs(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	user1 := seedUser(t, "user1@test.com")
	user2 := seedUser(t, "user2@test.com")

	token1 := userToken(t, user1)
	createBdb(t, router, token1, "Bdb A", "")
	createBdb(t, router, token1, "Bdb B", "")
	createBdb(t, router, userToken(t, user2), "Bdb C", "")

	status, resp := testutil.DoRequestList(t, router, http.MethodGet,
		"/api/bdbs/v1/bdbs", token1, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(resp) != 2 {
		t.Errorf("expected 2 bdbs, got %d", len(resp))
	}
	for _, c := range resp {
		if c["my_role"] != "owner" {
			t.Errorf("my_role = %v, want owner", c["my_role"])
		}
	}
}

func TestUpdateBdb_Success(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := seedUser(t, "owner@test.com")
	token := userToken(t, userID)

	_, created := createBdb(t, router, token, "Old Name", "old desc")
	bdbID := created["id"].(string)

	status, resp := testutil.DoRequest(t, router, http.MethodPatch,
		"/api/bdbs/v1/bdbs/"+bdbID, token,
		map[string]string{"name": "New Name", "description": "new desc"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
	if resp["name"] != "New Name" {
		t.Errorf("name = %v, want New Name", resp["name"])
	}
	if resp["description"] != "new desc" {
		t.Errorf("description = %v, want new desc", resp["description"])
	}
}

// TestUpdateBdb_OmittedFieldUnchanged checks that a PATCH leaves out a field the caller did not send, instead
// of clearing it to the zero value.
func TestUpdateBdb_OmittedFieldUnchanged(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := seedUser(t, "owner@test.com")
	token := userToken(t, userID)

	_, created := createBdb(t, router, token, "Old Name", "keep me")
	bdbID := created["id"].(string)

	status, resp := testutil.DoRequest(t, router, http.MethodPatch,
		"/api/bdbs/v1/bdbs/"+bdbID, token, map[string]any{"name": "New Name"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
	if resp["name"] != "New Name" {
		t.Errorf("name = %v, want New Name", resp["name"])
	}
	if resp["description"] != "keep me" {
		t.Errorf("description = %v, want keep me", resp["description"])
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPatch,
		"/api/bdbs/v1/bdbs/"+bdbID, token, map[string]any{"description": "new desc"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
	if resp["name"] != "New Name" {
		t.Errorf("name = %v, want New Name", resp["name"])
	}
	if resp["description"] != "new desc" {
		t.Errorf("description = %v, want new desc", resp["description"])
	}
}

func TestUpdateBdb_NonOwner(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	ownerID := seedUser(t, "owner@test.com")
	editorID := seedUser(t, "editor@test.com")

	ownerToken := userToken(t, ownerID)
	_, created := createBdb(t, router, ownerToken, "Bdb", "")
	bdbID := created["id"].(string)

	// Adds editor as member.
	testutil.DoRequest(t, router, http.MethodPost,
		"/api/bdbs/v1/bdbs/"+bdbID+"/members", ownerToken,
		map[string]string{"user_id": editorID, "role": "editor"})

	status, _ := testutil.DoRequest(t, router, http.MethodPatch,
		"/api/bdbs/v1/bdbs/"+bdbID, userToken(t, editorID),
		map[string]string{"name": "Hacked", "description": ""})
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

func TestDeleteBdb_Success(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := seedUser(t, "owner@test.com")
	token := userToken(t, userID)

	_, created := createBdb(t, router, token, "To Delete", "")
	bdbID := created["id"].(string)

	status, _ := testutil.DoRequest(t, router, http.MethodDelete,
		"/api/bdbs/v1/bdbs/"+bdbID, token, nil)
	if status != http.StatusNoContent {
		t.Errorf("status = %d, want %d", status, http.StatusNoContent)
	}

	// Delete is a soft delete: the owner still sees the bdb during the
	// grace period, with deleted_at and purge_at populated.
	status, body := testutil.DoRequest(t, router, http.MethodGet,
		"/api/bdbs/v1/bdbs/"+bdbID, token, nil)
	if status != http.StatusOK {
		t.Fatalf("after soft delete: status = %d, want %d", status, http.StatusOK)
	}
	if body["deleted_at"] == nil {
		t.Errorf("deleted_at = nil, want a timestamp after soft delete")
	}
	if body["purge_at"] == nil {
		t.Errorf("purge_at = nil, want a timestamp after soft delete")
	}
}

func TestDeleteBdb_NonOwner(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	ownerID := seedUser(t, "owner@test.com")
	editorID := seedUser(t, "editor@test.com")

	ownerToken := userToken(t, ownerID)
	_, created := createBdb(t, router, ownerToken, "Bdb", "")
	bdbID := created["id"].(string)

	testutil.DoRequest(t, router, http.MethodPost,
		"/api/bdbs/v1/bdbs/"+bdbID+"/members", ownerToken,
		map[string]string{"user_id": editorID, "role": "editor"})

	status, _ := testutil.DoRequest(t, router, http.MethodDelete,
		"/api/bdbs/v1/bdbs/"+bdbID, userToken(t, editorID), nil)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// --- Member management tests ---

func TestListMembers(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	ownerID := seedUser(t, "owner@test.com")
	seedUser(t, "viewer@test.com")
	ownerToken := userToken(t, ownerID)
	viewerToken := userToken(t, seedUser(t, "viewer2@test.com"))

	_, created := createBdb(t, router, ownerToken, "Bdb", "")
	bdbID := created["id"].(string)

	addMemberViaInvite(t, router, ownerToken, viewerToken, bdbID, "viewer")

	status, resp := testutil.DoRequestList(t, router, http.MethodGet,
		"/api/bdbs/v1/bdbs/"+bdbID+"/members", ownerToken, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(resp) != 2 {
		t.Errorf("expected 2 members, got %d", len(resp))
	}
}

// TestListMembers_ProfileFields asserts every member row carries the
// email + name resolved from the auth service. Regression guard for the
// bug where members were rendered as bare UUIDs because the profile
// resolver was hitting a non-existent endpoint.
func TestListMembers_ProfileFields(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	ownerID := seedUser(t, "owner@test.com")
	viewerID := seedUser(t, "viewer@test.com")
	ownerToken := userToken(t, ownerID)
	viewerToken := userToken(t, viewerID)

	_, created := createBdb(t, router, ownerToken, "Bdb", "")
	bdbID := created["id"].(string)

	addMemberViaInvite(t, router, ownerToken, viewerToken, bdbID, "viewer")

	status, resp := testutil.DoRequestList(t, router, http.MethodGet,
		"/api/bdbs/v1/bdbs/"+bdbID+"/members", ownerToken, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(resp) != 2 {
		t.Fatalf("expected 2 members, got %d", len(resp))
	}

	for _, m := range resp {
		uid, _ := m["user_id"].(string)
		email, _ := m["email"].(string)
		first, _ := m["first_name"].(string)
		last, _ := m["last_name"].(string)

		wantEmail := uid + "@test.com"
		if email != wantEmail {
			t.Errorf("member %s: email = %q, want %q", uid, email, wantEmail)
		}
		if first != "Test" {
			t.Errorf("member %s: first_name = %q, want %q", uid, first, "Test")
		}
		if last != "User" {
			t.Errorf("member %s: last_name = %q, want %q", uid, last, "User")
		}
	}
}

func TestUpdateMemberRole_Success(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	ownerID := seedUser(t, "owner@test.com")
	viewerID := seedUser(t, "viewer@test.com")
	ownerToken := userToken(t, ownerID)
	viewerToken := userToken(t, viewerID)

	_, created := createBdb(t, router, ownerToken, "Bdb", "")
	bdbID := created["id"].(string)

	addMemberViaInvite(t, router, ownerToken, viewerToken, bdbID, "viewer")

	status, resp := testutil.DoRequest(t, router, http.MethodPatch,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/members/%s", bdbID, viewerID),
		ownerToken, map[string]string{"role": "editor"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	// Verifies the role was updated.
	members := resp["members"].([]any)
	for _, m := range members {
		member := m.(map[string]any)
		if member["user_id"] == viewerID && member["role"] != "editor" {
			t.Errorf("role = %v, want editor", member["role"])
		}
	}
}

func TestUpdateMemberRole_CannotChangeOwner(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	ownerID := seedUser(t, "owner@test.com")
	ownerToken := userToken(t, ownerID)

	_, created := createBdb(t, router, ownerToken, "Bdb", "")
	bdbID := created["id"].(string)

	status, _ := testutil.DoRequest(t, router, http.MethodPatch,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/members/%s", bdbID, ownerID),
		ownerToken, map[string]string{"role": "editor"})
	if status != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want %d", status, http.StatusUnprocessableEntity)
	}
}

func TestRemoveMember_Success(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	ownerID := seedUser(t, "owner@test.com")
	viewerID := seedUser(t, "viewer@test.com")
	ownerToken := userToken(t, ownerID)
	viewerToken := userToken(t, viewerID)

	_, created := createBdb(t, router, ownerToken, "Bdb", "")
	bdbID := created["id"].(string)

	addMemberViaInvite(t, router, ownerToken, viewerToken, bdbID, "viewer")

	status, resp := testutil.DoRequest(t, router, http.MethodDelete,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/members/%s", bdbID, viewerID),
		ownerToken, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	members := resp["members"].([]any)
	if len(members) != 1 {
		t.Errorf("expected 1 member after removal, got %d", len(members))
	}
}

func TestRemoveMember_CannotRemoveOwner(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	ownerID := seedUser(t, "owner@test.com")
	ownerToken := userToken(t, ownerID)

	_, created := createBdb(t, router, ownerToken, "Bdb", "")
	bdbID := created["id"].(string)

	status, _ := testutil.DoRequest(t, router, http.MethodDelete,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/members/%s", bdbID, ownerID),
		ownerToken, nil)
	if status != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want %d", status, http.StatusUnprocessableEntity)
	}
}

// --- Internal endpoint tests ---

func TestCheckPermission_Internal(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	ownerID := seedUser(t, "owner@test.com")
	viewerID := seedUser(t, "viewer@test.com")
	nonMemberID := seedUser(t, "nonmember@test.com")
	ownerToken := userToken(t, ownerID)
	viewerToken := userToken(t, viewerID)

	_, created := createBdb(t, router, ownerToken, "Bdb", "")
	bdbID := created["id"].(string)

	addMemberViaInvite(t, router, ownerToken, viewerToken, bdbID, "viewer")

	tests := []struct {
		name       string
		userID     string
		perm       string
		wantStatus int
		wantAllow  bool
	}{
		{"owner can read", ownerID, "db:read", http.StatusOK, true},
		{"owner can write", ownerID, "db:write", http.StatusOK, true},
		{"viewer can read", viewerID, "db:read", http.StatusOK, true},
		{"viewer cannot write", viewerID, "db:write", http.StatusOK, false},
		{"non-member denied", nonMemberID, "db:read", http.StatusOK, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, resp := testutil.DoRequest(t, router, http.MethodPost,
				"/internal/api/bdbs/v1/check-permission", "",
				map[string]string{"user_id": tt.userID, "bdb_id": bdbID, "permission": tt.perm})
			if status != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body: %v", status, tt.wantStatus, resp)
			}
			if allowed, ok := resp["allowed"].(bool); ok && allowed != tt.wantAllow {
				t.Errorf("allowed = %v, want %v", allowed, tt.wantAllow)
			}
		})
	}
}

// --- Quota enforcement tests ---

func TestCreateBdb_LimitReached(t *testing.T) {
	truncateAll(t)
	limits := quota.Fixed(quota.Limits{MaxBdbs: 2, MaxMembersPerBdb: 100})
	router := setupRouterWithLimits(t, limits)
	userID := seedUser(t, "owner@test.com")
	token := userToken(t, userID)

	// Creates 2 bdbs (at limit).
	for i := 0; i < 2; i++ {
		status, _ := createBdb(t, router, token, fmt.Sprintf("Bdb %d", i), "")
		if status != http.StatusCreated {
			t.Fatalf("bdb %d: status = %d, want %d", i, status, http.StatusCreated)
		}
	}

	// 3rd should be rejected.
	status, resp := createBdb(t, router, token, "One Too Many", "")
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusForbidden, resp)
	}
	errMsg, _ := resp["error"].(string)
	if !strings.Contains(errMsg, "limit") {
		t.Errorf("error message should mention limit, got: %s", errMsg)
	}
}

func TestInviteAccept_MemberLimitReached(t *testing.T) {
	truncateAll(t)
	limits := quota.Fixed(quota.Limits{MaxBdbs: 100, MaxMembersPerBdb: 3})
	router := setupRouterWithLimits(t, limits)
	ownerID := seedUser(t, "owner@test.com")
	ownerToken := userToken(t, ownerID)

	_, created := createBdb(t, router, ownerToken, "Bdb", "")
	bdbID := created["id"].(string)

	// Adds 2 members via invites (3 total with owner = at limit).
	for i := 0; i < 2; i++ {
		memberID := seedUser(t, fmt.Sprintf("member%d@test.com", i))
		memberToken := userToken(t, memberID)
		status, resp := addMemberViaInvite(t, router, ownerToken, memberToken, bdbID, "viewer")
		if status != http.StatusOK {
			t.Fatalf("member %d: status = %d, want %d, body: %v", i, status, http.StatusOK, resp)
		}
	}

	// 4th member should be rejected (3 = limit).
	extraID := seedUser(t, "extra@test.com")
	extraToken := userToken(t, extraID)

	// Create invite (succeeds — invite creation doesn't check member limit).
	_, invResp := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/invites", bdbID), ownerToken,
		map[string]any{"role": "viewer"})
	invToken := invResp["token"].(string)

	// Accept should fail with limit.
	status, resp := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/invites/%s/accept", invToken), extraToken, nil)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusForbidden, resp)
	}
	errMsg, _ := resp["error"].(string)
	if !strings.Contains(errMsg, "limit") {
		t.Errorf("error message should mention limit, got: %s", errMsg)
	}
}

func TestCreateBdb_BodyTooLarge(t *testing.T) {
	truncateAll(t)
	// Uses a very small body limit to trigger the error.
	limits := defaultLimits()
	testAuthSvc := &testAuthApp{}
	bdbSvc := newBdbService(testAuthSvc, limits)
	inviteSvc := newInviteService(testAuthSvc, limits)
	authChecker := newAuthChecker(nil)

	tv, err := authhttp.NewTokenValidator(context.Background(), testJWKS.JWKSURL(), testJWKS.Issuer(), "", testLogger)
	if err != nil {
		t.Fatalf("creating token validator: %v", err)
	}
	t.Cleanup(tv.Close)

	handler := bdbapi.NewHandler(bdbSvc, testLogger)
	inviteHandler := bdbapi.NewInviteHandler(inviteSvc, bdbSvc, "http://test.local", testLogger)
	internalHandler := bdbapi.NewInternalHandler(authChecker, testLogger)
	// 10 bytes body-size limit.
	router := bdbapi.NewRouter(handler, inviteHandler, internalHandler, tv.RequireToken(), 10,
		testutil.InternalAPISecret)

	userID := seedUser(t, "owner@test.com")
	token := userToken(t, userID)

	status, resp := createBdb(t, router, token, "This name is definitely longer than 10 bytes", "desc")
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusRequestEntityTooLarge, resp)
	}
}

// --- Soft-delete tests ---

// softDeleteWithEditor creates a bdb owned by ownerToken, adds a viewer
// member via invite, then soft-deletes the bdb. Returns the bdb id and
// the viewer's token so callers can assert visibility for both sides.
func softDeleteWithEditor(t *testing.T, router chi.Router) (ownerToken, viewerToken, bdbID string) {
	t.Helper()
	ownerID := seedUser(t, "owner@test.com")
	viewerID := seedUser(t, "viewer@test.com")
	ownerToken = userToken(t, ownerID)
	viewerToken = userToken(t, viewerID)

	_, created := createBdb(t, router, ownerToken, "Soft Target", "")
	bdbID = created["id"].(string)

	addMemberViaInvite(t, router, ownerToken, viewerToken, bdbID, "viewer")

	status, _ := testutil.DoRequest(t, router, http.MethodDelete,
		"/api/bdbs/v1/bdbs/"+bdbID, ownerToken, nil)
	if status != http.StatusNoContent {
		t.Fatalf("soft delete: status = %d, want %d", status, http.StatusNoContent)
	}
	return ownerToken, viewerToken, bdbID
}

func TestSoftDeleteBdb_OwnerListStillShows(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	ownerToken, _, bdbID := softDeleteWithEditor(t, router)

	status, list := testutil.DoRequestList(t, router, http.MethodGet,
		"/api/bdbs/v1/bdbs", ownerToken, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	var match map[string]any
	for _, c := range list {
		if c["id"] == bdbID {
			match = c
			break
		}
	}
	if match == nil {
		t.Fatalf("owner's list should still contain soft-deleted bdb %s", bdbID)
	}
	if match["deleted_at"] == nil {
		t.Errorf("deleted_at = nil, want a timestamp")
	}
	if match["purge_at"] == nil {
		t.Errorf("purge_at = nil, want a timestamp")
	}
}

func TestSoftDeleteBdb_HidesFromNonOwner_List(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	_, viewerToken, bdbID := softDeleteWithEditor(t, router)

	status, list := testutil.DoRequestList(t, router, http.MethodGet,
		"/api/bdbs/v1/bdbs", viewerToken, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	for _, c := range list {
		if c["id"] == bdbID {
			t.Fatalf("viewer's list should not contain soft-deleted bdb %s", bdbID)
		}
	}
}

func TestSoftDeleteBdb_HidesFromNonOwner_Get(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	_, viewerToken, bdbID := softDeleteWithEditor(t, router)

	status, _ := testutil.DoRequest(t, router, http.MethodGet,
		"/api/bdbs/v1/bdbs/"+bdbID, viewerToken, nil)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

func TestDeleteBdb_AlreadySoftDeleted(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := seedUser(t, "owner@test.com")
	token := userToken(t, userID)

	_, created := createBdb(t, router, token, "Double Delete", "")
	bdbID := created["id"].(string)

	status, _ := testutil.DoRequest(t, router, http.MethodDelete,
		"/api/bdbs/v1/bdbs/"+bdbID, token, nil)
	if status != http.StatusNoContent {
		t.Fatalf("first delete: status = %d, want %d", status, http.StatusNoContent)
	}

	status, _ = testutil.DoRequest(t, router, http.MethodDelete,
		"/api/bdbs/v1/bdbs/"+bdbID, token, nil)
	if status != http.StatusConflict {
		t.Errorf("second delete: status = %d, want %d", status, http.StatusConflict)
	}
}

func TestRestoreBdb_Success(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := seedUser(t, "owner@test.com")
	token := userToken(t, userID)

	_, created := createBdb(t, router, token, "Restore Me", "")
	bdbID := created["id"].(string)

	status, _ := testutil.DoRequest(t, router, http.MethodDelete,
		"/api/bdbs/v1/bdbs/"+bdbID, token, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want %d", status, http.StatusNoContent)
	}

	status, resp := testutil.DoRequest(t, router, http.MethodPost,
		"/api/bdbs/v1/bdbs/"+bdbID+"/restore", token, nil)
	if status != http.StatusOK {
		t.Fatalf("restore: status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
	if resp["deleted_at"] != nil {
		t.Errorf("deleted_at = %v, want nil after restore", resp["deleted_at"])
	}
	if resp["purge_at"] != nil {
		t.Errorf("purge_at = %v, want nil after restore", resp["purge_at"])
	}

	status, body := testutil.DoRequest(t, router, http.MethodGet,
		"/api/bdbs/v1/bdbs/"+bdbID, token, nil)
	if status != http.StatusOK {
		t.Fatalf("get after restore: status = %d, want %d", status, http.StatusOK)
	}
	if body["deleted_at"] != nil {
		t.Errorf("after restore, GET deleted_at = %v, want nil", body["deleted_at"])
	}
}

func TestRestoreBdb_NotDeleted(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := seedUser(t, "owner@test.com")
	token := userToken(t, userID)

	_, created := createBdb(t, router, token, "Active", "")
	bdbID := created["id"].(string)

	status, _ := testutil.DoRequest(t, router, http.MethodPost,
		"/api/bdbs/v1/bdbs/"+bdbID+"/restore", token, nil)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestRestoreBdb_NotOwner(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	ownerToken, viewerToken, bdbID := softDeleteWithEditor(t, router)
	_ = ownerToken

	status, _ := testutil.DoRequest(t, router, http.MethodPost,
		"/api/bdbs/v1/bdbs/"+bdbID+"/restore", viewerToken, nil)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

func TestWritesBlockedOnSoftDeleted(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	ownerToken, viewerToken, bdbID := softDeleteWithEditor(t, router)
	_ = viewerToken

	// Resolves the viewer's user_id from the members list (the owner can still
	// read the bdb while it's soft-deleted, so this works).
	_, bdb := testutil.DoRequest(t, router, http.MethodGet,
		"/api/bdbs/v1/bdbs/"+bdbID, ownerToken, nil)
	members := bdb["members"].([]any)
	var viewerID string
	for _, m := range members {
		mm := m.(map[string]any)
		if mm["role"] == "viewer" {
			viewerID = mm["user_id"].(string)
			break
		}
	}
	if viewerID == "" {
		t.Fatalf("could not resolve viewer user_id from members %v", members)
	}

	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{
			name:   "PATCH bdb",
			method: http.MethodPatch,
			path:   "/api/bdbs/v1/bdbs/" + bdbID,
			body:   map[string]string{"name": "Renamed", "description": ""},
		},
		{
			name:   "PATCH member role",
			method: http.MethodPatch,
			path:   "/api/bdbs/v1/bdbs/" + bdbID + "/members/" + viewerID,
			body:   map[string]string{"role": "editor"},
		},
		{
			name:   "DELETE member",
			method: http.MethodDelete,
			path:   "/api/bdbs/v1/bdbs/" + bdbID + "/members/" + viewerID,
			body:   nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, resp := testutil.DoRequest(t, router, tc.method, tc.path, ownerToken, tc.body)
			if status != http.StatusConflict {
				t.Errorf("status = %d, want %d, body: %v", status, http.StatusConflict, resp)
			}
		})
	}
}

// TestBdbNames_OnePerOwnerWhateverTheCase checks that an owner can't give two of their bdbs one name, whatever its case
// and surrounding spaces, on creation (409) or rename (409), while another owner can use it and a bdb can change the
// case of its own name; and that a bdb sharing its name with an older one, from before the rule, still saves its other
// changes.
func TestBdbNames_OnePerOwnerWhateverTheCase(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	ada := userToken(t, seedUser(t, "ada@test.com"))
	bob := userToken(t, seedUser(t, "bob@test.com"))

	if status, _ := createBdb(t, router, ada, "Notes", ""); status != http.StatusCreated {
		t.Fatalf("create Notes = %d, want 201", status)
	}
	for _, name := range []string{"notes", " NOTES "} {
		if status, resp := createBdb(t, router, ada, name, ""); status != http.StatusConflict ||
			resp["error"] != "a bdb of yours already has that name" {
			t.Errorf("create %q = %d %v, want 409 and the error", name, status, resp)
		}
	}
	if status, _ := createBdb(t, router, bob, "notes", ""); status != http.StatusCreated {
		t.Errorf("create notes as another owner = %d, want 201", status)
	}

	_, other := createBdb(t, router, ada, "Other", "")
	otherID := other["id"].(string)
	rename := func(id string, body map[string]any) (int, map[string]any) {
		t.Helper()
		return testutil.DoRequest(t, router, http.MethodPatch, "/api/bdbs/v1/bdbs/"+id, ada, body)
	}
	if status, resp := rename(otherID, map[string]any{"name": "NOTES"}); status != http.StatusConflict {
		t.Errorf("rename Other to NOTES = %d %v, want 409", status, resp)
	}
	if status, resp := rename(otherID, map[string]any{"name": "OTHER"}); status != http.StatusOK ||
		resp["name"] != "OTHER" {
		t.Errorf("rename Other to OTHER = %d %v, want 200", status, resp)
	}

	if err := testDB.Exec(context.Background(), `UPDATE bdbs SET name = 'notes' WHERE id = $1`, otherID); err != nil {
		t.Fatalf("giving Other an older bdb's name: %v", err)
	}
	if status, resp := rename(otherID, map[string]any{"description": "kept apart"}); status != http.StatusOK {
		t.Errorf("updating the description of a bdb sharing an older name = %d %v, want 200", status, resp)
	}
}
