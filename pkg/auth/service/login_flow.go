package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// rememberFor is how long, in seconds, the authorization server remembers a login or a consent the user gave.
const rememberFor = 3600

// LoginFlowService runs the OAuth2 login and consent requests the authorization server delegates to the app: it
// identifies the user, grants the scopes they agree to, and decides the roles the issued tokens carry.
type LoginFlowService struct {
	server authorizationServer
	roles  userRoles
}

// authorizationServer is the OAuth2 authorization server that delegates login and consent to the app. Every answer
// returns the URL the browser goes to next.
type authorizationServer interface {
	// GetLoginRequest fetches the login request challenge identifies.
	GetLoginRequest(ctx context.Context, challenge string) (*domain.LoginRequest, error)

	// AcceptLogin accepts the login request challenge identifies for req's subject.
	AcceptLogin(ctx context.Context, challenge string, req domain.LoginAcceptance) (string, error)

	// GetConsentRequest fetches the consent request challenge identifies.
	GetConsentRequest(ctx context.Context, challenge string) (*domain.ConsentRequest, error)

	// AcceptConsent grants the consent request challenge identifies as grant describes.
	AcceptConsent(ctx context.Context, challenge string, grant domain.ConsentGrant) (string, error)

	// RejectConsent rejects the consent request challenge identifies with req's error.
	RejectConsent(ctx context.Context, challenge string, req domain.Rejection) (string, error)
}

// userRoles reads the global roles of a user.
type userRoles interface {
	// GetRoles returns all roles assigned to the given user.
	GetRoles(ctx context.Context, userID uuid.UUID) ([]authz.Role, error)
}

// NewLoginFlowService creates a LoginFlowService answering server's login and consent requests, with the roles the
// issued tokens carry read from roles.
func NewLoginFlowService(server authorizationServer, roles userRoles) *LoginFlowService {
	return &LoginFlowService{server: server, roles: roles}
}

// StartLogin returns the login request challenge identifies, for the app to identify the user. When the
// authorization server already remembers a login for this browser, it is accepted right away instead, and StartLogin
// returns where to send the browser; the request is then nil.
func (s *LoginFlowService) StartLogin(
	ctx context.Context, challenge string,
) (req *domain.LoginRequest, redirectTo string, err error) {
	req, err = s.server.GetLoginRequest(ctx, challenge)
	if err != nil {
		return nil, "", fmt.Errorf("getting login request: %w", err)
	}
	if !req.Skip {
		return req, "", nil
	}
	redirectTo, err = s.server.AcceptLogin(ctx, challenge, domain.LoginAcceptance{Subject: req.Subject})
	if err != nil {
		return nil, "", fmt.Errorf("accepting remembered login: %w", err)
	}
	return nil, redirectTo, nil
}

// LoginRequest returns the login request challenge identifies, without answering it.
func (s *LoginFlowService) LoginRequest(ctx context.Context, challenge string) (*domain.LoginRequest, error) {
	req, err := s.server.GetLoginRequest(ctx, challenge)
	if err != nil {
		return nil, fmt.Errorf("getting login request: %w", err)
	}
	return req, nil
}

// CompleteLogin accepts the login request challenge identifies for userID, remembered for this browser, and returns
// where to send the browser.
func (s *LoginFlowService) CompleteLogin(ctx context.Context, challenge string, userID uuid.UUID) (string, error) {
	redirectTo, err := s.server.AcceptLogin(ctx, challenge, domain.LoginAcceptance{
		Subject:     userID.String(),
		Remember:    true,
		RememberFor: rememberFor,
	})
	if err != nil {
		return "", fmt.Errorf("accepting login: %w", err)
	}
	return redirectTo, nil
}

// StartConsent returns the consent request challenge identifies, for the user to agree to its scopes. When the
// authorization server remembers an earlier consent, or the client skips consent (a first-party client), every
// requested scope is granted right away instead, and StartConsent returns where to send the browser; the request is
// then nil.
func (s *LoginFlowService) StartConsent(
	ctx context.Context, challenge string,
) (req *domain.ConsentRequest, redirectTo string, err error) {
	req, err = s.server.GetConsentRequest(ctx, challenge)
	if err != nil {
		return nil, "", fmt.Errorf("getting consent request: %w", err)
	}
	if !req.Skip && !req.SkipConsent {
		return req, "", nil
	}
	grant, err := s.consentGrant(ctx, req, req.RequestedScopes, false)
	if err != nil {
		return nil, "", err
	}
	redirectTo, err = s.server.AcceptConsent(ctx, challenge, grant)
	if err != nil {
		return nil, "", fmt.Errorf("accepting skipped consent: %w", err)
	}
	return nil, redirectTo, nil
}

// GrantConsent grants the consent request challenge identifies for scopes, the ones the user agreed to, remembered
// for this client, and returns where to send the browser.
func (s *LoginFlowService) GrantConsent(ctx context.Context, challenge string, scopes []string) (string, error) {
	req, err := s.server.GetConsentRequest(ctx, challenge)
	if err != nil {
		return "", fmt.Errorf("getting consent request: %w", err)
	}
	grant, err := s.consentGrant(ctx, req, scopes, true)
	if err != nil {
		return "", err
	}
	redirectTo, err := s.server.AcceptConsent(ctx, challenge, grant)
	if err != nil {
		return "", fmt.Errorf("accepting consent: %w", err)
	}
	return redirectTo, nil
}

// RejectConsent rejects the consent request challenge identifies as denied by the user, and returns where to send
// the browser.
func (s *LoginFlowService) RejectConsent(ctx context.Context, challenge string) (string, error) {
	redirectTo, err := s.server.RejectConsent(ctx, challenge, domain.Rejection{
		Error:            "access_denied",
		ErrorDescription: "The user denied the request",
	})
	if err != nil {
		return "", fmt.Errorf("rejecting consent: %w", err)
	}
	return redirectTo, nil
}

// consentGrant builds the grant of scopes for req, remembered when remember is set: the audience req asks for, and the
// subject's global roles in both the access token's and the ID token's claims, so the tokens carry what the user may
// do.
func (s *LoginFlowService) consentGrant(
	ctx context.Context, req *domain.ConsentRequest, scopes []string, remember bool,
) (domain.ConsentGrant, error) {
	userID, err := uuid.Parse(req.Subject)
	if err != nil {
		return domain.ConsentGrant{}, fmt.Errorf("parsing consent subject: %w", err)
	}
	roles, err := s.roles.GetRoles(ctx, userID)
	if err != nil {
		return domain.ConsentGrant{}, fmt.Errorf("reading the subject's roles: %w", err)
	}

	roleNames := make([]string, len(roles))
	for i, role := range roles {
		roleNames[i] = string(role)
	}
	grant := domain.ConsentGrant{
		Scopes:            scopes,
		Audience:          req.RequestedAudience,
		AccessTokenClaims: map[string]any{"roles": roleNames},
		IDTokenClaims:     map[string]any{"sub": req.Subject, "roles": roleNames},
	}
	if remember {
		grant.Remember, grant.RememberFor = true, rememberFor
	}
	return grant, nil
}
