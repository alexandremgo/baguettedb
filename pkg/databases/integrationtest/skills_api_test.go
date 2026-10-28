//go:build integration

package integrationtest

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// skillsPath returns the API path for skills endpoints scoped to a bdb.
func skillsPath(bdbID, suffix string) string {
	base := fmt.Sprintf("/api/databases/v1/bdbs/%s/skills", bdbID)
	if suffix == "" {
		return base
	}
	return base + "/" + suffix
}

// --- Public REST CRUD ---

func TestSkills_REST_CreateAndGet(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Pre-creates a target table so the skill targets a real table name.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE inventory (id INTEGER PRIMARY KEY)"})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, skillsPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{
			"name":          "inventory-rules",
			"description":   "Inventory operating rules.",
			"body":          "Always normalize SKU to uppercase.",
			"target_tables": []string{"inventory"},
		})
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, want 201, body: %v", status, resp)
	}
	id := resp["id"].(string)
	if _, err := uuid.Parse(id); err != nil {
		t.Errorf("created id is not a UUID: %v", id)
	}

	// Fetches by name.
	status, resp = testutil.DoRequest(t, router, http.MethodGet, skillsPath(bdbID, "inventory-rules"),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("get-by-name status = %d, want 200, body: %v", status, resp)
	}
	if resp["body"] != "Always normalize SKU to uppercase." {
		t.Errorf("body = %v, want full body", resp["body"])
	}

	// Fetches by UUID.
	status, resp = testutil.DoRequest(t, router, http.MethodGet, skillsPath(bdbID, id),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("get-by-uuid status = %d, want 200, body: %v", status, resp)
	}
	if resp["name"] != "inventory-rules" {
		t.Errorf("name = %v, want inventory-rules", resp["name"])
	}
}

func TestSkills_REST_List(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	for i, name := range []string{"alpha", "bravo", "charlie"} {
		_ = i
		testutil.DoRequest(t, router, http.MethodPost, skillsPath(bdbID, ""),
			userToken(t, userID),
			map[string]any{
				"name":        name,
				"description": "desc " + name,
				"body":        "body " + name,
			})
	}

	status, list := testutil.DoRequestList(t, router, http.MethodGet, skillsPath(bdbID, ""),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("list status = %d, want 200", status)
	}
	if len(list) != 3 {
		t.Fatalf("list length = %d, want 3", len(list))
	}
	// Sorted by name ascending.
	for i, want := range []string{"alpha", "bravo", "charlie"} {
		if list[i]["name"] != want {
			t.Errorf("list[%d] name = %v, want %v", i, list[i]["name"], want)
		}
	}
}

func TestSkills_REST_UpdatePartial(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE foo (id INTEGER PRIMARY KEY)"})

	testutil.DoRequest(t, router, http.MethodPost, skillsPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{
			"name":          "to-edit",
			"description":   "Original description.",
			"body":          "Original body.",
			"target_tables": []string{"foo"},
		})

	newBody := "Updated body."
	status, resp := testutil.DoRequest(t, router, http.MethodPut, skillsPath(bdbID, "to-edit"),
		userToken(t, userID),
		map[string]any{"body": newBody})
	if status != http.StatusOK {
		t.Fatalf("update status = %d, want 200, body: %v", status, resp)
	}
	if resp["body"] != newBody {
		t.Errorf("body = %v, want %v", resp["body"], newBody)
	}
	if resp["description"] != "Original description." {
		t.Errorf("description = %v, want unchanged", resp["description"])
	}
	targets, _ := resp["target_tables"].([]any)
	if len(targets) != 1 || targets[0] != "foo" {
		t.Errorf("target_tables = %v, want [foo] unchanged", targets)
	}
}

func TestSkills_REST_UpdateClearsTargets(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE x (id INTEGER PRIMARY KEY)"})

	testutil.DoRequest(t, router, http.MethodPost, skillsPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{
			"name":          "demote",
			"description":   "Was table-targeted.",
			"body":          "body",
			"target_tables": []string{"x"},
		})

	status, resp := testutil.DoRequest(t, router, http.MethodPut, skillsPath(bdbID, "demote"),
		userToken(t, userID),
		map[string]any{"target_tables": []string{}})
	if status != http.StatusOK {
		t.Fatalf("update status = %d, want 200, body: %v", status, resp)
	}
	targets, _ := resp["target_tables"].([]any)
	if len(targets) != 0 {
		t.Errorf("target_tables = %v, want empty (bdb-level)", targets)
	}
}

func TestSkills_REST_Delete(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, skillsPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{
			"name":        "to-delete",
			"description": "Bye.",
			"body":        "body",
		})

	status, _ := testutil.DoRequest(t, router, http.MethodDelete, skillsPath(bdbID, "to-delete"),
		userToken(t, userID), nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", status)
	}

	status, _ = testutil.DoRequest(t, router, http.MethodGet, skillsPath(bdbID, "to-delete"),
		userToken(t, userID), nil)
	if status != http.StatusNotFound {
		t.Errorf("get-after-delete status = %d, want 404", status)
	}
}

// --- Error mapping ---

func TestSkills_REST_DuplicateNameReturns409(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, skillsPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{"name": "dup", "description": "first", "body": "first"})

	status, _ := testutil.DoRequest(t, router, http.MethodPost, skillsPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{"name": "dup", "description": "second", "body": "second"})
	if status != http.StatusConflict {
		t.Errorf("duplicate-name status = %d, want 409", status)
	}
}

func TestSkills_REST_InvalidNameReturns400(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	status, _ := testutil.DoRequest(t, router, http.MethodPost, skillsPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{"name": "Bad Name", "description": "x", "body": "y"})
	if status != http.StatusBadRequest {
		t.Errorf("invalid-name status = %d, want 400", status)
	}
}

func TestSkills_REST_BodyTooLargeReturns413(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	bigBody := make([]byte, 100*1024+1)
	for i := range bigBody {
		bigBody[i] = 'a'
	}

	status, _ := testutil.DoRequest(t, router, http.MethodPost, skillsPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{"name": "too-big", "description": "x", "body": string(bigBody)})
	if status != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized-body status = %d, want 413", status)
	}
}

// --- Permissions ---

func TestSkills_REST_ViewerCanRead(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	ownerRouter := setupRouterWithRole(t, authz.BdbRoleOwner)
	testutil.DoRequest(t, ownerRouter, http.MethodPost, skillsPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{"name": "viewer-read", "description": "x", "body": "y"})

	viewerRouter := setupRouterWithRole(t, authz.BdbRoleViewer)
	status, _ := testutil.DoRequestList(t, viewerRouter, http.MethodGet, skillsPath(bdbID, ""),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Errorf("viewer list status = %d, want 200", status)
	}

	status, _ = testutil.DoRequest(t, viewerRouter, http.MethodGet, skillsPath(bdbID, "viewer-read"),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Errorf("viewer get status = %d, want 200", status)
	}
}

func TestSkills_REST_ViewerCannotWrite(t *testing.T) {
	truncateAll(t)
	router := setupRouterWithRole(t, authz.BdbRoleViewer)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	status, _ := testutil.DoRequest(t, router, http.MethodPost, skillsPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{"name": "cant-create", "description": "x", "body": "y"})
	if status != http.StatusForbidden {
		t.Errorf("viewer-create status = %d, want 403", status)
	}
}

func TestSkills_REST_Unauthenticated(t *testing.T) {
	router := setupRouter(t)
	bdbID := uuid.New().String()

	status, _ := testutil.DoRequestList(t, router, http.MethodGet, skillsPath(bdbID, ""), "", nil)
	if status != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401", status)
	}
}

// --- DELETE FK cascade ---

func TestSkills_REST_DeleteCascadesTargets(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	for _, tbl := range []string{"a", "b"} {
		testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
			userToken(t, userID),
			map[string]any{"sql": "CREATE TABLE " + tbl + " (id INTEGER PRIMARY KEY)"})
	}

	testutil.DoRequest(t, router, http.MethodPost, skillsPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{
			"name":          "fk-cascade",
			"description":   "x",
			"body":          "y",
			"target_tables": []string{"a", "b"},
		})

	status, _ := testutil.DoRequest(t, router, http.MethodDelete, skillsPath(bdbID, "fk-cascade"),
		userToken(t, userID), nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", status)
	}

	// Re-creating with the same name verifies that no _skill_targets rows
	// remain (otherwise the FK would still point at the now-gone skill ID).
	status, resp := testutil.DoRequest(t, router, http.MethodPost, skillsPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{
			"name":        "fk-cascade",
			"description": "fresh",
			"body":        "fresh",
		})
	if status != http.StatusCreated {
		t.Fatalf("re-create status = %d, want 201, body: %v", status, resp)
	}
	targets, _ := resp["target_tables"].([]any)
	if len(targets) != 0 {
		t.Errorf("re-created skill targets = %v, want empty", targets)
	}
}
