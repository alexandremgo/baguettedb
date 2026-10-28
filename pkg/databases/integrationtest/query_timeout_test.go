//go:build integration

package integrationtest

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// runawayCountSQL never finishes on its own: the recursive CTE has no bound, and COUNT keeps the result to a single
// row so only the time budget can end it.
const runawayCountSQL = "WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM r) SELECT COUNT(*) FROM r"

// TestExecuteQuery_TimeoutReturns504 covers the per-request time budget on the direct query path over REST and MCP,
// and on a formula run, whose interactive deadline is longer here so the request budget is what fires.
func TestExecuteQuery_TimeoutReturns504(t *testing.T) {
	truncateAll(t)
	router := setupRouterWithQueryTimeout(t, 200*time.Millisecond)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	start := time.Now()
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": runawayCountSQL})
	if status != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusGatewayTimeout, resp)
	}
	if msg, _ := resp["error"].(string); !strings.Contains(msg, "time limit") {
		t.Errorf("error = %q, want the time-limit message", msg)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("request took %v, want it stopped near the 200ms budget", elapsed)
	}

	result := callTool(t, startMCPClient(t, router, userID), "execute_query",
		map[string]any{"bdb_id": bdbID, "sql": runawayCountSQL})
	if !result.IsError || !strings.Contains(toolResultText(result), "time limit") {
		t.Errorf("execute_query = %v, want a time-limit error", result.Content)
	}

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "runaway",
		"description": "Never finishes.",
		"sql":         runawayCountSQL,
	})
	status, resp = testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "runaway"), token, map[string]any{})
	if status != http.StatusGatewayTimeout {
		t.Fatalf("formula run status = %d, want %d, body: %v", status, http.StatusGatewayTimeout, resp)
	}
	if msg, _ := resp["error"].(string); !strings.Contains(msg, "time limit") {
		t.Errorf("formula run error = %q, want the request time-limit message, not the interactive-run copy", msg)
	}
}
