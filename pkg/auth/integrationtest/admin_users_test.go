//go:build integration

package integrationtest

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

func TestAdminCreateUser_Success(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	adminID := seedAdmin(t, "createadmin@example.com", "strongpass123")

	status, resp := testutil.DoRequest(t, router, http.MethodPost, "/api/auth/v1/admin/users",
		adminToken(t, adminID),
		map[string]string{
			"email": "newuser@example.com", "password": "strongpass123",
			"first_name": "New", "last_name": "User",
		})
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusCreated, resp)
	}
	if resp["email"] != "newuser@example.com" {
		t.Errorf("email = %v, want newuser@example.com", resp["email"])
	}
	if resp["id"] == nil {
		t.Error("expected id in response")
	}
}

func TestAdminCreateUser_DuplicateEmail(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	adminID := seedAdmin(t, "dupadmin@example.com", "strongpass123")

	status, _ := testutil.DoRequest(t, router, http.MethodPost, "/api/auth/v1/admin/users",
		adminToken(t, adminID),
		map[string]string{"email": "dup@example.com", "password": "strongpass123"})
	if status != http.StatusCreated {
		t.Fatalf("first create: status = %d, want %d", status, http.StatusCreated)
	}

	status, _ = testutil.DoRequest(t, router, http.MethodPost, "/api/auth/v1/admin/users",
		adminToken(t, adminID),
		map[string]string{"email": "dup@example.com", "password": "strongpass123"})
	if status != http.StatusConflict {
		t.Errorf("duplicate create: status = %d, want %d", status, http.StatusConflict)
	}
}

func TestAdminCreateUser_WeakPassword(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	adminID := seedAdmin(t, "weakadmin@example.com", "strongpass123")

	status, _ := testutil.DoRequest(t, router, http.MethodPost, "/api/auth/v1/admin/users",
		adminToken(t, adminID),
		map[string]string{"email": "weakpwd@example.com", "password": "short"})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestAdminCreateUser_ForbiddenForRegularUser(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := createUser(t, "regularuser@example.com", "strongpass123")

	status, _ := testutil.DoRequest(t, router, http.MethodPost, "/api/auth/v1/admin/users",
		userToken(t, userID),
		map[string]string{"email": "shouldfail@example.com", "password": "strongpass123"})
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

func TestAdminListUsers_Success(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	adminID := seedAdmin(t, "listadmin@example.com", "strongpass123")

	createUser(t, "usera@example.com", "strongpass123")
	createUser(t, "userb@example.com", "strongpass123")

	status, resp := testutil.DoRequest(t, router, http.MethodGet, "/api/auth/v1/admin/users",
		adminToken(t, adminID), nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	total, ok := resp["total"].(float64)
	if !ok || total != 3 {
		t.Errorf("total = %v, want 3", resp["total"])
	}
}

func TestAdminListUsers_Search(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	adminID := seedAdmin(t, "searchadmin@example.com", "strongpass123")

	createUser(t, "findme@example.com", "strongpass123")
	createUser(t, "other@example.com", "strongpass123")

	status, resp := testutil.DoRequest(t, router, http.MethodGet, "/api/auth/v1/admin/users?search=findme",
		adminToken(t, adminID), nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	total, ok := resp["total"].(float64)
	if !ok || total != 1 {
		t.Errorf("total = %v, want 1", resp["total"])
	}
}

// TestAdminListUsers_SearchMatchesWildcardsLiterally checks that LIKE wildcards in a search term match only
// themselves — "a_b" does not also find "axb" — and that matching ignores case.
func TestAdminListUsers_SearchMatchesWildcardsLiterally(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	adminID := seedAdmin(t, "wildcardadmin@example.com", "strongpass123")

	createUser(t, "a_b@example.com", "strongpass123")
	createUser(t, "axb@example.com", "strongpass123")
	createUser(t, "100%@example.com", "strongpass123")

	for search, want := range map[string]float64{"a_b": 1, "A_B": 1, "100%": 1, "0%25": 0} {
		status, resp := testutil.DoRequest(t, router, http.MethodGet,
			"/api/auth/v1/admin/users?search="+url.QueryEscape(search), adminToken(t, adminID), nil)
		if status != http.StatusOK {
			t.Fatalf("search %q: status = %d, want %d", search, status, http.StatusOK)
		}
		if total, ok := resp["total"].(float64); !ok || total != want {
			t.Errorf("search %q: total = %v, want %v", search, resp["total"], want)
		}
	}
}

func TestAdminGetUserRoles(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	adminID := seedAdmin(t, "rolesadmin@example.com", "strongpass123")
	targetID := createUser(t, "target@example.com", "strongpass123")

	status, resp := testutil.DoRequest(t, router, http.MethodGet,
		"/api/auth/v1/admin/users/"+targetID+"/roles",
		adminToken(t, adminID), nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	roles, ok := resp["roles"].([]any)
	if !ok {
		t.Fatalf("expected roles array, got %v", resp["roles"])
	}
	if len(roles) != 1 || roles[0] != "user" {
		t.Errorf("roles = %v, want [user]", roles)
	}
}

func TestAdminListUsers_ForbiddenForUser(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := createUser(t, "forbiduser@example.com", "strongpass123")

	status, _ := testutil.DoRequest(t, router, http.MethodGet, "/api/auth/v1/admin/users",
		userToken(t, userID), nil)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}
