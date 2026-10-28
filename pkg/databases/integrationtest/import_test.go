//go:build integration

package integrationtest

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// importServiceFor returns the import service of the stack the test set up last.
func importServiceFor(t *testing.T) *service.ImportService {
	t.Helper()
	v, ok := stackBackings.Load(t)
	if !ok {
		t.Fatal("no stack backing; call a setupRouter/setupStack helper first")
	}
	return v.(*stackTestBacking).importSvc
}

// TestImportDatabase checks the databases app's import of an export: the owner installs it as a new bdb's database,
// which then serves the exported rows and records its writes in the change log. It refuses a bdb that already holds a
// table, a file larger than a bdb's database may be, and an editor of the bdb, as only its owner may replace its
// database.
func TestImportDatabase(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	ctx := context.Background()
	ownerID := uuid.New()
	token := userToken(t, ownerID.String())
	sourceID := uuid.New().String()
	seedTable(t, router, sourceID, token, 3)
	rr := doExportRequest(t, router, sourceID, token)
	if rr.Code != http.StatusOK {
		t.Fatalf("export status = %d, body: %s", rr.Code, rr.Body.String())
	}
	export, passphrase := rr.Body.Bytes(), rr.Header().Get("X-Baguettedb-Export-Key")
	imports := importServiceFor(t)

	targetID := uuid.New()
	if err := imports.ImportDatabase(ctx, ownerID, targetID, bytes.NewReader(export), passphrase); err != nil {
		t.Fatalf("ImportDatabase into a new bdb: %v", err)
	}
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(targetID.String(), "query"), token,
		map[string]string{"sql": "SELECT count(*) AS n FROM t"})
	if rows, _ := resp["rows"].([]any); status != http.StatusOK || len(rows) != 1 {
		t.Fatalf("counting the imported rows = %d %v", status, resp)
	} else if cell, _ := rows[0].([]any)[0].(map[string]any); cell["value"] != float64(3) {
		t.Errorf("imported rows = %v, want 3", cell)
	}
	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(targetID.String(), "data"), token,
		map[string]string{"sql": "INSERT INTO t (v) VALUES ('after')"})
	if status != http.StatusOK {
		t.Fatalf("writing to the imported database = %d %v", status, resp)
	}

	sourceUUID := uuid.MustParse(sourceID)
	tooLarge := bytes.NewReader(make([]byte, 10*1024*1024+1))
	for _, tc := range []struct {
		name  string
		bdbID uuid.UUID
		src   *bytes.Reader
		want  error
	}{
		{"a bdb holding a table", sourceUUID, bytes.NewReader(export), domain.ErrImportTargetNotEmpty},
		{"a file past the size limit", uuid.New(), tooLarge, domain.ErrImportTooLarge},
	} {
		if err := imports.ImportDatabase(ctx, ownerID, tc.bdbID, tc.src, passphrase); !errors.Is(err, tc.want) {
			t.Errorf("ImportDatabase of %s = %v, want %v", tc.name, err, tc.want)
		}
	}

	setupRouterWithRole(t, authz.BdbRoleEditor)
	err := importServiceFor(t).ImportDatabase(ctx, uuid.New(), uuid.New(), bytes.NewReader(export), passphrase)
	if !errors.Is(err, domain.ErrAccessDenied) {
		t.Errorf("ImportDatabase as an editor = %v, want ErrAccessDenied", err)
	}
}
