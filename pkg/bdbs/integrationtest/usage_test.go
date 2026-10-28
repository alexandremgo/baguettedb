//go:build integration

package integrationtest

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// TestCountActiveBdbs_ExcludesSoftDeleted verifies the usage count follows the API: a bdb deleted through the API
// leaves the count, and one restored through the API rejoins it.
func TestCountActiveBdbs_ExcludesSoftDeleted(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := seedUser(t, "owner@test.com")
	token := userToken(t, userID)
	bdbSvc := newBdbService(&testAuthApp{}, defaultLimits())
	ctx := context.Background()

	if n, err := bdbSvc.CountActiveBdbs(ctx); err != nil || n != 0 {
		t.Fatalf("count on an empty table = %d, %v; want 0, nil", n, err)
	}

	_, first := createBdb(t, router, token, "First", "")
	_, second := createBdb(t, router, token, "Second", "")
	if n, err := bdbSvc.CountActiveBdbs(ctx); err != nil || n != 2 {
		t.Fatalf("count after two creations = %d, %v; want 2, nil", n, err)
	}

	firstID := first["id"].(string)
	if status, resp := testutil.DoRequest(t, router, http.MethodDelete,
		"/api/bdbs/v1/bdbs/"+firstID, token, nil); status != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204, body: %v", status, resp)
	}
	if n, err := bdbSvc.CountActiveBdbs(ctx); err != nil || n != 1 {
		t.Fatalf("count after a soft delete = %d, %v; want 1, nil", n, err)
	}

	if status, resp := testutil.DoRequest(t, router, http.MethodPost,
		fmt.Sprintf("/api/bdbs/v1/bdbs/%s/restore", firstID), token, nil); status != http.StatusOK {
		t.Fatalf("restore status = %d, want 200, body: %v", status, resp)
	}
	if n, err := bdbSvc.CountActiveBdbs(ctx); err != nil || n != 2 {
		t.Fatalf("count after a restore = %d, %v; want 2, nil", n, err)
	}
	_ = second
}
