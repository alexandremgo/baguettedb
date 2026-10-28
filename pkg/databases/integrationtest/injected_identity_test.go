//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
	dbmodule "github.com/alexandremgo/baguettedb/pkg/databases"
)

// TestRouter_InjectedMiddlewareIdentity checks that a databases app set up with an injected middleware builds no
// JWT validator and serves its protected routes to the identity that middleware sets: the caller reads its own
// account storage, and an admin-only route refuses it for lacking the admin role. The Hydra URL points at a closed
// port, so building a validator would fail the setup. The config is loaded from the environment so every other
// setting takes its default.
func TestRouter_InjectedMiddlewareIdentity(t *testing.T) {
	dataDir := t.TempDir()
	for key, value := range map[string]string{
		"BAGUETTEDB_DATABASES_DATA_DIR":         dataDir,
		"BAGUETTEDB_DATABASES_KEK_FILE":         filepath.Join(dataDir, "kek"),
		"BAGUETTEDB_DATABASES_KEK_AUTOGENERATE": "true",
		"BAGUETTEDB_HYDRA_PUBLIC_URL":           "http://127.0.0.1:1",
		"BAGUETTEDB_INTERNAL_API_SECRET":        testutil.InternalAPISecret,
	} {
		t.Setenv(key, value)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	testDB.Configure(&cfg)
	res, err := dbmodule.Setup(context.Background(), cfg, testLogger, &allowAllBdbsApp{}, dbmodule.SetupOptions{
		RequireToken: testutil.IdentityMiddleware(uuid.NewString(), authz.RoleUser),
	})
	if err != nil {
		t.Fatalf("setting up the databases app: %v", err)
	}
	t.Cleanup(res.Cleanup)
	if res.TokenValidator != nil {
		t.Error("TokenValidator is set, want nil when a middleware is injected")
	}

	status, resp := testutil.DoRequest(t, res.Router, http.MethodGet, "/api/databases/v1/account/storage", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /account/storage status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
	adminStats := "/api/databases/v1/admin/bdbs/" + uuid.NewString() + "/database/stats"
	status, _ = testutil.DoRequest(t, res.Router, http.MethodGet, adminStats, "", nil)
	if status != http.StatusForbidden {
		t.Errorf("GET admin stats status = %d, want %d", status, http.StatusForbidden)
	}
}
