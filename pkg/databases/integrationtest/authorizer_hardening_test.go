//go:build integration

package integrationtest

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// TestSchema_TempObjectsAreRejected covers temporary objects: they live on the bdb's shared connection rather than
// in its file, so they are visible to every member, shadow same-named main tables for later statements, carry no
// change-log triggers and never appear in table listings. The schema endpoint must refuse them, and a second member
// must keep reading the main table.
func TestSchema_TempObjectsAreRejected(t *testing.T) {
	truncateAll(t)
	owner := setupRouterWithRole(t, authz.BdbRoleOwner)
	viewer := setupRouterWithRole(t, authz.BdbRoleViewer)
	bdbID := uuid.New().String()
	ownerToken := userToken(t, uuid.New().String())
	viewerToken := userToken(t, uuid.New().String())

	for _, step := range []struct{ endpoint, sql string }{
		{"schema", "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)"},
		{"data", "INSERT INTO t (v) VALUES ('main row')"},
	} {
		status, resp := testutil.DoRequest(t, owner, http.MethodPost, dbPath(bdbID, step.endpoint), ownerToken,
			map[string]string{"sql": step.sql})
		if status != http.StatusOK {
			t.Fatalf("%s %q: status = %d, want %d, body: %v", step.endpoint, step.sql, status, http.StatusOK, resp)
		}
	}

	for _, sql := range []string{
		"CREATE TEMP TABLE t (id INTEGER PRIMARY KEY, v TEXT)",
		"CREATE TEMP VIEW tv AS SELECT id, v FROM t",
		"CREATE TEMP TRIGGER tt AFTER INSERT ON t BEGIN SELECT 1; END",
	} {
		status, resp := testutil.DoRequest(t, owner, http.MethodPost, dbPath(bdbID, "schema"), ownerToken,
			map[string]string{"sql": sql})
		if status != http.StatusBadRequest {
			t.Errorf("schema %q: status = %d, want %d, body: %v", sql, status, http.StatusBadRequest, resp)
		}
	}

	if n := countViaQuery(t, viewer, viewerToken, bdbID, "SELECT count(*) FROM t"); n != 1 {
		t.Errorf("viewer sees %v rows in t, want 1 (a temporary table must not shadow the main one)", n)
	}
}

// TestQuery_ReindexIsRejected covers REINDEX: it rebuilds every index it names and prepares as read-only, so
// without the denial a viewer could run it through the query endpoint.
func TestQuery_ReindexIsRejected(t *testing.T) {
	truncateAll(t)
	owner := setupRouterWithRole(t, authz.BdbRoleOwner)
	viewer := setupRouterWithRole(t, authz.BdbRoleViewer)
	bdbID := uuid.New().String()
	ownerToken := userToken(t, uuid.New().String())
	viewerToken := userToken(t, uuid.New().String())

	for _, sql := range []string{
		"CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)",
		"CREATE INDEX t_v ON t (v)",
	} {
		status, resp := testutil.DoRequest(t, owner, http.MethodPost, dbPath(bdbID, "schema"), ownerToken,
			map[string]string{"sql": sql})
		if status != http.StatusOK {
			t.Fatalf("schema %q: status = %d, want %d, body: %v", sql, status, http.StatusOK, resp)
		}
	}

	for _, sql := range []string{"REINDEX", "REINDEX t"} {
		status, resp := testutil.DoRequest(t, viewer, http.MethodPost, dbPath(bdbID, "query"), viewerToken,
			map[string]string{"sql": sql})
		if status != http.StatusBadRequest {
			t.Errorf("query %q: status = %d, want %d, body: %v", sql, status, http.StatusBadRequest, resp)
		}
	}
}
