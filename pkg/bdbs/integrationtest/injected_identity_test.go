//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"testing"

	bdbsmodule "github.com/alexandremgo/baguettedb/pkg/bdbs"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// TestRouter_InjectedMiddlewareIdentity checks that a bdbs app set up with an injected middleware builds no JWT
// validator and serves its protected routes to the identity that middleware sets: the caller creates a bdb and lists
// it back as its owner. The Hydra URL points at a closed port, so building a validator would fail the setup.
func TestRouter_InjectedMiddlewareIdentity(t *testing.T) {
	truncateAll(t)
	userID := seedUser(t, "injected@test.com")

	cfg := config.Config{
		HydraPublicURL:     "http://127.0.0.1:1",
		MaxRequestBodySize: 1024 * 1024,
		InternalAPISecret:  testutil.InternalAPISecret,
	}
	testDB.Configure(&cfg)
	res, err := bdbsmodule.Setup(context.Background(), cfg, testLogger, &testAuthApp{}, bdbsmodule.SetupOptions{
		RequireToken:  testutil.IdentityMiddleware(userID, authz.RoleUser),
		QuotaResolver: defaultLimits(),
	})
	if err != nil {
		t.Fatalf("setting up the bdbs app: %v", err)
	}
	t.Cleanup(res.Cleanup)
	if res.TokenValidator != nil {
		t.Error("TokenValidator is set, want nil when a middleware is injected")
	}

	if status, resp := createBdb(t, res.Router, "", "Injected", ""); status != http.StatusCreated {
		t.Fatalf("POST /bdbs status = %d, want %d, body: %v", status, http.StatusCreated, resp)
	}
	status, list := testutil.DoRequestList(t, res.Router, http.MethodGet, "/api/bdbs/v1/bdbs", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /bdbs status = %d, want %d", status, http.StatusOK)
	}
	if len(list) != 1 || list[0]["name"] != "Injected" || list[0]["my_role"] != "owner" {
		t.Errorf("GET /bdbs = %v, want the one bdb, owned by the caller", list)
	}
}
