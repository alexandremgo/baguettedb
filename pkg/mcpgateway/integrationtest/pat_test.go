//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/instance"
)

// TestInstance_PersonalAccessToken checks that a personal access token authenticates its user on every route of an
// instance: the auth, bdbs and databases APIs, and the streamable MCP endpoint, where a bdb created with the token
// lists back as its user's own. An unknown token and a revoked one are refused.
//
// The instance authenticates with OAuth and personal tokens on, its validators checking JWTs against the suite's JWKS
// in place of Hydra's: the token is accepted alongside JWTs, not instead of them. The test sends no JWT.
func TestInstance_PersonalAccessToken(t *testing.T) {
	truncateAll(t)
	inst := newJWTInstance(t, instance.OAuth(true))
	ts := httptest.NewServer(inst.Router)
	t.Cleanup(ts.Close)

	ctx := context.Background()
	user, err := inst.Auth.UserSvc.CreateUser(ctx, "pat-"+uuid.NewString()+"@example.com", "Str0ng-Passw0rd!",
		"Pat", "Token")
	if err != nil {
		t.Fatalf("creating a user: %v", err)
	}
	pat, token, err := inst.Auth.TokenSvc.Create(ctx, user.ID, "test", nil)
	if err != nil {
		t.Fatalf("creating a token: %v", err)
	}

	get := func(path, bearer string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+path, nil)
		if err != nil {
			t.Fatalf("building GET %s: %v", path, err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("closing GET %s: %v", path, err)
		}
		return resp.StatusCode
	}
	for _, path := range []string{"/api/auth/v1/me", "/api/bdbs/v1/bdbs", "/api/databases/v1/account/storage"} {
		if status := get(path, pat); status != http.StatusOK {
			t.Errorf("GET %s with the token = %d, want 200", path, status)
		}
	}
	if status := get("/api/bdbs/v1/bdbs", "bdbp_unknown"); status != http.StatusUnauthorized {
		t.Errorf("GET /api/bdbs/v1/bdbs with an unknown token = %d, want 401", status)
	}

	c, err := mcpclient.NewStreamableHttpClient(ts.URL+"/mcp",
		transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + pat}))
	if err != nil {
		t.Fatalf("creating the MCP client: %v", err)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatalf("starting the MCP client: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("closing the MCP client: %v", err)
		}
	})
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{
		ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcp.Implementation{Name: "test-client", Version: "1.0.0"},
	}}); err != nil {
		t.Fatalf("initializing over MCP with the token: %v", err)
	}
	created := callTool(t, c, "create_bdb", map[string]any{"name": "Token notes"})
	if created.IsError {
		t.Fatalf("create_bdb: %v", created.Content)
	}
	bdbs := parseToolJSONArray(t, callTool(t, c, "list_bdbs", nil))
	if len(bdbs) != 1 || bdbs[0].(map[string]any)["role"] != "owner" {
		t.Errorf("list_bdbs = %v, want the bdb created with the token, owned by its user", bdbs)
	}

	if err := inst.Auth.TokenSvc.Revoke(ctx, user.ID, token.ID); err != nil {
		t.Fatalf("revoking the token: %v", err)
	}
	if status := get("/api/auth/v1/me", pat); status != http.StatusUnauthorized {
		t.Errorf("GET /api/auth/v1/me with the revoked token = %d, want 401", status)
	}
}

// TestInstance_PersonalAccessTokensOff checks that an instance authenticating with OAuth leaves personal access tokens
// out unless asked: a valid one is refused on the auth, bdbs and databases APIs, and the routes managing them are not
// mounted. A nil Authentication and OAuth(false) both behave so.
func TestInstance_PersonalAccessTokensOff(t *testing.T) {
	for name, auth := range map[string]instance.Authentication{"nil": nil, "OAuth(false)": instance.OAuth(false)} {
		t.Run(name, func(t *testing.T) {
			truncateAll(t)
			inst := newJWTInstance(t, auth)
			ts := httptest.NewServer(inst.Router)
			t.Cleanup(ts.Close)

			ctx := context.Background()
			user, err := inst.Auth.UserSvc.CreateUser(ctx, "pat-off-"+uuid.NewString()+"@example.com",
				"Str0ng-Passw0rd!", "Pat", "Off")
			if err != nil {
				t.Fatalf("creating a user: %v", err)
			}
			pat, _, err := inst.Auth.TokenSvc.Create(ctx, user.ID, "test", nil)
			if err != nil {
				t.Fatalf("creating a token: %v", err)
			}

			paths := map[string]int{
				"/api/auth/v1/me":                   http.StatusUnauthorized,
				"/api/bdbs/v1/bdbs":                 http.StatusUnauthorized,
				"/api/databases/v1/account/storage": http.StatusUnauthorized,
				"/api/auth/v1/personal-tokens":      http.StatusNotFound,
			}
			for path, want := range paths {
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+path, nil)
				if err != nil {
					t.Fatalf("building GET %s: %v", path, err)
				}
				req.Header.Set("Authorization", "Bearer "+pat)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatalf("GET %s: %v", path, err)
				}
				if err := resp.Body.Close(); err != nil {
					t.Fatalf("closing GET %s: %v", path, err)
				}
				if resp.StatusCode != want {
					t.Errorf("GET %s with a personal token = %d, want %d", path, resp.StatusCode, want)
				}
			}
		})
	}
}

// newJWTInstance composes every app on the suite's instance store with auth, an OAuth Authentication: JWTs checked
// against the suite's JWKS standing in for Hydra, a preset web client so no Hydra admin call is made, and bdb files in
// a temporary directory keyed by a KEK generated on the spot.
func newJWTInstance(t *testing.T, auth instance.Authentication) *instance.Instance {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	cfg.HydraPublicURL = testJWKS.JWKSURL()
	cfg.HydraIssuerURL = testJWKS.Issuer()
	cfg.WebClientID = "test-web-client"
	cfg.DatabasesDataDir = t.TempDir()
	cfg.DatabasesKEKFile = filepath.Join(t.TempDir(), "kek")
	cfg.DatabasesKEKAutogenerate = true
	backups := false
	cfg.DatabasesBackupEnabled = &backups
	testDB.Configure(&cfg)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	inst, err := instance.New(ctx, cfg, testLogger, instance.Options{Authentication: auth})
	if err != nil {
		t.Fatalf("instance.New: %v", err)
	}
	t.Cleanup(inst.Close)
	return inst
}
