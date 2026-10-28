//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// denyAllBdbsApp is a BdbsApp fake for a caller who is not a member of any bdb.
type denyAllBdbsApp struct{}

func (denyAllBdbsApp) CheckMemberPermission(
	_ context.Context, _, _ uuid.UUID, _ authz.DatabasePermission,
) (bool, error) {
	return false, nil
}

func (denyAllBdbsApp) GetBdbOwner(_ context.Context, _ uuid.UUID) (uuid.UUID, []authz.Role, error) {
	return uuid.Nil, []authz.Role{authz.RoleUser}, nil
}

func (denyAllBdbsApp) GetOwnedBdbIDs(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

// TestExecute_NonMemberDoesNotProvisionDatabase covers the order of the execute preflight: a non-member's request
// on a bdb without a database yet is refused before anything is provisioned — no metadata row, no file on disk —
// so nothing lands on the owner's storage quota, and the refusal says the caller is not a member. A member's request
// on the same stack then provisions normally, which keeps the "nothing appeared" assertions honest.
func TestExecute_NonMemberDoesNotProvisionDatabase(t *testing.T) {
	truncateAll(t)
	nonMember, _ := setupStackWithBdbsApp(t, denyAllBdbsApp{})
	dataDir := getOrCreateStackBacking(t, newDatabaseRepo()).dataDir
	bdbID := uuid.New().String()
	userID := uuid.New().String()

	for _, endpoint := range []string{"query", "data", "schema"} {
		status, resp := testutil.DoRequest(t, nonMember, http.MethodPost, dbPath(bdbID, endpoint), userToken(t, userID),
			map[string]string{"sql": "SELECT 1"})
		if status != http.StatusForbidden || resp["error"] != "access denied: you are not a member of this bdb" {
			t.Errorf("%s: status = %d, want %d and the non-member error, body: %v", endpoint, status,
				http.StatusForbidden, resp)
		}
	}
	result := callTool(t, startMCPClient(t, nonMember, userID), "execute_query",
		map[string]any{"bdb_id": bdbID, "sql": "SELECT 1"})
	if !result.IsError || !strings.Contains(toolResultText(result), "you are not a member of this bdb") {
		t.Errorf("execute_query as a non-member = %v, want the non-member error", result.Content)
	}
	assertDatabaseCount(t, bdbID, dataDir, 0)

	member := setupRouter(t)
	status, resp := testutil.DoRequest(t, member, http.MethodPost, dbPath(bdbID, "query"), userToken(t, userID),
		map[string]string{"sql": "SELECT 1"})
	if status != http.StatusOK {
		t.Fatalf("member query: status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
	assertDatabaseCount(t, bdbID, dataDir, 1)
}

// assertDatabaseCount checks how many databases exist for the bdb, both as metadata rows and as files in dataDir.
func assertDatabaseCount(t *testing.T, bdbID, dataDir string, want int) {
	t.Helper()
	var rows int
	if err := testDB.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM databases WHERE bdb_id = $1", bdbID).Scan(&rows); err != nil {
		t.Fatalf("counting databases rows: %v", err)
	}
	if rows != want {
		t.Errorf("databases rows for bdb = %d, want %d", rows, want)
	}
	files, err := filepath.Glob(filepath.Join(dataDir, "*.db"))
	if err != nil {
		t.Fatalf("listing database files: %v", err)
	}
	if len(files) != want {
		t.Errorf("database files under %s = %v, want %d", dataDir, files, want)
	}
}
