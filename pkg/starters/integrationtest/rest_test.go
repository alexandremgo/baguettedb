//go:build integration

package integrationtest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/embedded"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/intraprocessbdbs"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/intraprocessdatabases"
	startersapi "github.com/alexandremgo/baguettedb/pkg/starters/api"
	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
	startersservice "github.com/alexandremgo/baguettedb/pkg/starters/service"
)

// setupRESTServer mounts the starters REST routes (backed by the full real stack through the intraprocess
// adapters) behind the real JWT middleware, and returns the test server.
func setupRESTServer(t *testing.T, sources ...startersservice.StarterSource) (*httptest.Server, engineDeps) {
	t.Helper()
	stacks := setupStacks(t)

	tv, err := authhttp.NewTokenValidator(
		t.Context(), testJWKS.JWKSURL(), testJWKS.Issuer(), "", testLogger)
	if err != nil {
		t.Fatalf("creating token validator: %v", err)
	}
	t.Cleanup(tv.Close)

	starterSvc := startersservice.NewStarterService(
		startersservice.NewRegistry(sources...),
		intraprocessbdbs.New(stacks.bdbSvc),
		intraprocessdatabases.New(stacks.querySvc, stacks.formulaSvc, stacks.skillSvc, stacks.dbSvc),
		testLogger,
	)

	router := chi.NewRouter()
	startersapi.RegisterRoutes(
		router, startersapi.NewHandler(starterSvc, testLogger), tv.RequireToken(), 1024*1024)

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server, stacks.deps
}

// doJSON sends a JSON request with the given bearer token and decodes the response body into out when non-nil.
func doJSON(t *testing.T, method, url, token string, body, out any) int {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshaling body: %v", err)
		}
		reader = bytes.NewReader(payload)
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, url, reader)
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sending request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if out != nil && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
	}
	return resp.StatusCode
}

func bearerToken(t *testing.T, userID uuid.UUID) string {
	t.Helper()
	token, err := testJWKS.BuildToken(userID.String(), []string{"user"})
	if err != nil {
		t.Fatalf("building token: %v", err)
	}
	return token
}

// TestStartersCatalogREST verifies GET /starters returns the catalog with the manifest-known preview (formula
// and skill names + descriptions) and that the route sits behind authentication.
func TestStartersCatalogREST(t *testing.T) {
	server, _ := setupRESTServer(t, &testSource{manifests: []domain.StarterManifest{tasksManifest()}})
	url := server.URL + "/api/bdbs/v1/starters"

	if status := doJSON(t, http.MethodGet, url, "", nil, nil); status != http.StatusUnauthorized {
		t.Errorf("GET /starters without token = %d, want 401", status)
	}

	var catalog []struct {
		Key      string   `json:"key"`
		Version  int      `json:"version"`
		Name     string   `json:"name"`
		Tags     []string `json:"tags"`
		Formulas []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"formulas"`
		Skills []struct {
			Name string `json:"name"`
		} `json:"skills"`
	}
	status := doJSON(t, http.MethodGet, url, bearerToken(t, uuid.New()), nil, &catalog)
	if status != http.StatusOK {
		t.Fatalf("GET /starters = %d, want 200", status)
	}
	if len(catalog) != 1 || catalog[0].Key != "tasks" || catalog[0].Version != 2 {
		t.Fatalf("catalog = %+v, want the tasks starter at version 2", catalog)
	}
	if len(catalog[0].Formulas) != 4 || catalog[0].Formulas[0].Name != "open-tasks" {
		t.Errorf("preview formulas = %+v, want 4 entries starting with open-tasks", catalog[0].Formulas)
	}
	if len(catalog[0].Skills) != 1 || catalog[0].Skills[0].Name != "tasks-orientation" {
		t.Errorf("preview skills = %+v, want [tasks-orientation]", catalog[0].Skills)
	}
}

// TestCreateFromStarterREST drives POST /bdbs/from-starter through the real HTTP surface: 201 with the same
// response shape as POST /bdbs on success, 409 for a name another of the caller's bdbs has, whatever the case, 404
// for an unknown key, 400 for a missing starter_key, and 500 with a clean retryable message when seeding fails (the
// bdb is cleaned up).
func TestCreateFromStarterREST(t *testing.T) {
	broken := tasksManifest()
	broken.Key = "broken-skill"
	broken.Skills[0].Targets = []string{"no-such-table"}

	server, deps := setupRESTServer(t,
		&testSource{manifests: []domain.StarterManifest{tasksManifest(), broken}})
	url := server.URL + "/api/bdbs/v1/bdbs/from-starter"
	userID := uuid.New()
	token := bearerToken(t, userID)

	var created struct {
		ID     uuid.UUID `json:"id"`
		Name   string    `json:"name"`
		MyRole string    `json:"my_role"`
	}
	status := doJSON(t, http.MethodPost, url, token,
		map[string]string{"starter_key": "tasks", "name": "My week"}, &created)
	if status != http.StatusCreated {
		t.Fatalf("POST from-starter = %d, want 201", status)
	}
	if created.Name != "My week" || created.MyRole != "owner" {
		t.Errorf("created bdb = %+v, want name My week owned by the caller", created)
	}
	if bdb, err := deps.bdbRepo.FindByID(t.Context(), created.ID); err != nil || bdb.StarterKey == nil {
		t.Errorf("bdb %v provenance missing (err = %v)", created.ID, err)
	}

	if status := doJSON(t, http.MethodPost, url, token,
		map[string]string{"starter_key": "tasks", "name": "my WEEK"}, nil); status != http.StatusConflict {
		t.Errorf("POST from-starter with a taken name = %d, want 409", status)
	}
	if status := doJSON(t, http.MethodPost, url, token,
		map[string]string{"starter_key": "ghost"}, nil); status != http.StatusNotFound {
		t.Errorf("POST from-starter with unknown key = %d, want 404", status)
	}
	if status := doJSON(t, http.MethodPost, url, token,
		map[string]string{"name": "No key"}, nil); status != http.StatusBadRequest {
		t.Errorf("POST from-starter without starter_key = %d, want 400", status)
	}
	if status := doJSON(t, http.MethodPost, url, token,
		map[string]string{"starter_key": "broken-skill"}, nil); status != http.StatusInternalServerError {
		t.Errorf("POST from-starter with failing seed = %d, want 500", status)
	}
}

// TestBuiltinCatalogREST lists the real embedded catalog over REST — the gallery's actual payload.
func TestBuiltinCatalogREST(t *testing.T) {
	source, err := embedded.NewSource()
	if err != nil {
		t.Fatalf("NewSource() error = %v", err)
	}
	server, _ := setupRESTServer(t, source)

	var catalog []struct {
		Key  string `json:"key"`
		Icon string `json:"icon"`
	}
	status := doJSON(t, http.MethodGet, server.URL+"/api/bdbs/v1/starters", bearerToken(t, uuid.New()), nil, &catalog)
	if status != http.StatusOK {
		t.Fatalf("GET /starters = %d, want 200", status)
	}
	if len(catalog) == 0 {
		t.Fatal("catalog is empty, want the built-in starters")
	}
	for _, entry := range catalog {
		if entry.Key == "" {
			t.Errorf("catalog entry with empty key: %+v", entry)
		}
	}
}
