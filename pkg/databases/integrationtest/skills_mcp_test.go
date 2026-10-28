//go:build integration

package integrationtest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// callSkillTool wraps callTool but expects the response to deserialize as a
// single skill detail object (e.g. for create_skill, update_skill, get_skill).
func callSkillTool(t *testing.T, c *mcpclient.Client, name string, args map[string]any) map[string]any {
	t.Helper()
	result := callTool(t, c, name, args)
	if result.IsError {
		t.Fatalf("tool %s returned error: %v", name, result.Content)
	}
	if len(result.Content) == 0 {
		t.Fatalf("tool %s: empty content", name)
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("tool %s: expected TextContent, got %T", name, result.Content[0])
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(text.Text), &data); err != nil {
		t.Fatalf("tool %s: unmarshal: %v (text=%s)", name, err, text.Text)
	}
	return data
}

func parseToolJSONList(t *testing.T, c *mcpclient.Client, name string, args map[string]any) map[string]any {
	t.Helper()
	result := callTool(t, c, name, args)
	if result.IsError {
		t.Fatalf("tool %s returned error: %v", name, result.Content)
	}
	return parseToolJSON(t, result)
}

// --- Discovery ---

func TestMCP_ListTools_IncludesSkillTools(t *testing.T) {
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

	for _, expected := range []string{"create_skill", "update_skill", "get_skill"} {
		if !names[expected] {
			t.Errorf("expected tool %q not registered", expected)
		}
	}
}

// --- create_skill ---

func TestMCP_CreateSkill_WithTargets(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	// Schema must exist so the target tables look real (not enforced today, but
	// a realistic agent would create the table first).
	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE groceries (id INTEGER PRIMARY KEY, name TEXT)",
	})

	skill := callSkillTool(t, c, "create_skill", map[string]any{
		"bdb_id":        bdbID,
		"name":          "groceries-flow",
		"description":   "How to keep the grocery list organized.",
		"body":          "# Groceries\nAdd items as TEXT rows.",
		"target_tables": []any{"groceries"},
	})

	if skill["name"] != "groceries-flow" {
		t.Errorf("name = %v, want groceries-flow", skill["name"])
	}
	if skill["description"] == nil || skill["description"].(string) == "" {
		t.Errorf("description not returned")
	}
	if skill["body"] == nil || skill["body"].(string) == "" {
		t.Errorf("body not returned")
	}
	targets, _ := skill["target_tables"].([]any)
	if len(targets) != 1 || targets[0] != "groceries" {
		t.Errorf("target_tables = %v, want [groceries]", targets)
	}
	if _, err := uuid.Parse(skill["id"].(string)); err != nil {
		t.Errorf("id is not a UUID: %v", skill["id"])
	}
}

func TestMCP_CreateSkill_BdbLevel(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	skill := callSkillTool(t, c, "create_skill", map[string]any{
		"bdb_id":      bdbID,
		"name":        "bdb-overview",
		"description": "How this bdb is structured.",
		"body":        "Bdb-level guidance.",
	})

	targets, _ := skill["target_tables"].([]any)
	if len(targets) != 0 {
		t.Errorf("target_tables = %v, want empty", targets)
	}
}

func TestMCP_CreateSkill_DuplicateName(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callSkillTool(t, c, "create_skill", map[string]any{
		"bdb_id":      bdbID,
		"name":        "dup-name",
		"description": "First.",
		"body":        "first body.",
	})

	result := callTool(t, c, "create_skill", map[string]any{
		"bdb_id":      bdbID,
		"name":        "dup-name",
		"description": "Second.",
		"body":        "second body.",
	})
	if !result.IsError {
		t.Fatal("expected error for duplicate skill name")
	}
}

func TestMCP_CreateSkill_BodyTooLarge(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	bigBody := strings.Repeat("a", domain.SkillBodyMaxBytes+1)

	result := callTool(t, c, "create_skill", map[string]any{
		"bdb_id":      bdbID,
		"name":        "too-big",
		"description": "Oversized body.",
		"body":        bigBody,
	})
	if !result.IsError {
		t.Fatal("expected error for oversized body")
	}
}

// TestMCP_CreateSkill_TargetTableMustExist verifies that create_skill rejects
// target_tables containing names that don't exist in the bdb's database.
func TestMCP_CreateSkill_TargetTableMustExist(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	// No table created. target_tables references a non-existent name.
	result := callTool(t, c, "create_skill", map[string]any{
		"bdb_id":        bdbID,
		"name":          "ghost-target",
		"description":   "Targets a missing table.",
		"body":          "body",
		"target_tables": []any{"missing_table"},
	})
	if !result.IsError {
		t.Fatal("expected error when target_tables references a non-existent table")
	}
}

// TestMCP_UpdateSkill_TargetTableMustExist verifies that update_skill rejects
// patches whose target_tables include a non-existent name.
func TestMCP_UpdateSkill_TargetTableMustExist(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE real_table (id INTEGER PRIMARY KEY)",
	})
	callSkillTool(t, c, "create_skill", map[string]any{
		"bdb_id":        bdbID,
		"name":          "real-targets",
		"description":   "Initially targets real_table.",
		"body":          "body",
		"target_tables": []any{"real_table"},
	})

	result := callTool(t, c, "update_skill", map[string]any{
		"bdb_id":        bdbID,
		"ref":           "real-targets",
		"target_tables": []any{"real_table", "ghost"},
	})
	if !result.IsError {
		t.Fatal("expected error when update target_tables references a non-existent table")
	}
}

func TestMCP_CreateSkill_InvalidName(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	result := callTool(t, c, "create_skill", map[string]any{
		"bdb_id":      bdbID,
		"name":        "Bad Name With Spaces",
		"description": "Invalid slug.",
		"body":        "body",
	})
	if !result.IsError {
		t.Fatal("expected error for invalid name")
	}
}

// --- get_skill ---

func TestMCP_GetSkill_ByNameAndUUID(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	created := callSkillTool(t, c, "create_skill", map[string]any{
		"bdb_id":      bdbID,
		"name":        "inventory-flow",
		"description": "Inventory rules.",
		"body":        "# Inventory\nUse SKU.",
	})
	id := created["id"].(string)

	byName := callSkillTool(t, c, "get_skill", map[string]any{
		"bdb_id": bdbID,
		"ref":    "inventory-flow",
	})
	if byName["id"] != id {
		t.Errorf("by-name id = %v, want %v", byName["id"], id)
	}
	if byName["body"].(string) == "" {
		t.Error("expected body in get_skill result")
	}

	byUUID := callSkillTool(t, c, "get_skill", map[string]any{
		"bdb_id": bdbID,
		"ref":    id,
	})
	if byUUID["name"] != "inventory-flow" {
		t.Errorf("by-uuid name = %v, want inventory-flow", byUUID["name"])
	}
}

func TestMCP_GetSkill_NotFound(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	result := callTool(t, c, "get_skill", map[string]any{
		"bdb_id": bdbID,
		"ref":    "nope",
	})
	if !result.IsError {
		t.Fatal("expected error for missing skill")
	}
}

// --- update_skill ---

func TestMCP_UpdateSkill_PartialBody(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE foo (id INTEGER PRIMARY KEY)",
	})
	callSkillTool(t, c, "create_skill", map[string]any{
		"bdb_id":        bdbID,
		"name":          "to-edit",
		"description":   "Original description.",
		"body":          "Original body.",
		"target_tables": []any{"foo"},
	})

	updated := callSkillTool(t, c, "update_skill", map[string]any{
		"bdb_id": bdbID,
		"ref":    "to-edit",
		"body":   "Brand new body.",
	})

	if updated["body"] != "Brand new body." {
		t.Errorf("body = %v, want updated body", updated["body"])
	}
	if updated["description"] != "Original description." {
		t.Errorf("description = %v, want unchanged", updated["description"])
	}
	targets, _ := updated["target_tables"].([]any)
	if len(targets) != 1 || targets[0] != "foo" {
		t.Errorf("target_tables = %v, want [foo] unchanged", targets)
	}
}

func TestMCP_UpdateSkill_ReplaceTargets(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	for _, tableName := range []string{"a", "b", "c"} {
		callTool(t, c, "execute_schema", map[string]any{
			"bdb_id": bdbID,
			"sql":    fmt.Sprintf("CREATE TABLE %s (id INTEGER PRIMARY KEY)", tableName),
		})
	}
	callSkillTool(t, c, "create_skill", map[string]any{
		"bdb_id":        bdbID,
		"name":          "swap-targets",
		"description":   "Targets swap.",
		"body":          "body",
		"target_tables": []any{"a", "b"},
	})

	updated := callSkillTool(t, c, "update_skill", map[string]any{
		"bdb_id":        bdbID,
		"ref":           "swap-targets",
		"target_tables": []any{"c"},
	})

	targets, _ := updated["target_tables"].([]any)
	if len(targets) != 1 || targets[0] != "c" {
		t.Errorf("target_tables = %v, want [c]", targets)
	}
}

func TestMCP_UpdateSkill_ToBdbLevel(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE x (id INTEGER PRIMARY KEY)",
	})
	callSkillTool(t, c, "create_skill", map[string]any{
		"bdb_id":        bdbID,
		"name":          "demote",
		"description":   "Was table-targeted.",
		"body":          "body",
		"target_tables": []any{"x"},
	})

	updated := callSkillTool(t, c, "update_skill", map[string]any{
		"bdb_id":        bdbID,
		"ref":           "demote",
		"target_tables": []any{},
	})

	targets, _ := updated["target_tables"].([]any)
	if len(targets) != 0 {
		t.Errorf("target_tables = %v, want empty (bdb-level)", targets)
	}
}

// --- describe_table enrichment ---

func TestMCP_DescribeTable_IncludesTargetingSkillBodies(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE inventory (id INTEGER PRIMARY KEY, sku TEXT)",
	})

	callSkillTool(t, c, "create_skill", map[string]any{
		"bdb_id":        bdbID,
		"name":          "inventory-rules",
		"description":   "Inventory rules.",
		"body":          "Always use SKU.",
		"target_tables": []any{"inventory"},
	})
	callSkillTool(t, c, "create_skill", map[string]any{
		"bdb_id":      bdbID,
		"name":        "bdb-only",
		"description": "Bdb.",
		"body":        "Bdb-level body.",
	})

	data := parseToolJSONList(t, c, "describe_table", map[string]any{
		"bdb_id":     bdbID,
		"table_name": "inventory",
	})
	skills, _ := data["skills"].([]any)
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill in describe_table, got %d: %v", len(skills), skills)
	}
	s := skills[0].(map[string]any)
	if s["name"] != "inventory-rules" {
		t.Errorf("skill name = %v, want inventory-rules", s["name"])
	}
	if s["body"] != "Always use SKU." {
		t.Errorf("skill body = %v, want full body", s["body"])
	}
}

// --- DDL cascade ---

func TestMCP_DropTable_CascadesSkillTargets(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE temp_table (id INTEGER PRIMARY KEY)",
	})
	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE keep_table (id INTEGER PRIMARY KEY)",
	})

	callSkillTool(t, c, "create_skill", map[string]any{
		"bdb_id":        bdbID,
		"name":          "multi-target",
		"description":   "Touches both.",
		"body":          "body",
		"target_tables": []any{"temp_table", "keep_table"},
	})

	// Drops one of the two target tables.
	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "DROP TABLE temp_table",
	})

	skill := callSkillTool(t, c, "get_skill", map[string]any{
		"bdb_id": bdbID,
		"ref":    "multi-target",
	})
	targets, _ := skill["target_tables"].([]any)
	if len(targets) != 1 || targets[0] != "keep_table" {
		t.Errorf("after drop, target_tables = %v, want [keep_table]", targets)
	}
}

func TestMCP_DropTable_KeepsOrphanedSkill(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE only_target (id INTEGER PRIMARY KEY)",
	})

	callSkillTool(t, c, "create_skill", map[string]any{
		"bdb_id":        bdbID,
		"name":          "single-target",
		"description":   "Single target.",
		"body":          "body",
		"target_tables": []any{"only_target"},
	})

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "DROP TABLE only_target",
	})

	// Skill is kept (orphan policy: never auto-delete) but its targets are gone.
	skill := callSkillTool(t, c, "get_skill", map[string]any{
		"bdb_id": bdbID,
		"ref":    "single-target",
	})
	targets, _ := skill["target_tables"].([]any)
	if len(targets) != 0 {
		t.Errorf("after dropping last target, target_tables = %v, want empty", targets)
	}
}

// TestMCP_DropTable_AtomicWithChangeLogAndCascade verifies that a successful
// DROP TABLE commits the cascade cleanup AND the _change_log audit row in the
// same transaction. We can't easily inject a mid-transaction failure from the
// integration boundary, but we can prove they're committed together: after
// the DROP succeeds, both the targets cleanup and the change_log entry are
// observable in a single follow-up read.
func TestMCP_DropTable_AtomicWithChangeLogAndCascade(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE atomic_drop (id INTEGER PRIMARY KEY)",
	})
	callSkillTool(t, c, "create_skill", map[string]any{
		"bdb_id":        bdbID,
		"name":          "atomic-drop-skill",
		"description":   "Targets the table about to be dropped.",
		"body":          "body",
		"target_tables": []any{"atomic_drop"},
	})

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "DROP TABLE atomic_drop",
	})

	// _change_log row written (DDL kind, DROP operation).
	changeLog := parseToolJSONList(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql": "SELECT object_name, operation FROM _change_log WHERE kind = 'DDL' AND object_name = 'atomic_drop' " +
			"AND operation = 'DROP'",
	})
	rows, _ := changeLog["rows"].([]any)
	if len(rows) != 1 {
		t.Errorf("expected exactly 1 _change_log row for the drop, got %d: %v", len(rows), rows)
	}

	// _skill_targets row removed.
	targetsLog := parseToolJSONList(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT skill_id FROM _skill_targets WHERE table_name = 'atomic_drop'",
	})
	rows, _ = targetsLog["rows"].([]any)
	if len(rows) != 0 {
		t.Errorf("expected 0 remaining _skill_targets rows for the dropped table, got %d: %v", len(rows), rows)
	}

	// Skill itself kept per orphan policy.
	skill := callSkillTool(t, c, "get_skill", map[string]any{
		"bdb_id": bdbID,
		"ref":    "atomic-drop-skill",
	})
	targets, _ := skill["target_tables"].([]any)
	if len(targets) != 0 {
		t.Errorf("after drop, target_tables = %v, want empty", targets)
	}
}

func TestMCP_AlterRenameTable_CascadesSkillTargets(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE old_name (id INTEGER PRIMARY KEY)",
	})

	callSkillTool(t, c, "create_skill", map[string]any{
		"bdb_id":        bdbID,
		"name":          "rename-skill",
		"description":   "Follows rename.",
		"body":          "body",
		"target_tables": []any{"old_name"},
	})

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "ALTER TABLE old_name RENAME TO new_name",
	})

	skill := callSkillTool(t, c, "get_skill", map[string]any{
		"bdb_id": bdbID,
		"ref":    "rename-skill",
	})
	targets, _ := skill["target_tables"].([]any)
	if len(targets) != 1 || targets[0] != "new_name" {
		t.Errorf("after rename, target_tables = %v, want [new_name]", targets)
	}

	// describe_table on the new name finds the skill.
	desc := parseToolJSONList(t, c, "describe_table", map[string]any{
		"bdb_id":     bdbID,
		"table_name": "new_name",
	})
	skills, _ := desc["skills"].([]any)
	if len(skills) != 1 {
		t.Errorf("describe_table(new_name) skills count = %d, want 1", len(skills))
	}
}

// --- permissions ---

func TestMCP_CreateSkill_ViewerForbidden(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClientWithRole(t, userID, authz.BdbRoleViewer)

	result := callTool(t, c, "create_skill", map[string]any{
		"bdb_id":      bdbID,
		"name":        "viewer-attempt",
		"description": "Should fail.",
		"body":        "body",
	})
	if !result.IsError {
		t.Error("viewer should not be able to create skills")
	}
}

// TestMCP_GetSkill_ViewerAllowed confirms viewers can read a skill via get_skill.
// Skill discovery for bdb-bound viewers happens through describe_bdb (in
// the bdbs MCP module, covered by its own tests); here we verify the
// per-skill read path enforces only the same viewer-or-better permission.
func TestMCP_GetSkill_ViewerAllowed(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	ownerClient := setupMCPClientWithRole(t, userID, authz.BdbRoleOwner)
	callSkillTool(t, ownerClient, "create_skill", map[string]any{
		"bdb_id":      bdbID,
		"name":        "owner-skill",
		"description": "Owner.",
		"body":        "body",
	})

	viewerClient := setupMCPClientWithRole(t, userID, authz.BdbRoleViewer)
	skill := callSkillTool(t, viewerClient, "get_skill", map[string]any{
		"bdb_id": bdbID,
		"ref":    "owner-skill",
	})
	if skill["name"] != "owner-skill" {
		t.Errorf("viewer get_skill name = %v, want owner-skill", skill["name"])
	}
}
