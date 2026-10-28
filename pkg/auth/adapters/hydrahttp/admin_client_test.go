package hydrahttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
)

// fakeClientsAPI is an in-memory stand-in for Hydra's /admin/clients endpoints: get by id, create, and full replace.
type fakeClientsAPI struct {
	clients map[string]oauth2ClientDTO
	// lastBody is the JSON body of the most recent create or update request.
	lastBody oauth2ClientDTO
}

func (f *fakeClientsAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path != "/admin/clients":
		id := r.URL.Path[len("/admin/clients/"):]
		c, ok := f.clients[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"Unable to locate the resource"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(c)
	case r.Method == http.MethodPost && r.URL.Path == "/admin/clients":
		var c oauth2ClientDTO
		_ = json.NewDecoder(r.Body).Decode(&c)
		f.lastBody = c
		if c.ClientID == "" {
			c.ClientID = "generated-id"
		}
		if _, exists := f.clients[c.ClientID]; exists {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"Unable to insert or update resource"}`))
			return
		}
		f.clients[c.ClientID] = c
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(c)
	case r.Method == http.MethodPut:
		id := r.URL.Path[len("/admin/clients/"):]
		if _, ok := f.clients[id]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var c oauth2ClientDTO
		_ = json.NewDecoder(r.Body).Decode(&c)
		f.lastBody = c
		f.clients[id] = c
		_ = json.NewEncoder(w).Encode(c)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// newFakeAdmin starts an httptest server around a fresh fakeClientsAPI and returns an AdminClient pointed at it.
func newFakeAdmin(t *testing.T) (*AdminClient, *fakeClientsAPI) {
	t.Helper()
	fake := &fakeClientsAPI{clients: map[string]oauth2ClientDTO{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return NewAdminClient(srv.URL), fake
}

// webClient returns the registration the platform's web frontend uses, as a fixture for the CRUD tests.
func webClient() domain.OAuth2Client {
	return domain.OAuth2Client{
		ID:                      "baguettedb-web",
		Name:                    "baguettedb-web",
		RedirectURIs:            []string{"http://localhost:5173/callback"},
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		Scope:                   "openid offline",
		TokenEndpointAuthMethod: "none",
		SkipConsent:             true,
	}
}

// TestGetOAuth2Client_MissingMapsToNotFound checks that a 404 from the admin API surfaces as the domain sentinel
// rather than a generic transport error.
func TestGetOAuth2Client_MissingMapsToNotFound(t *testing.T) {
	admin, _ := newFakeAdmin(t)
	_, err := admin.GetOAuth2Client(context.Background(), "nope")
	if !errors.Is(err, domain.ErrOAuth2ClientNotFound) {
		t.Fatalf("expected ErrOAuth2ClientNotFound, got %v", err)
	}
}

// TestCreateOAuth2Client_SendsRegistrationAndReturnsStoredClient checks that the registration is sent verbatim
// (fixed id, scope, grant types), that the stored client comes back, that a second create on the same id maps to
// ErrOAuth2ClientExists, and that the created client can be read back.
func TestCreateOAuth2Client_SendsRegistrationAndReturnsStoredClient(t *testing.T) {
	admin, fake := newFakeAdmin(t)
	want := webClient()

	got, err := admin.CreateOAuth2Client(context.Background(), want)
	if err != nil {
		t.Fatalf("CreateOAuth2Client: %v", err)
	}
	if got.ID != want.ID || got.Name != want.Name || !got.SkipConsent || got.TokenEndpointAuthMethod != "none" {
		t.Fatalf("stored client = %+v, want %+v", got, want)
	}
	if fake.lastBody.ClientID != "baguettedb-web" || fake.lastBody.Scope != "openid offline" {
		t.Fatalf("request body = %+v, want the registration fields verbatim", fake.lastBody)
	}
	if len(fake.lastBody.GrantTypes) != 2 || fake.lastBody.GrantTypes[1] != "refresh_token" {
		t.Fatalf("grant_types = %v, want authorization_code + refresh_token", fake.lastBody.GrantTypes)
	}

	if _, err := admin.CreateOAuth2Client(context.Background(), want); !errors.Is(err, domain.ErrOAuth2ClientExists) {
		t.Fatalf("second create: expected ErrOAuth2ClientExists, got %v", err)
	}

	fetched, err := admin.GetOAuth2Client(context.Background(), want.ID)
	if err != nil {
		t.Fatalf("GetOAuth2Client after create: %v", err)
	}
	if !fetched.HasRedirectURI("http://localhost:5173/callback") {
		t.Fatalf("fetched redirect URIs = %v", fetched.RedirectURIs)
	}
}

// TestUpdateOAuth2Client_ReplacesRegistration checks that an update fully replaces the stored registration under
// the client id and that updating an unknown id maps to ErrOAuth2ClientNotFound.
func TestUpdateOAuth2Client_ReplacesRegistration(t *testing.T) {
	admin, fake := newFakeAdmin(t)
	c := webClient()
	if _, err := admin.CreateOAuth2Client(context.Background(), c); err != nil {
		t.Fatalf("CreateOAuth2Client: %v", err)
	}

	c.RedirectURIs = append(c.RedirectURIs, "http://localhost:8080/callback")
	got, err := admin.UpdateOAuth2Client(context.Background(), c)
	if err != nil {
		t.Fatalf("UpdateOAuth2Client: %v", err)
	}
	if len(got.RedirectURIs) != 2 || len(fake.clients[c.ID].RedirectURIs) != 2 {
		t.Fatalf("redirect URIs after update = %v", got.RedirectURIs)
	}

	c.ID = "unknown"
	if _, err := admin.UpdateOAuth2Client(context.Background(), c); !errors.Is(err, domain.ErrOAuth2ClientNotFound) {
		t.Fatalf("update of unknown id: expected ErrOAuth2ClientNotFound, got %v", err)
	}
}
