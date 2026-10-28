//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// TestUndoAnyone_OwnerReversesMemberChange checks that an owner can reverse a change made by another member via the
// REST undo/anyone endpoint, and that the resulting redo entry lands on the owner's redo stack.
func TestUndoAnyone_OwnerReversesMemberChange(t *testing.T) {
	truncateAll(t)
	router := setupRouterWithRole(t, authz.BdbRoleOwner)
	aliceID := uuid.New().String()
	ownerID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, ownerID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	// Alice inserts a row.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), userToken(t, aliceID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	// Owner reverses it via undo_anyone.
	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo/anyone"),
		userToken(t, ownerID), nil)
	if status != http.StatusOK {
		t.Fatalf("undo_anyone status = %d, want %d", status, http.StatusOK)
	}

	// Alice's row is gone.
	_, items := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), userToken(t, ownerID),
		map[string]string{"sql": "SELECT COUNT(*) FROM items"})
	if c := extractRowValues(t, items["rows"].([]any)[0])[0]; c != float64(0) {
		t.Errorf("expected 0 rows after undo_anyone, got %v", c)
	}

	// Owner's redo stack received the reversed batch.
	_, redo := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), userToken(t, ownerID),
		map[string]string{"sql": "SELECT COUNT(*) FROM _redo_stack WHERE user_id = '" + ownerID + "'"})
	if c := extractRowValues(t, redo["rows"].([]any)[0])[0]; c != float64(1) {
		t.Errorf("expected 1 entry on owner's redo stack, got %v", c)
	}
}

// TestUndoAnyone_NonOwnerForbidden checks that a non-owner member calling undo/anyone or redo/anyone receives 403.
func TestUndoAnyone_NonOwnerForbidden(t *testing.T) {
	truncateAll(t)
	editorRouter := setupRouterWithRole(t, authz.BdbRoleEditor)
	aliceID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, editorRouter, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, aliceID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})
	testutil.DoRequest(t, editorRouter, http.MethodPost, dbPath(bdbID, "data"), userToken(t, aliceID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	status, _ := testutil.DoRequest(t, editorRouter, http.MethodPost, dbPath(bdbID, "changes/undo/anyone"),
		userToken(t, aliceID), nil)
	if status != http.StatusForbidden {
		t.Errorf("editor undo_anyone status = %d, want %d", status, http.StatusForbidden)
	}

	status, _ = testutil.DoRequest(t, editorRouter, http.MethodPost, dbPath(bdbID, "changes/redo/anyone"),
		userToken(t, aliceID), nil)
	if status != http.StatusForbidden {
		t.Errorf("editor redo_anyone status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestRedoAnyone_RoundTripOnMemberChange drives the full owner-mediated undo → redo cycle on a member's change.
func TestRedoAnyone_RoundTripOnMemberChange(t *testing.T) {
	truncateAll(t)
	router := setupRouterWithRole(t, authz.BdbRoleOwner)
	aliceID := uuid.New().String()
	ownerID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, ownerID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), userToken(t, aliceID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	// Owner undoes Alice's insert, then redoes it.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo/anyone"), userToken(t, ownerID), nil)
	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/redo/anyone"),
		userToken(t, ownerID), nil)
	if status != http.StatusOK {
		t.Fatalf("redo_anyone status = %d, want %d", status, http.StatusOK)
	}

	// Alice's row is back.
	_, items := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), userToken(t, ownerID),
		map[string]string{"sql": "SELECT name FROM items"})
	rows := items["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row after redo_anyone, got %d", len(rows))
	}
	if v := extractRowValues(t, rows[0])[0]; v != "milk" {
		t.Errorf("name = %v, want milk", v)
	}
}

// TestUndoAnyone_TopAtExecuteNoVerification confirms undo/anyone targets whichever entry is top at execute time,
// without any expected_batch_id parameter. Two members make concurrent writes; the owner's undo/anyone reverses the
// most-recent one.
func TestUndoAnyone_TopAtExecuteNoVerification(t *testing.T) {
	truncateAll(t)
	router := setupRouterWithRole(t, authz.BdbRoleOwner)
	aliceID := uuid.New().String()
	bobID := uuid.New().String()
	ownerID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, ownerID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), userToken(t, aliceID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('alice-row')"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), userToken(t, bobID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('bob-row')"})

	// Owner undoes — should reverse Bob's row (the most recent).
	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo/anyone"),
		userToken(t, ownerID), nil)
	if status != http.StatusOK {
		t.Fatalf("undo_anyone status = %d, want %d", status, http.StatusOK)
	}

	_, items := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), userToken(t, ownerID),
		map[string]string{"sql": "SELECT name FROM items ORDER BY name"})
	rows := items["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row remaining, got %d", len(rows))
	}
	if v := extractRowValues(t, rows[0])[0]; v != "alice-row" {
		t.Errorf("remaining row = %v, want alice-row (bob's was the top of stack)", v)
	}
}

// TestUndoAnyone_EmptyStack checks that undo/anyone on a fresh database returns 400 ErrNothingToUndo.
func TestUndoAnyone_EmptyStack(t *testing.T) {
	truncateAll(t)
	router := setupRouterWithRole(t, authz.BdbRoleOwner)
	ownerID := uuid.New().String()
	bdbID := uuid.New().String()

	// Touch the database without writing.
	testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""), userToken(t, ownerID), nil)

	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo/anyone"),
		userToken(t, ownerID), nil)
	if status != http.StatusBadRequest {
		t.Errorf("undo_anyone empty-stack status = %d, want %d", status, http.StatusBadRequest)
	}
}

// TestMCP_AnyoneTools_Registered is a smoke check that both new MCP tools are registered and discoverable.
func TestMCP_AnyoneTools_Registered(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	c := setupMCPClient(t, userID)

	result, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("listing tools: %v", err)
	}
	names := make(map[string]bool)
	for _, tool := range result.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"undo_last_change_anyone", "redo_last_change_anyone"} {
		if !names[want] {
			t.Errorf("expected tool %q to be registered", want)
		}
	}
}

// TestMCP_UndoChangeAnyone_OwnerCanUndoOtherUsersChange exercises the MCP tool end-to-end: an owner reverses an
// insert made by a member. Owner and member each get their own role-aware router (the stack backing is shared, so they
// see the same database).
func TestMCP_UndoChangeAnyone_OwnerCanUndoOtherUsersChange(t *testing.T) {
	truncateAll(t)
	aliceID := uuid.New().String()
	ownerID := uuid.New().String()
	bdbID := uuid.New().String()

	ownerClient := setupMCPClientWithRole(t, ownerID, authz.BdbRoleOwner)
	aliceClient := setupMCPClientWithRole(t, aliceID, authz.BdbRoleEditor)

	callTool(t, ownerClient, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})
	callTool(t, aliceClient, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('milk')",
	})

	result := callTool(t, ownerClient, "undo_last_change_anyone", map[string]any{
		"bdb_id": bdbID,
	})
	if result.IsError {
		t.Fatalf("undo_last_change_anyone failed: %v", result.Content)
	}

	q := callTool(t, ownerClient, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT COUNT(*) FROM items",
	})
	data := parseToolJSON(t, q)
	rows := data["rows"].([]any)
	cells := rows[0].([]any)
	count := cells[0].(map[string]any)["value"]
	if count != float64(0) {
		t.Errorf("expected 0 rows after undo_last_change_anyone, got %v", count)
	}
}

// TestMCP_UndoChangeAnyone_NonOwnerForbidden checks that a member (non-owner) hitting the MCP tool gets an error.
func TestMCP_UndoChangeAnyone_NonOwnerForbidden(t *testing.T) {
	truncateAll(t)
	aliceID := uuid.New().String()
	bdbID := uuid.New().String()

	editorClient := setupMCPClientWithRole(t, aliceID, authz.BdbRoleEditor)
	callTool(t, editorClient, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})
	callTool(t, editorClient, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('milk')",
	})

	if r := callTool(t, editorClient, "undo_last_change_anyone", map[string]any{"bdb_id": bdbID}); !r.IsError {
		t.Error("expected editor's undo_last_change_anyone to error (owner-only)")
	}
	if r := callTool(t, editorClient, "redo_last_change_anyone", map[string]any{"bdb_id": bdbID}); !r.IsError {
		t.Error("expected editor's redo_last_change_anyone to error (owner-only)")
	}
}

// TestMCP_RedoChangeAnyone_RoundTrip exercises the owner-mediated MCP undo → redo cycle on a member's change.
func TestMCP_RedoChangeAnyone_RoundTrip(t *testing.T) {
	truncateAll(t)
	aliceID := uuid.New().String()
	ownerID := uuid.New().String()
	bdbID := uuid.New().String()

	ownerClient := setupMCPClientWithRole(t, ownerID, authz.BdbRoleOwner)
	aliceClient := setupMCPClientWithRole(t, aliceID, authz.BdbRoleEditor)

	callTool(t, ownerClient, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})
	callTool(t, aliceClient, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('milk')",
	})

	callTool(t, ownerClient, "undo_last_change_anyone", map[string]any{"bdb_id": bdbID})
	result := callTool(t, ownerClient, "redo_last_change_anyone", map[string]any{"bdb_id": bdbID})
	if result.IsError {
		t.Fatalf("redo_last_change_anyone failed: %v", result.Content)
	}

	q := callTool(t, ownerClient, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT name FROM items",
	})
	rows := parseToolJSON(t, q)["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row after redo_last_change_anyone, got %d", len(rows))
	}
	name := rows[0].([]any)[0].(map[string]any)["value"]
	if name != "milk" {
		t.Errorf("name = %v, want milk", name)
	}
}

// TestMCP_UndoChangeAnyone_EmptyStack checks the error path on an unwritten bdb.
func TestMCP_UndoChangeAnyone_EmptyStack(t *testing.T) {
	truncateAll(t)
	ownerID := uuid.New().String()
	bdbID := uuid.New().String()
	ownerClient := setupMCPClientWithRole(t, ownerID, authz.BdbRoleOwner)

	// Touch the database without writing (list_mutations resolves the database without pushing).
	callTool(t, ownerClient, "list_changes", map[string]any{"bdb_id": bdbID})

	if r := callTool(t, ownerClient, "undo_last_change_anyone", map[string]any{"bdb_id": bdbID}); !r.IsError {
		t.Error("expected undo_last_change_anyone on empty stack to error")
	}
}
