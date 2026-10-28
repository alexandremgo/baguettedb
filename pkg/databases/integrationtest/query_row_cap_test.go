//go:build integration

package integrationtest

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// rowCappedLimits mirrors the default test limits with a tiny result-row cap.
func rowCappedLimits(maxQueryResultRows int) quota.Resolver {
	return quota.Fixed(quota.Limits{
		MaxDBSizeBytes:          10 * 1024 * 1024,
		MaxStatementsPerRequest: 100,
		MaxRowsPerRequest:       1000,
		MaxQueryResultRows:      maxQueryResultRows,
	})
}

// TestExecuteQuery_RowCapReturns413 covers the per-request result-row cap on the direct query path over REST and MCP
// and on formula runs: a result past the cap fails loud with the LIMIT hint, a LIMIT under it succeeds, an empty
// result keeps its columns, a runaway generator stops at the cap, and a paginated view whose over-fetch stays within
// the cap keeps working.
func TestExecuteQuery_RowCapReturns413(t *testing.T) {
	truncateAll(t)
	router, _ := setupStackWithOptions(t, stackOptions{resolver: rowCappedLimits(3)})
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)
	seedGroceriesN(t, router, bdbID, userID, 4)

	query := func(sql string) (int, map[string]any) {
		return testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
			map[string]string{"sql": sql})
	}

	status, resp := query("SELECT * FROM groceries")
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("4 rows under a cap of 3: status = %d, want %d, body: %v", status, http.StatusRequestEntityTooLarge,
			resp)
	}
	if msg, _ := resp["error"].(string); !strings.Contains(msg, "add a LIMIT") ||
		!strings.Contains(msg, "limit 3 rows") {
		t.Errorf("error = %q, want the LIMIT hint and the cap value", msg)
	}

	status, resp = query("SELECT * FROM groceries LIMIT 3")
	if rows, _ := resp["rows"].([]any); status != http.StatusOK || len(rows) != 3 {
		t.Errorf("LIMIT 3: status = %d, rows = %d, want 200 with 3 rows (body: %v)", status, len(rows), resp)
	}

	status, resp = query("SELECT id, name FROM groceries WHERE 0")
	columns, _ := resp["columns"].([]any)
	if rows, _ := resp["rows"].([]any); status != http.StatusOK || len(columns) != 2 || len(rows) != 0 {
		t.Errorf("empty result: status = %d, columns = %d, rows = %d, want 200 with 2 columns and no rows",
			status, len(columns), len(rows))
	}

	start := time.Now()
	status, _ = query("WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM r) SELECT n FROM r")
	if status != http.StatusRequestEntityTooLarge {
		t.Errorf("runaway generator: status = %d, want %d", status, http.StatusRequestEntityTooLarge)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("runaway generator took %v, want it stopped at the cap", elapsed)
	}

	result := callTool(t, startMCPClient(t, router, userID), "execute_query",
		map[string]any{"bdb_id": bdbID, "sql": "SELECT * FROM groceries"})
	if !result.IsError || !strings.Contains(toolResultText(result), "add a LIMIT") {
		t.Errorf("execute_query = %v, want the row-cap error with the LIMIT hint", result.Content)
	}

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "all-groceries",
		"description": "Everything.",
		"sql":         "SELECT rowid, * FROM groceries ORDER BY rowid",
	})
	status, resp = testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "all-groceries"), token,
		map[string]any{})
	if status != http.StatusRequestEntityTooLarge {
		t.Errorf("non-paginated view over the cap: status = %d, want %d, body: %v", status,
			http.StatusRequestEntityTooLarge, resp)
	}

	createPaginatedView(t, router, bdbID, userID)
	if n, hasMore, _ := runPage(t, router, bdbID, userID, 0, 2); n != 2 || !hasMore {
		t.Errorf("paginated page under the cap: rows = %d, has_more = %v, want 2 rows and has_more", n, hasMore)
	}
}
