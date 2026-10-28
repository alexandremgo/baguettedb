//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	authmodule "github.com/alexandremgo/baguettedb/pkg/auth"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

func TestAdminAssignRole_Success(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	adminID := seedAdmin(t, "assignadmin@example.com", "strongpass123")
	targetID := createUser(t, "assigntarget@example.com", "strongpass123")

	status, _ := testutil.DoRequest(t, router, http.MethodPost,
		"/api/auth/v1/admin/users/"+targetID+"/roles",
		adminToken(t, adminID),
		map[string]string{"role": "admin"})
	if status != http.StatusOK {
		t.Errorf("assign role: status = %d, want %d", status, http.StatusOK)
	}

	getStatus, getResp := testutil.DoRequest(t, router, http.MethodGet,
		"/api/auth/v1/admin/users/"+targetID+"/roles",
		adminToken(t, adminID), nil)
	if getStatus != http.StatusOK {
		t.Fatalf("get roles: status = %d, want %d", getStatus, http.StatusOK)
	}

	roles := getResp["roles"].([]any)
	found := false
	for _, r := range roles {
		if r == "admin" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("roles = %v, expected to contain 'admin'", roles)
	}
}

func TestAdminRemoveRole_Success(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	adminID := seedAdmin(t, "removeadmin@example.com", "strongpass123")
	targetID := createUser(t, "removetarget@example.com", "strongpass123")
	targetUID, _ := uuid.Parse(targetID)

	if err := repos().roles.AssignRole(context.Background(), targetUID, authz.RoleAdmin); err != nil {
		t.Fatalf("assigning admin role to target: %v", err)
	}

	status, _ := testutil.DoRequest(t, router, http.MethodDelete,
		"/api/auth/v1/admin/users/"+targetID+"/roles/admin",
		adminToken(t, adminID), nil)
	if status != http.StatusOK {
		t.Errorf("remove role: status = %d, want %d", status, http.StatusOK)
	}

	getStatus, getResp := testutil.DoRequest(t, router, http.MethodGet,
		"/api/auth/v1/admin/users/"+targetID+"/roles",
		adminToken(t, adminID), nil)
	if getStatus != http.StatusOK {
		t.Fatalf("get roles: status = %d, want %d", getStatus, http.StatusOK)
	}

	roles := getResp["roles"].([]any)
	for _, r := range roles {
		if r == "admin" {
			t.Error("expected admin role to be removed, but it's still present")
		}
	}
}

func TestAdminAssignRole_InvalidRole(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	adminID := seedAdmin(t, "invalidroleadmin@example.com", "strongpass123")
	targetID := createUser(t, "invalidroletarget@example.com", "strongpass123")

	status, _ := testutil.DoRequest(t, router, http.MethodPost,
		"/api/auth/v1/admin/users/"+targetID+"/roles",
		adminToken(t, adminID),
		map[string]string{"role": "superadmin"})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestAdminRoles_ForbiddenForUser(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := createUser(t, "forbidrole@example.com", "strongpass123")
	targetID := uuid.New().String()

	tests := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{
			"assign role", http.MethodPost,
			"/api/auth/v1/admin/users/" + targetID + "/roles",
			map[string]string{"role": "admin"},
		},
		{
			"remove role", http.MethodDelete,
			"/api/auth/v1/admin/users/" + targetID + "/roles/admin", nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, _ := testutil.DoRequest(t, router, tt.method, tt.path, userToken(t, userID), tt.body)
			if status != http.StatusForbidden {
				t.Errorf("status = %d, want %d", status, http.StatusForbidden)
			}
		})
	}
}

// TestAdminRoutes_SetupOptionsPolicy checks that a role added through SetupOptions.Policy is honored end to end: its
// token reaches the admin routes the role is granted, and an admin can assign it, while a role the policy does not
// know is still refused. The same requests against an app set up with the zero policy are refused, so the grants come
// from the option and not from the built-in roles.
func TestAdminRoutes_SetupOptionsPolicy(t *testing.T) {
	truncateAll(t)
	const roleSupport authz.Role = "support"
	extended := setupWithOAuth(t, authmodule.SetupOptions{
		Policy: authz.Policy{}.With(roleSupport, authz.PermManageUsers, authz.PermManageRoles),
	})
	builtIn := setupWithOAuth(t, authmodule.SetupOptions{})

	adminID := createSessionUser(t, extended, "policy-admin@example.com").String()
	supportID := createSessionUser(t, extended, "support@example.com").String()
	targetID := createSessionUser(t, extended, "support-target@example.com").String()
	supportToken, err := testJWKS.BuildToken(supportID, []string{string(roleSupport)})
	if err != nil {
		t.Fatalf("building the support token: %v", err)
	}
	rolesPath := "/api/auth/v1/admin/users/" + targetID + "/roles"

	tests := []struct {
		name       string
		router     http.Handler
		method     string
		path       string
		token      string
		body       any
		wantStatus int
	}{
		{"extended policy: support lists users", extended.Router, http.MethodGet, "/api/auth/v1/admin/users",
			supportToken, nil, http.StatusOK},
		{"extended policy: support assigns a role", extended.Router, http.MethodPost, rolesPath,
			supportToken, map[string]string{"role": "user"}, http.StatusOK},
		{"extended policy: admin assigns support", extended.Router, http.MethodPost, rolesPath,
			adminToken(t, adminID), map[string]string{"role": string(roleSupport)}, http.StatusOK},
		{"extended policy: an unknown role is still refused", extended.Router, http.MethodPost, rolesPath,
			adminToken(t, adminID), map[string]string{"role": "superadmin"}, http.StatusBadRequest},
		{"built-in policy: support cannot list users", builtIn.Router, http.MethodGet, "/api/auth/v1/admin/users",
			supportToken, nil, http.StatusForbidden},
		{"built-in policy: support is not assignable", builtIn.Router, http.MethodPost, rolesPath,
			adminToken(t, adminID), map[string]string{"role": string(roleSupport)}, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := testutil.DoRequest(t, tt.router, tt.method, tt.path, tt.token, tt.body)
			if status != tt.wantStatus {
				t.Errorf("%s %s = %d, want %d (body %v)", tt.method, tt.path, status, tt.wantStatus, body)
			}
		})
	}
}
