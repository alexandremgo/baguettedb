//go:build integration

package integrationtest

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// toolResultText returns the first text content of a tool result, or "" when absent.
func toolResultText(result *mcp.CallToolResult) string {
	if len(result.Content) == 0 {
		return ""
	}
	if tc, ok := result.Content[0].(mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}

// schemaObjectCount returns how many sqlite_master rows carry the given object name.
func schemaObjectCount(t *testing.T, c *mcpclient.Client, bdbID, name string) int {
	t.Helper()
	sql := fmt.Sprintf("SELECT COUNT(*) FROM sqlite_master WHERE name = '%s'", strings.ReplaceAll(name, "'", "''"))
	r := callTool(t, c, "execute_query", map[string]any{"bdb_id": bdbID, "sql": sql})
	if r.IsError {
		t.Fatalf("counting schema object %q: %v", name, r.Content)
	}
	count := parseToolJSON(t, r)["rows"].([]any)[0].([]any)[0].(map[string]any)["value"].(float64)
	return int(count)
}

// columnCount returns how many columns of the given table carry the given column name.
func columnCount(t *testing.T, c *mcpclient.Client, bdbID, table, column string) int {
	t.Helper()
	sql := fmt.Sprintf("SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name = '%s'",
		table, strings.ReplaceAll(column, "'", "''"))
	r := callTool(t, c, "execute_query", map[string]any{"bdb_id": bdbID, "sql": sql})
	if r.IsError {
		t.Fatalf("counting column %q on %q: %v", column, table, r.Content)
	}
	count := parseToolJSON(t, r)["rows"].([]any)[0].([]any)[0].(map[string]any)["value"].(float64)
	return int(count)
}

// TestMCP_ExecuteSchema_InvalidTableNameRejected verifies that a CREATE TABLE with a non-conforming table
// name is rejected at admission (prepare time), names the identifier rule, and leaves no trace — no schema
// object and no change-log batch.
func TestMCP_ExecuteSchema_InvalidTableNameRejected(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	result := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    `CREATE TABLE "my café" (id INTEGER PRIMARY KEY)`,
	})
	if !result.IsError {
		t.Fatal("expected error for accented table name, got success")
	}
	if text := toolResultText(result); !strings.Contains(text, "identifier") {
		t.Errorf("error text %q does not name the identifier rule", text)
	}

	if n := schemaObjectCount(t, c, bdbID, "my café"); n != 0 {
		t.Errorf("table exists after rejected CREATE: count = %d, want 0", n)
	}
	batches := parseToolJSON(t, callTool(t, c, "list_changes", map[string]any{
		"bdb_id": bdbID, "kind": "DDL",
	}))["batches"].([]any)
	if len(batches) != 0 {
		t.Errorf("change log has %d DDL batches after rejected CREATE, want 0", len(batches))
	}
}

// TestMCP_ExecuteSchema_InvalidColumnNameRollsBack verifies that column names — invisible at prepare time —
// are validated from the post-execute schema inside the transaction: the CREATE executes, the violation is
// detected, and the whole request rolls back for plain and virtual tables alike.
func TestMCP_ExecuteSchema_InvalidColumnNameRollsBack(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	tests := []struct {
		name  string
		sql   string
		table string
	}{
		{"plain table", `CREATE TABLE notes (id INTEGER PRIMARY KEY, "prix (€)" TEXT)`, "notes"},
		{"virtual table", `CREATE VIRTUAL TABLE ft USING fts5("bad col")`, "ft"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := callTool(t, c, "execute_schema", map[string]any{"bdb_id": bdbID, "sql": tt.sql})
			if !result.IsError {
				t.Fatal("expected error for invalid column name, got success")
			}
			if text := toolResultText(result); !strings.Contains(text, "column") ||
				!strings.Contains(text, "identifier") {
				t.Errorf("error text %q does not name the column identifier rule", text)
			}
			if n := schemaObjectCount(t, c, bdbID, tt.table); n != 0 {
				t.Errorf("table %q exists after rollback: count = %d, want 0", tt.table, n)
			}
		})
	}
}

// TestMCP_ExecuteSchema_InvalidSecondaryObjectNames verifies that index, trigger, and view names go through
// the same admission rule as table names.
func TestMCP_ExecuteSchema_InvalidSecondaryObjectNames(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	if r := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	}); r.IsError {
		t.Fatalf("seeding table: %v", r.Content)
	}

	tests := []struct {
		name    string
		sql     string
		objName string
	}{
		{"index", `CREATE INDEX "idx héro" ON items (name)`, "idx héro"},
		{"trigger", `CREATE TRIGGER "trg 1" AFTER INSERT ON items BEGIN SELECT 1; END`, "trg 1"},
		{"view", `CREATE VIEW "la vue" AS SELECT id FROM items`, "la vue"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := callTool(t, c, "execute_schema", map[string]any{"bdb_id": bdbID, "sql": tt.sql})
			if !result.IsError {
				t.Fatal("expected error for invalid object name, got success")
			}
			if text := toolResultText(result); !strings.Contains(text, "identifier") {
				t.Errorf("error text %q does not name the identifier rule", text)
			}
			if n := schemaObjectCount(t, c, bdbID, tt.objName); n != 0 {
				t.Errorf("object %q exists after rejected CREATE: count = %d, want 0", tt.objName, n)
			}
		})
	}
}

// TestMCP_ExecuteSchema_AlterNewNamesValidated verifies that the names an ALTER TABLE introduces (RENAME TO,
// ADD COLUMN, RENAME COLUMN targets) are validated while renames to conforming names keep working.
func TestMCP_ExecuteSchema_AlterNewNamesValidated(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	if r := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	}); r.IsError {
		t.Fatalf("seeding table: %v", r.Content)
	}

	rejected := []struct {
		name string
		sql  string
	}{
		{"add column", `ALTER TABLE items ADD COLUMN "unit (kg)" REAL`},
		{"rename column", `ALTER TABLE items RENAME COLUMN name TO "le nom"`},
		{"rename table", `ALTER TABLE items RENAME TO "les items"`},
	}
	for _, tt := range rejected {
		t.Run(tt.name, func(t *testing.T) {
			result := callTool(t, c, "execute_schema", map[string]any{"bdb_id": bdbID, "sql": tt.sql})
			if !result.IsError {
				t.Fatal("expected error for invalid ALTER target name, got success")
			}
			if text := toolResultText(result); !strings.Contains(text, "identifier") {
				t.Errorf("error text %q does not name the identifier rule", text)
			}
		})
	}

	// The table and its original column survived every rejected ALTER.
	if n := schemaObjectCount(t, c, bdbID, "items"); n == 0 {
		t.Error("items table lost after rejected ALTERs")
	}
	if n := columnCount(t, c, bdbID, "items", "name"); n != 1 {
		t.Errorf("column name count = %d, want 1", n)
	}
	if n := columnCount(t, c, bdbID, "items", "unit (kg)"); n != 0 {
		t.Errorf("rejected column was added: count = %d, want 0", n)
	}

	// Conforming ALTER targets still work.
	if r := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "ALTER TABLE items RENAME COLUMN name TO label",
	}); r.IsError {
		t.Fatalf("conforming RENAME COLUMN failed: %v", r.Content)
	}
	if r := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "ALTER TABLE items ADD COLUMN qty INTEGER",
	}); r.IsError {
		t.Fatalf("conforming ADD COLUMN failed: %v", r.Content)
	}
}

// TestMCP_ExecuteSchema_MultiStmt_InvalidNameRollsBackAll verifies that an invalid name in one statement of
// a multi-statement request reports its 1-indexed position and rolls back the earlier statements.
func TestMCP_ExecuteSchema_MultiStmt_InvalidNameRollsBackAll(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	result := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    `CREATE TABLE good_t (id INTEGER); CREATE TABLE "bad name" (id INTEGER);`,
	})
	if !result.IsError {
		t.Fatal("expected error for invalid name in statement 2, got success")
	}
	if text := toolResultText(result); !strings.Contains(text, "statement 2") {
		t.Errorf("error text %q does not carry the statement index", text)
	}
	if n := schemaObjectCount(t, c, bdbID, "good_t"); n != 0 {
		t.Errorf("good_t exists after rejected multi-statement request: count = %d, want 0", n)
	}
}

// TestMCP_ExecuteSchema_KeywordAndMaxLenNamesAllowed pins the permissive side of the rule: reserved SQL
// keywords and names at exactly the length cap are admitted.
func TestMCP_ExecuteSchema_KeywordAndMaxLenNamesAllowed(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	if r := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    `CREATE TABLE "order" (id INTEGER PRIMARY KEY, "group" TEXT)`,
	}); r.IsError {
		t.Fatalf("keyword names rejected: %v", r.Content)
	}
	if n := schemaObjectCount(t, c, bdbID, "order"); n != 1 {
		t.Errorf("keyword table count = %d, want 1", n)
	}

	// Keyword names survive the full write cycle: the generated triggers quote them, so DML and undo work.
	if r := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    `INSERT INTO "order" ("group") VALUES ('a')`,
	}); r.IsError {
		t.Fatalf("insert into keyword table: %v", r.Content)
	}
	if r := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    `UPDATE "order" SET "group" = 'b'`,
	}); r.IsError {
		t.Fatalf("update keyword column: %v", r.Content)
	}
	if r := callTool(t, c, "undo_last_change", map[string]any{"bdb_id": bdbID}); r.IsError {
		t.Fatalf("undo on keyword table: %v", r.Content)
	}
	val := parseToolJSON(t, callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID, "sql": `SELECT "group" FROM "order"`,
	}))["rows"].([]any)[0].([]any)[0].(map[string]any)["value"]
	if val != "a" {
		t.Errorf("keyword column after undo = %v, want a", val)
	}

	longName := strings.Repeat("a", 128)
	if r := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    fmt.Sprintf("CREATE TABLE %s (id INTEGER PRIMARY KEY)", longName),
	}); r.IsError {
		t.Fatalf("128-char name rejected: %v", r.Content)
	}
	if n := schemaObjectCount(t, c, bdbID, longName); n != 1 {
		t.Errorf("128-char table count = %d, want 1", n)
	}
}

// TestMCP_ExecuteSchema_WeirdIdentifierMatrix sweeps the rejection matrix for table names: every
// non-conforming shape is rejected without side effects — no schema object and no change-log batch. The
// leading-underscore case is denied by the reserved-prefix authorizer rule (a different error family), so
// only the other cases assert the identifier wording.
func TestMCP_ExecuteSchema_WeirdIdentifierMatrix(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	tests := []struct {
		name           string
		sql            string
		objName        string
		wantIdentifier bool
	}{
		{"space", `CREATE TABLE "my table" (id INTEGER)`, "my table", true},
		{"accent bare", `CREATE TABLE café (id INTEGER)`, "café", true},
		{"emoji bare", `CREATE TABLE notes🥖 (id INTEGER)`, "notes🥖", true},
		{"single quote", `CREATE TABLE "it's" (id INTEGER)`, "it's", true},
		{"double quote", `CREATE TABLE "a""b" (id INTEGER)`, `a"b`, true},
		{"129 chars", "CREATE TABLE " + strings.Repeat("a", 129) + " (id INTEGER)", strings.Repeat("a", 129), true},
		{"leading digit", `CREATE TABLE "1table" (id INTEGER)`, "1table", true},
		{"leading underscore", `CREATE TABLE "_private" (id INTEGER)`, "_private", false},
		{"emoji column", `CREATE TABLE stock (id INTEGER, "qté🥖" REAL)`, "stock", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := callTool(t, c, "execute_schema", map[string]any{"bdb_id": bdbID, "sql": tt.sql})
			if !result.IsError {
				t.Fatalf("expected rejection for %s, got success", tt.sql)
			}
			if text := toolResultText(result); tt.wantIdentifier && !strings.Contains(text, "identifier") {
				t.Errorf("error text %q does not name the identifier rule", text)
			}
			if n := schemaObjectCount(t, c, bdbID, tt.objName); n != 0 {
				t.Errorf("object %q exists after rejection: count = %d, want 0", tt.objName, n)
			}
		})
	}

	batches := parseToolJSON(t, callTool(t, c, "list_changes", map[string]any{
		"bdb_id": bdbID, "kind": "DDL",
	}))["batches"].([]any)
	if len(batches) != 0 {
		t.Errorf("change log has %d DDL batches after the rejection sweep, want 0", len(batches))
	}
}

// TestMCP_LegacyNonConformingColumn_Tolerated pins the tolerance guarantee for columns created before
// admission validation existed: with such a column planted directly (bypassing admission), describe still
// lists it, trigger regeneration and DML + undo work against it, and it can be renamed to a conforming name.
func TestMCP_LegacyNonConformingColumn_Tolerated(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	if r := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE pantry (id INTEGER PRIMARY KEY, name TEXT)",
	}); r.IsError {
		t.Fatalf("seeding table: %v", r.Content)
	}

	// Plants the legacy column and regenerates the per-table triggers so their bodies carry the
	// non-conforming name — the shape a pre-validation database would be in today.
	client := backingSQLiteClient(t)
	dbID := databaseIDForBdb(t, bdbID)
	ctx := context.Background()
	if _, err := client.ExecuteInternal(ctx, dbID, `ALTER TABLE pantry ADD COLUMN "prix (€)" REAL`, nil); err != nil {
		t.Fatalf("planting legacy column: %v", err)
	}
	if err := service.RegenChangeLogTriggersForAllUserTablesForTesting(ctx, client, dbID); err != nil {
		t.Fatalf("regen triggers over legacy column: %v", err)
	}

	// describe_table lists the legacy column by its exact name.
	desc := parseToolJSON(t, callTool(t, c, "describe_table", map[string]any{
		"bdb_id": bdbID, "table_name": "pantry",
	}))
	found := false
	for _, col := range desc["columns"].([]any) {
		if col.(map[string]any)["name"] == "prix (€)" {
			found = true
		}
	}
	if !found {
		t.Fatalf("describe_table does not list the legacy column: %v", desc["columns"])
	}

	// DML through the escaped triggers, then undo.
	if r := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    `INSERT INTO pantry (name, "prix (€)") VALUES ('baguette', 1.5)`,
	}); r.IsError {
		t.Fatalf("insert against legacy column: %v", r.Content)
	}
	if r := callTool(t, c, "undo_last_change", map[string]any{"bdb_id": bdbID}); r.IsError {
		t.Fatalf("undo against legacy column: %v", r.Content)
	}
	count := parseToolJSON(t, callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID, "sql": "SELECT COUNT(*) FROM pantry",
	}))["rows"].([]any)[0].([]any)[0].(map[string]any)["value"].(float64)
	if count != 0 {
		t.Errorf("rows after undo = %v, want 0", count)
	}

	// The escape hatch: renaming the legacy column to a conforming name is admitted.
	if r := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    `ALTER TABLE pantry RENAME COLUMN "prix (€)" TO prix`,
	}); r.IsError {
		t.Fatalf("renaming legacy column to conforming name: %v", r.Content)
	}
	if n := columnCount(t, c, bdbID, "pantry", "prix"); n != 1 {
		t.Errorf("renamed column count = %d, want 1", n)
	}
}
