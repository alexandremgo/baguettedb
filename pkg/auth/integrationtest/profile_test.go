//go:build integration

package integrationtest

import (
	"net/http"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

func TestMe_Success(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := createUser(t, "me@example.com", "strongpass123")

	status, resp := testutil.DoRequest(t, router, http.MethodGet, "/api/auth/v1/me", userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
	if resp["email"] != "me@example.com" {
		t.Errorf("email = %v, want me@example.com", resp["email"])
	}
}

func TestMe_Unauthorized(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)

	status, _ := testutil.DoRequest(t, router, http.MethodGet, "/api/auth/v1/me", "", nil)
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestChangePassword_Success(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := createUser(t, "pwd@example.com", "oldpassword1")

	status, _ := testutil.DoRequest(t, router, http.MethodPost, "/api/auth/v1/password",
		userToken(t, userID),
		map[string]string{"old_password": "oldpassword1", "new_password": "newpassword1"})
	if status != http.StatusOK {
		t.Errorf("status = %d, want %d", status, http.StatusOK)
	}
}

// TestChangePassword_WrongOld checks that a wrong current password is a 400 saying so, not a 401: the caller is
// signed in, and the web app reads a 401 as a session that ended, leaving the page without showing the error.
func TestChangePassword_WrongOld(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := createUser(t, "pwdwrong@example.com", "oldpassword1")

	status, body := testutil.DoRequest(t, router, http.MethodPost, "/api/auth/v1/password",
		userToken(t, userID),
		map[string]string{"old_password": "wrongpassword", "new_password": "newpassword1"})
	if status != http.StatusBadRequest || body["error"] != "incorrect current password" {
		t.Errorf("status = %d %v, want %d and the error", status, body, http.StatusBadRequest)
	}
}

func TestChangePassword_WeakNew(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := createUser(t, "pwdweak@example.com", "oldpassword1")

	status, _ := testutil.DoRequest(t, router, http.MethodPost, "/api/auth/v1/password",
		userToken(t, userID),
		map[string]string{"old_password": "oldpassword1", "new_password": "short"})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}
