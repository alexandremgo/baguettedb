package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
)

// oidcStateTTL is the lifetime of the server-side flow state written by StartFlow. Five minutes covers a typical IdP
// login + 2FA prompt with margin while keeping the window for abandoned flows short.
const oidcStateTTL = 5 * time.Minute

// OIDCCallbackResult is the verdict of completing an OIDC sign-in callback.
type OIDCCallbackResult struct {
	// User is the local user the IdP identity resolves to. nil when NeedsSignup is true.
	User *domain.User
	// NeedsSignup is true when no local user matches and the caller must hand off to the signup-interstitial flow.
	NeedsSignup bool
	// Info is the verified user info returned by the IdP. Passed through so the signup hand-off can prefill fields.
	Info domain.OIDCUserInfo
}

// OIDCLoginService orchestrates both halves of an OIDC sign-in flow: StartFlow mints and persists the per-flow
// secrets before the IdP redirect, ConsumeFlow recovers them when the IdP redirects back, and HandleCallback
// exchanges the code and resolves the local user. The Hydra accept-login call is the caller's responsibility (the
// API handler in front of this service), since it renders the resulting redirect.
type OIDCLoginService struct {
	resolver  *ExternalIdentityService
	stateRepo OIDCStateRepository
}

// NewOIDCLoginService creates a new OIDCLoginService backed by the given external-identity resolver and flow-state
// repository.
func NewOIDCLoginService(resolver *ExternalIdentityService, stateRepo OIDCStateRepository) *OIDCLoginService {
	return &OIDCLoginService{resolver: resolver, stateRepo: stateRepo}
}

// StartFlow begins a sign-in round-trip against the given provider: it generates the state, nonce, and PKCE
// verifier, persists them server-side keyed by the state value, and returns the provider authorization URL to
// redirect the browser to, along with the stored flow state.
func (s *OIDCLoginService) StartFlow(
	ctx context.Context, provider OIDCProvider, loginChallenge string,
) (string, *domain.OIDCFlowState, error) {
	state, err := randomURLSafe(32)
	if err != nil {
		return "", nil, fmt.Errorf("generating state: %w", err)
	}
	nonce, err := randomURLSafe(32)
	if err != nil {
		return "", nil, fmt.Errorf("generating nonce: %w", err)
	}
	verifier, err := randomURLSafe(48)
	if err != nil {
		return "", nil, fmt.Errorf("generating pkce verifier: %w", err)
	}

	authURL, err := provider.AuthorizationURL(state, nonce, pkceChallenge(verifier))
	if err != nil {
		return "", nil, fmt.Errorf("building authorization URL: %w", err)
	}

	now := time.Now().UTC()
	flow := &domain.OIDCFlowState{
		State:          state,
		Provider:       provider.Name(),
		LoginChallenge: loginChallenge,
		Nonce:          nonce,
		CodeVerifier:   verifier,
		CreatedAt:      now,
		ExpiresAt:      now.Add(oidcStateTTL),
	}
	if err := s.stateRepo.Create(ctx, flow); err != nil {
		return "", nil, fmt.Errorf("storing flow state: %w", err)
	}
	return authURL, flow, nil
}

// ConsumeFlow atomically spends the flow state matching the state value an IdP callback echoed back. The state value
// is the flow's single-use server-side key: an unknown value means the flow was never started here, already
// completed in another tab, or sat at the IdP past the TTL — all surfaced as ErrOIDCStateNotFound. A flow issued for
// a different provider than the callback route names is rejected with ErrOIDCProviderMismatch (and stays spent).
func (s *OIDCLoginService) ConsumeFlow(
	ctx context.Context, providerName, state string,
) (*domain.OIDCFlowState, error) {
	flow, err := s.stateRepo.ConsumeByState(ctx, state)
	if err != nil {
		return nil, err
	}
	if flow.Provider != providerName {
		return nil, domain.ErrOIDCProviderMismatch
	}
	return flow, nil
}

// PurgeExpiredFlows removes flow state whose expiry is in the past, returning how many rows were deleted. Expired
// rows are inert (ConsumeFlow rejects them), so purging only bounds table growth from abandoned flows.
func (s *OIDCLoginService) PurgeExpiredFlows(ctx context.Context) (int64, error) {
	return s.stateRepo.DeleteExpired(ctx)
}

// HandleCallback exchanges the authorization code for an ID token, enforces email_verified, and resolves the IdP
// identity to a local user. Returns ErrEmailUnverified when the IdP did not vouch for the email — without that gate,
// the email-match branch in LookupOrLinkByEmail becomes an account-takeover primitive.
func (s *OIDCLoginService) HandleCallback(
	ctx context.Context,
	provider OIDCProvider,
	code, codeVerifier, nonce string,
) (*OIDCCallbackResult, error) {
	info, err := provider.ExchangeCode(ctx, code, codeVerifier, nonce)
	if err != nil {
		return nil, fmt.Errorf("exchanging authorization code: %w", err)
	}
	if !info.EmailVerified {
		return nil, domain.ErrEmailUnverified
	}

	user, _, err := s.resolver.LookupOrLinkByEmail(ctx, provider.Name(), info)
	if err != nil {
		return nil, fmt.Errorf("resolving external identity: %w", err)
	}

	if user == nil {
		return &OIDCCallbackResult{NeedsSignup: true, Info: info}, nil
	}
	return &OIDCCallbackResult{User: user, Info: info}, nil
}

// randomURLSafe returns n random bytes encoded as base64url without padding. Output length is ceil(4n/3) chars:
// each byte is 8 bits and each base64 char carries 6 bits, so 8n/6 = 4n/3 chars (rounded up).
func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// pkceChallenge returns the S256 PKCE code challenge for the given verifier per RFC 7636.
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
