package service

import (
	"context"
	"testing"

	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
)

const localRedirect = "http://localhost:5173/callback"

// memClientAdmin implements oauth2ClientAdmin over a map, counting writes so tests can assert idempotency.
type memClientAdmin struct {
	clients map[string]domain.OAuth2Client
	creates int
	updates int
}

// newMemClientAdmin returns an empty in-memory admin, the state of an authorization server before first boot.
func newMemClientAdmin() *memClientAdmin {
	return &memClientAdmin{clients: map[string]domain.OAuth2Client{}}
}

func (m *memClientAdmin) GetOAuth2Client(_ context.Context, id string) (*domain.OAuth2Client, error) {
	c, ok := m.clients[id]
	if !ok {
		return nil, domain.ErrOAuth2ClientNotFound
	}
	return &c, nil
}

func (m *memClientAdmin) CreateOAuth2Client(_ context.Context, c domain.OAuth2Client) (*domain.OAuth2Client, error) {
	m.creates++
	if _, exists := m.clients[c.ID]; exists {
		return nil, domain.ErrOAuth2ClientExists
	}
	m.clients[c.ID] = c
	return &c, nil
}

func (m *memClientAdmin) UpdateOAuth2Client(_ context.Context, c domain.OAuth2Client) (*domain.OAuth2Client, error) {
	m.updates++
	if _, exists := m.clients[c.ID]; !exists {
		return nil, domain.ErrOAuth2ClientNotFound
	}
	m.clients[c.ID] = c
	return &c, nil
}

// TestEnsureWebClient_CreatesWhenAbsent checks that a first boot registers the fixed-id public PKCE client with
// consent skipped and the given redirect URI, using exactly one create and no update.
func TestEnsureWebClient_CreatesWhenAbsent(t *testing.T) {
	admin := newMemClientAdmin()

	id, err := EnsureWebClient(context.Background(), admin, localRedirect, zerolog.Nop())
	if err != nil {
		t.Fatalf("EnsureWebClient: %v", err)
	}
	if id != WebClientID {
		t.Fatalf("client id = %q, want %q", id, WebClientID)
	}
	got := admin.clients[WebClientID]
	if !got.SkipConsent || got.TokenEndpointAuthMethod != "none" || got.Scope != "openid offline" {
		t.Fatalf("registration = %+v, want a public PKCE client with consent skipped", got)
	}
	if !got.HasRedirectURI(localRedirect) {
		t.Fatalf("redirect URIs = %v", got.RedirectURIs)
	}
	if admin.creates != 1 || admin.updates != 0 {
		t.Fatalf("writes = %d creates, %d updates, want 1 and 0", admin.creates, admin.updates)
	}
}

// TestEnsureWebClient_AddsMissingRedirectURI checks that a boot under a new public origin appends its redirect
// URI to the existing registration instead of creating a second client, keeping the earlier URI.
func TestEnsureWebClient_AddsMissingRedirectURI(t *testing.T) {
	admin := newMemClientAdmin()
	if _, err := EnsureWebClient(context.Background(), admin, localRedirect, zerolog.Nop()); err != nil {
		t.Fatalf("first EnsureWebClient: %v", err)
	}

	id, err := EnsureWebClient(context.Background(), admin, "https://bdb.example.com/callback", zerolog.Nop())
	if err != nil {
		t.Fatalf("second EnsureWebClient: %v", err)
	}
	if id != WebClientID {
		t.Fatalf("client id = %q, want %q", id, WebClientID)
	}
	got := admin.clients[WebClientID]
	if len(got.RedirectURIs) != 2 || !got.HasRedirectURI("https://bdb.example.com/callback") {
		t.Fatalf("redirect URIs = %v, want both origins", got.RedirectURIs)
	}
	if admin.creates != 1 || admin.updates != 1 {
		t.Fatalf("writes = %d creates, %d updates, want 1 and 1", admin.creates, admin.updates)
	}
}

// TestEnsureWebClient_IsANoOpWhenAlreadyRegistered checks that a boot whose redirect URI is already registered
// performs no write at all.
func TestEnsureWebClient_IsANoOpWhenAlreadyRegistered(t *testing.T) {
	admin := newMemClientAdmin()
	if _, err := EnsureWebClient(context.Background(), admin, localRedirect, zerolog.Nop()); err != nil {
		t.Fatalf("first EnsureWebClient: %v", err)
	}

	if _, err := EnsureWebClient(context.Background(), admin, localRedirect, zerolog.Nop()); err != nil {
		t.Fatalf("second EnsureWebClient: %v", err)
	}
	if admin.creates != 1 || admin.updates != 0 {
		t.Fatalf("writes = %d creates, %d updates, want 1 and 0", admin.creates, admin.updates)
	}
}
