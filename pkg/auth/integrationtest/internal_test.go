//go:build integration

package integrationtest

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

func TestInternalGetUserProfiles(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)

	id1 := createUser(t, "alice@example.com", "strongpass123")
	id2 := createUser(t, "bob@example.com", "strongpass123")
	unknownID := uuid.New().String()

	t.Run("empty list returns empty profiles", func(t *testing.T) {
		status, resp := testutil.DoRequest(t, router, http.MethodPost,
			"/internal/api/auth/v1/user-profiles", "",
			map[string]any{"user_ids": []string{}})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
		}
		profiles, ok := resp["profiles"].([]any)
		if !ok {
			t.Fatalf("expected profiles array, got %v", resp["profiles"])
		}
		if len(profiles) != 0 {
			t.Errorf("expected 0 profiles, got %d", len(profiles))
		}
	})

	t.Run("two valid IDs return both profiles", func(t *testing.T) {
		status, resp := testutil.DoRequest(t, router, http.MethodPost,
			"/internal/api/auth/v1/user-profiles", "",
			map[string]any{"user_ids": []string{id1, id2}})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
		}
		profiles, ok := resp["profiles"].([]any)
		if !ok {
			t.Fatalf("expected profiles array, got %v", resp["profiles"])
		}
		if len(profiles) != 2 {
			t.Fatalf("expected 2 profiles, got %d: %v", len(profiles), profiles)
		}
		gotEmails := map[string]bool{}
		for _, p := range profiles {
			entry, _ := p.(map[string]any)
			gotEmails[entry["email"].(string)] = true
			if entry["first_name"] != "Test" {
				t.Errorf("first_name = %v, want Test", entry["first_name"])
			}
		}
		if !gotEmails["alice@example.com"] || !gotEmails["bob@example.com"] {
			t.Errorf("missing expected emails, got %v", gotEmails)
		}
	})

	t.Run("mix of valid and unknown IDs returns only the valid one", func(t *testing.T) {
		status, resp := testutil.DoRequest(t, router, http.MethodPost,
			"/internal/api/auth/v1/user-profiles", "",
			map[string]any{"user_ids": []string{id1, unknownID}})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
		}
		profiles, _ := resp["profiles"].([]any)
		if len(profiles) != 1 {
			t.Fatalf("expected 1 profile, got %d", len(profiles))
		}
		entry, _ := profiles[0].(map[string]any)
		if entry["user_id"] != id1 {
			t.Errorf("user_id = %v, want %v", entry["user_id"], id1)
		}
	})

	t.Run("malformed UUID returns 400", func(t *testing.T) {
		status, _ := testutil.DoRequest(t, router, http.MethodPost,
			"/internal/api/auth/v1/user-profiles", "",
			map[string]any{"user_ids": []string{"not-a-uuid"}})
		if status != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
		}
	})
}
