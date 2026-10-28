//go:build integration

package integrationtest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// singleSlotLimits mirrors the default test limits with one in-flight SQL request per user.
func singleSlotLimits() quota.Resolver {
	return quota.Fixed(quota.Limits{
		MaxDBSizeBytes:          10 * 1024 * 1024,
		MaxStatementsPerRequest: 100,
		MaxRowsPerRequest:       1000,
		MaxConcurrentQueries:    1,
	})
}

// TestExecute_ConcurrencyGateReturns429 covers the per-user in-flight gate: while one request of a user runs, a
// second one is refused with 429 and Retry-After over REST, the same refusal reaches formula runs and MCP, and the
// slot is free again once the first request ends.
func TestExecute_ConcurrencyGateReturns429(t *testing.T) {
	truncateAll(t)
	router, _ := setupStackWithOptions(t,
		stackOptions{resolver: singleSlotLimits(), queryRequestTimeout: 3 * time.Second})
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)
	seedGroceriesN(t, router, bdbID, userID, 1)
	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "all-groceries",
		"description": "Everything.",
		"sql":         "SELECT rowid, * FROM groceries",
	})

	// Holds the user's only slot with a query that just the request budget can end.
	var wg sync.WaitGroup
	wg.Go(func() {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
			map[string]string{"sql": runawayCountSQL})
		if status != http.StatusGatewayTimeout {
			t.Errorf("slot holder: status = %d, want %d, body: %v", status, http.StatusGatewayTimeout, resp)
		}
	})
	time.Sleep(200 * time.Millisecond)

	rr := doRequestRaw(t, router, dbPath(bdbID, "query"), token, map[string]string{"sql": "SELECT 1"})
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("second query: status = %d, want %d, body: %s", rr.Code, http.StatusTooManyRequests, rr.Body.String())
	}
	if got := rr.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want \"1\"", got)
	}
	if !strings.Contains(rr.Body.String(), "too many queries") {
		t.Errorf("body = %s, want the concurrency message", rr.Body.String())
	}

	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "all-groceries"), token,
		map[string]any{})
	if status != http.StatusTooManyRequests {
		t.Errorf("formula run while at the cap: status = %d, want %d, body: %v", status, http.StatusTooManyRequests,
			resp)
	}

	result := callTool(t, startMCPClient(t, router, userID), "execute_query",
		map[string]any{"bdb_id": bdbID, "sql": "SELECT 1"})
	if !result.IsError || !strings.Contains(toolResultText(result), "too many queries") {
		t.Errorf("execute_query while at the cap = %v, want the concurrency error", result.Content)
	}

	wg.Wait()
	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT 1"})
	if status != http.StatusOK {
		t.Errorf("query after the holder ended: status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
}

// doRequestRaw posts a JSON body as the given user and returns the recorder, for assertions on response headers.
func doRequestRaw(t *testing.T, handler http.Handler, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshaling request body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}
