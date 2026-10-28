//go:build integration

package integrationtest

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
)

// Multi-statement DML execution + rejection paths. Single-statement
// DML behavior is regressed by the existing tests (mutation log
// triggers, batch_id, etc.); this file exercises the N>1 behavior.

func TestMCP_MultiStmt_TwoInserts_ShareBatchID(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})

	result := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('a'); INSERT INTO items (name) VALUES ('b');",
	})
	if result.IsError {
		t.Fatalf("multi-stmt INSERT failed: %v", result.Content)
	}
	data := parseToolJSON(t, result)

	batchID, _ := data["batch_id"].(string)
	if batchID == "" {
		t.Fatalf("expected non-empty batch_id, got %v", data["batch_id"])
	}

	affected, _ := data["affected_rows"].([]any)
	if len(affected) != 2 {
		t.Fatalf("affected_rows = %d, want 2", len(affected))
	}
	for i, raw := range affected {
		row := raw.(map[string]any)
		if row["table_name"] != "items" || row["operation"] != "INSERT" {
			t.Errorf("affected_rows[%d] = %v, want items/INSERT", i, row)
		}
	}

	// Verify list_mutations groups both inserts under one batch.
	listResult := callTool(t, c, "list_changes", map[string]any{
		"bdb_id": bdbID,
		"kind":   "DML",
	})
	listData := parseToolJSON(t, listResult)
	batches := listData["batches"].([]any)
	if len(batches) != 1 {
		t.Fatalf("list_mutations: %d batches, want 1 (both stmts share batch_id)", len(batches))
	}
	batch := batches[0].(map[string]any)
	if batch["batch_id"] != batchID {
		t.Errorf("list_mutations batch_id = %v, want %s", batch["batch_id"], batchID)
	}
	entries := batch["entries"].([]any)
	if len(entries) != 2 {
		t.Errorf("batch entries = %d, want 2", len(entries))
	}
}

func TestMCP_MultiStmt_FKChain_LastInsertRowid(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE authors (id INTEGER PRIMARY KEY, name TEXT)",
	})
	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE books (id INTEGER PRIMARY KEY, title TEXT, author_id INTEGER)",
	})

	// Headline use case: parent + child tied via last_insert_rowid().
	result := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql": "INSERT INTO authors (name) VALUES ('Camus');" +
			"INSERT INTO books (title, author_id) VALUES ('La Peste', last_insert_rowid());",
	})
	if result.IsError {
		t.Fatalf("FK-chain insert failed: %v", result.Content)
	}

	// books.author_id must equal authors.id from the same request.
	queryResult := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT b.title, b.author_id, a.name FROM books b JOIN authors a ON a.id = b.author_id",
	})
	rows := parseToolJSON(t, queryResult)["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("join returned %d rows, want 1 (FK link via last_insert_rowid)", len(rows))
	}
	row := rows[0].([]any)
	title := row[0].(map[string]any)["value"]
	authorName := row[2].(map[string]any)["value"]
	if title != "La Peste" || authorName != "Camus" {
		t.Errorf("row = %v, want title=La Peste author=Camus", row)
	}
}

func TestMCP_MultiStmt_AnyFailureRollsBackAll(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT NOT NULL)",
	})

	// Statement 2 violates NOT NULL. The whole request must roll back.
	result := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('ok'); INSERT INTO items (name) VALUES (NULL);",
	})
	if !result.IsError {
		t.Fatal("expected error from NOT NULL violation in stmt 2, got success")
	}

	// Neither row should be visible.
	queryResult := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT COUNT(*) FROM items",
	})
	rows := parseToolJSON(t, queryResult)["rows"].([]any)
	count := rows[0].([]any)[0].(map[string]any)["value"].(float64)
	if count != 0 {
		t.Errorf("rows after rollback = %v, want 0", count)
	}

	// No undo entry should have been pushed (the txn rolled back).
	listResult := callTool(t, c, "list_changes", map[string]any{"bdb_id": bdbID, "kind": "DML"})
	batches := parseToolJSON(t, listResult)["batches"].([]any)
	if len(batches) != 0 {
		t.Errorf("list_mutations after rollback: %d batches, want 0", len(batches))
	}
}

func TestMCP_MultiStmt_DDL_TwoCreatesIndependent_BothApplied(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	// Independent CREATE TABLEs (no cross-stmt schema deps) parse and
	// apply atomically. CREATE TABLE + CREATE INDEX on the same new
	// table is NOT supported in one request — parse-time name
	// resolution requires referenced tables to already exist; submit
	// the dependent stmt as a separate request.
	result := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE a (id INTEGER PRIMARY KEY); CREATE TABLE b (id INTEGER PRIMARY KEY);",
	})
	if result.IsError {
		t.Fatalf("multi-stmt DDL failed: %v", result.Content)
	}

	queryResult := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT name FROM sqlite_master WHERE type='table' AND name IN ('a','b') ORDER BY name",
	})
	rows := parseToolJSON(t, queryResult)["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("got %d schema rows, want 2 (a + b)", len(rows))
	}
}

func TestMCP_MultiStmt_DDL_FailureRollsBack(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	// Second CREATE TABLE duplicates the first → fails. The first must
	// also be rolled back atomically.
	result := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE foo (id INTEGER); CREATE TABLE foo (id INTEGER);",
	})
	if !result.IsError {
		t.Fatal("expected error for duplicate CREATE TABLE in multi-stmt DDL, got success")
	}

	queryResult := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='foo'",
	})
	count := parseToolJSON(t, queryResult)["rows"].([]any)[0].([]any)[0].(map[string]any)["value"].(float64)
	if count != 0 {
		t.Errorf("foo table count after rollback = %v, want 0", count)
	}
}

func TestMCP_MultiStmt_DDL_TwoAltersSameTable_TriggersReflectFinalSchema(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})

	// Two ALTERs on the same table — deferred regen runs once at end-of-tx.
	result := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "ALTER TABLE items ADD COLUMN price INTEGER; ALTER TABLE items ADD COLUMN qty INTEGER;",
	})
	if result.IsError {
		t.Fatalf("multi-ALTER on same table failed: %v", result.Content)
	}

	// A subsequent INSERT must log all four columns (id/name/price/qty)
	// — proves the deferred regen produced triggers from the final schema.
	callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name, price, qty) VALUES ('milk', 3, 2)",
	})
	listResult := callTool(t, c, "list_changes", map[string]any{"bdb_id": bdbID, "kind": "DML"})
	batches := parseToolJSON(t, listResult)["batches"].([]any)
	if len(batches) != 1 {
		t.Fatalf("expected 1 mutation batch, got %d", len(batches))
	}
	entry := batches[0].(map[string]any)["entries"].([]any)[0].(map[string]any)
	newValues := entry["new_values"].(map[string]any)
	for _, col := range []string{"id", "name", "price", "qty"} {
		if _, ok := newValues[col]; !ok {
			t.Errorf("new_values missing %q (deferred regen should have included all post-ALTER columns); got %v", col,
				newValues)
		}
	}
}

func TestMCP_MultiStmt_DDL_AltersOnDifferentTables(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE a (id INTEGER PRIMARY KEY); CREATE TABLE b (id INTEGER PRIMARY KEY);",
	})

	result := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "ALTER TABLE a ADD COLUMN x INTEGER; ALTER TABLE b ADD COLUMN y INTEGER;",
	})
	if result.IsError {
		t.Fatalf("multi-ALTER across tables failed: %v", result.Content)
	}

	// Both tables should have triggers reflecting their new columns.
	for _, ins := range []struct{ table, col string }{{"a", "x"}, {"b", "y"}} {
		callTool(t, c, "execute_data", map[string]any{
			"bdb_id": bdbID,
			"sql":    fmt.Sprintf("INSERT INTO %s (%s) VALUES (1)", ins.table, ins.col),
		})
	}
	listResult := callTool(t, c, "list_changes", map[string]any{"bdb_id": bdbID, "kind": "DML"})
	batches := parseToolJSON(t, listResult)["batches"].([]any)
	if len(batches) != 2 {
		t.Fatalf("expected 2 mutation batches (one per table), got %d", len(batches))
	}
}

func TestMCP_MultiStmt_DDL_CrossStmtSchemaDep_FailsAtParse(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	// Documented limitation: every statement is parsed against the
	// CURRENT schema, so a CREATE INDEX referencing a table the
	// previous stmt would create fails at the classifier walk before
	// any DDL executes. Surfaces as "no such table: <name>" prefixed
	// with the 1-indexed statement number that failed to parse.
	// Users submit the dependent stmt in a separate request.
	result := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql": "CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT); " +
			"CREATE INDEX idx_notes_body ON notes (body);",
	})
	if !result.IsError {
		t.Fatal("expected error for CREATE TABLE + CREATE INDEX in one request, got success")
	}
	msg := contentString(result)
	if !strings.Contains(msg, "statement 2") {
		t.Errorf("error should mention 'statement 2', got: %s", msg)
	}
	if !strings.Contains(msg, "no such table") || !strings.Contains(msg, "notes") {
		t.Errorf("error should surface SQLite's 'no such table: notes', got: %s", msg)
	}

	// Neither object should exist (the parse-time reject is before any DDL execute).
	queryResult := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT COUNT(*) FROM sqlite_master WHERE name IN ('notes','idx_notes_body')",
	})
	count := parseToolJSON(t, queryResult)["rows"].([]any)[0].([]any)[0].(map[string]any)["value"].(float64)
	if count != 0 {
		t.Errorf("schema objects after cross-stmt parse reject = %v, want 0", count)
	}
}

func TestMCP_MultiStmt_DDL_RenameTo_RejectedInMultiStmt(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE a (id INTEGER PRIMARY KEY); CREATE TABLE c (id INTEGER PRIMARY KEY);",
	})

	// Both stmts reference tables that exist at parse time; the rejection
	// fires from the AlterTableInfo subtype check, not from a parse error.
	result := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "ALTER TABLE a RENAME TO b; ALTER TABLE c ADD COLUMN x INTEGER;",
	})
	if !result.IsError {
		t.Fatal("expected error for RENAME TO in multi-statement DDL, got success")
	}
	msg := contentString(result)
	if !strings.Contains(msg, "RENAME TO") {
		t.Errorf("error should mention RENAME TO, got: %s", msg)
	}
}

func TestMCP_MultiStmt_BoundArgs_RejectedWithMultiStmt(t *testing.T) {
	// Bound args have no per-statement positional meaning across N>1.
	// Service must reject the combination explicitly.
	// Note: the MCP execute_mutation tool doesn't expose args, so this
	// is exercised via the REST handler in the rest_test suite when
	// applicable. Covered here only as a service-layer guard.
	t.Skip("MCP tools don't surface bound args; guard exercised via REST/unit layers")
}

func TestMCP_MultiStmt_StatementCountCap_Rejected(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})

	// 101 trivial INSERTs (default cap is 100). Rejected at parse time;
	// no rows persist.
	var sb strings.Builder
	for i := 0; i < 101; i++ {
		sb.WriteString("INSERT INTO items (name) VALUES ('x');")
	}
	result := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    sb.String(),
	})
	if !result.IsError {
		t.Fatal("expected error for 101-statement request, got success")
	}
	msg := contentString(result)
	if !strings.Contains(msg, "exceeded max statements (100)") || !strings.Contains(msg, "101") {
		t.Errorf("error should mention cap and request size, got: %s", msg)
	}

	// Verify nothing was persisted.
	queryResult := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT COUNT(*) FROM items",
	})
	count := parseToolJSON(t, queryResult)["rows"].([]any)[0].([]any)[0].(map[string]any)["value"].(float64)
	if count != 0 {
		t.Errorf("rows after parse-time reject = %v, want 0", count)
	}
}

func TestMCP_MultiStmt_RowCountCap_AcrossStatements(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE bulk (id INTEGER PRIMARY KEY, n INTEGER)",
	})

	// 4-stmt request producing 200 / 300 / 600 / 50 rows
	// → cumulative 200 / 500 / 1100 / 1150 → fails at statement 3.
	bulkInsert := func(n int) string {
		return "INSERT INTO bulk (n) " +
			"WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM seq WHERE x < " + strconv.Itoa(n) + ") " +
			"SELECT x FROM seq;"
	}
	sql := bulkInsert(200) + bulkInsert(300) + bulkInsert(600) + bulkInsert(50)

	result := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    sql,
	})
	if !result.IsError {
		t.Fatal("expected ErrTooManyRows, got success")
	}
	msg := contentString(result)
	if !strings.Contains(msg, "exceeded max rows (1000)") || !strings.Contains(msg, "at statement 3") {
		t.Errorf("error should be 'exceeded max rows (1000) at statement 3', got: %s", msg)
	}

	// Verify rollback: 0 rows in bulk, no batch on undo stack.
	queryResult := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT COUNT(*) FROM bulk",
	})
	count := parseToolJSON(t, queryResult)["rows"].([]any)[0].([]any)[0].(map[string]any)["value"].(float64)
	if count != 0 {
		t.Errorf("rows after row-cap rollback = %v, want 0", count)
	}
	listResult := callTool(t, c, "list_changes", map[string]any{"bdb_id": bdbID, "kind": "DML"})
	batches := parseToolJSON(t, listResult)["batches"].([]any)
	if len(batches) != 0 {
		t.Errorf("list_mutations after row-cap rollback: %d batches, want 0", len(batches))
	}
}

func TestMCP_MultiStmt_RowCountCap_SingleStatementOverCap(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE bulk (id INTEGER PRIMARY KEY, n INTEGER)",
	})

	// One INSERT producing 1001 rows (cap=1000) — fails at statement 1.
	result := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql": "INSERT INTO bulk (n) " +
			"WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM seq WHERE x < 1001) " +
			"SELECT x FROM seq",
	})
	if !result.IsError {
		t.Fatal("expected ErrTooManyRows, got success")
	}
	msg := contentString(result)
	if !strings.Contains(msg, "at statement 1") {
		t.Errorf("error should mention 'at statement 1', got: %s", msg)
	}

	queryResult := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT COUNT(*) FROM bulk",
	})
	count := parseToolJSON(t, queryResult)["rows"].([]any)[0].([]any)[0].(map[string]any)["value"].(float64)
	if count != 0 {
		t.Errorf("rows after single-stmt over-cap rollback = %v, want 0", count)
	}
}

func TestMCP_MultiStmt_Undo_BulkInsert500_RestoresPriorState(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE bulk (id INTEGER PRIMARY KEY, n INTEGER)",
	})

	// One INSERT producing 500 rows — well under the 1000-row cap, so
	// it commits, gets one batch_id, and one undo entry. Undo should
	// remove all 500 in one shot via the collapsed
	// DELETE … WHERE rowid IN (…) revert SQL.
	if r := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql": "INSERT INTO bulk (n) " +
			"WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM seq WHERE x < 500) " +
			"SELECT x FROM seq",
	}); r.IsError {
		t.Fatalf("bulk INSERT failed: %v", r.Content)
	}

	// Sanity-check the INSERT actually wrote the 500 expected n values
	// (count + sum of 1..500) — guards against the test silently
	// passing on a no-op INSERT.
	q := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT COUNT(*), SUM(n), MIN(n), MAX(n) FROM bulk",
	})
	row := parseToolJSON(t, q)["rows"].([]any)[0].([]any)
	count := row[0].(map[string]any)["value"].(float64)
	sum := row[1].(map[string]any)["value"].(float64)
	minN := row[2].(map[string]any)["value"].(float64)
	maxN := row[3].(map[string]any)["value"].(float64)
	if count != 500 || sum != 500*501/2 || minN != 1 || maxN != 500 {
		t.Fatalf("post-INSERT state: count=%v sum=%v min=%v max=%v; want 500 / %d / 1 / 500", count, sum, minN, maxN,
			500*501/2)
	}

	if r := callTool(t, c, "undo_last_change", map[string]any{"bdb_id": bdbID}); r.IsError {
		t.Fatalf("undo failed: %v", r.Content)
	}

	q = callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT COUNT(*) FROM bulk",
	})
	postCount := parseToolJSON(t, q)["rows"].([]any)[0].([]any)[0].(map[string]any)["value"].(float64)
	if postCount != 0 {
		t.Errorf("rows after undo of 500-row bulk INSERT = %v, want 0", postCount)
	}
}

func TestMCP_MultiStmt_Undo_HeterogeneousBatch_Works(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE a (id INTEGER PRIMARY KEY, name TEXT)",
	})
	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE b (id INTEGER PRIMARY KEY, name TEXT)",
	})
	// Seed an existing row in a so the multi-stmt request can mix INSERT + UPDATE.
	callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO a (id, name) VALUES (1, 'original')",
	})

	// Multi-table, mixed-op request — falls back to per-row revert SQL.
	if r := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql": "INSERT INTO a (id, name) VALUES (2, 'inserted-a');" +
			"INSERT INTO b (id, name) VALUES (1, 'inserted-b');" +
			"UPDATE a SET name = 'updated' WHERE id = 1;",
	}); r.IsError {
		t.Fatalf("heterogeneous batch failed: %v", r.Content)
	}

	// Sanity-check the mutations applied (so the undo is reverting
	// real changes, not a no-op).
	preQ := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql": "SELECT (SELECT name FROM a WHERE id=1), (SELECT name FROM a WHERE id=2), " +
			"(SELECT name FROM b WHERE id=1)",
	})
	preRow := parseToolJSON(t, preQ)["rows"].([]any)[0].([]any)
	if v := preRow[0].(map[string]any)["value"]; v != "updated" {
		t.Fatalf("pre-undo a.id=1 name = %v, want 'updated'", v)
	}
	if v := preRow[1].(map[string]any)["value"]; v != "inserted-a" {
		t.Fatalf("pre-undo a.id=2 name = %v, want 'inserted-a'", v)
	}
	if v := preRow[2].(map[string]any)["value"]; v != "inserted-b" {
		t.Fatalf("pre-undo b.id=1 name = %v, want 'inserted-b'", v)
	}

	if r := callTool(t, c, "undo_last_change", map[string]any{"bdb_id": bdbID}); r.IsError {
		t.Fatalf("undo failed: %v", r.Content)
	}

	// After undo: a.id=1 back to 'original'; a.id=2 gone; b empty.
	q := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql": "SELECT (SELECT name FROM a WHERE id=1), (SELECT COUNT(*) FROM a WHERE id=2), (SELECT COUNT(*) " +
			"FROM b)",
	})
	row := parseToolJSON(t, q)["rows"].([]any)[0].([]any)
	originalName := row[0].(map[string]any)["value"]
	aTwoCount := row[1].(map[string]any)["value"].(float64)
	bCount := row[2].(map[string]any)["value"].(float64)
	if originalName != "original" {
		t.Errorf("a.id=1 name after undo = %v, want 'original'", originalName)
	}
	if aTwoCount != 0 {
		t.Errorf("a.id=2 count after undo = %v, want 0", aTwoCount)
	}
	if bCount != 0 {
		t.Errorf("b count after undo = %v, want 0", bCount)
	}
}

// contentString flattens an MCP tool result's content blocks into a
// single string for substring matching.
func contentString(result *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range result.Content {
		if tc, ok := mcp.AsTextContent(c); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}
