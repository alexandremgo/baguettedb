package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
)

// WebClientID is the OAuth2 client id under which the platform registers its own web frontend.
const WebClientID = "baguettedb-web"

// oauth2ClientAdmin is the slice of the authorization server's admin API that web-client provisioning needs.
type oauth2ClientAdmin interface {
	GetOAuth2Client(ctx context.Context, clientID string) (*domain.OAuth2Client, error)
	CreateOAuth2Client(ctx context.Context, client domain.OAuth2Client) (*domain.OAuth2Client, error)
	UpdateOAuth2Client(ctx context.Context, client domain.OAuth2Client) (*domain.OAuth2Client, error)
}

// EnsureWebClient registers the web frontend's OAuth2 client under WebClientID when the authorization server has
// none, adds redirectURI to an existing registration that lacks it, and returns the client id to use. The
// registration is a public PKCE client (no secret, authorization code and refresh grants) with consent skipped,
// since the frontend is first-party. Two processes racing on the first registration both end up with the one that
// won.
func EnsureWebClient(
	ctx context.Context, admin oauth2ClientAdmin, redirectURI string, logger zerolog.Logger,
) (string, error) {
	existing, err := admin.GetOAuth2Client(ctx, WebClientID)
	switch {
	case errors.Is(err, domain.ErrOAuth2ClientNotFound):
		created, err := admin.CreateOAuth2Client(ctx, webClientRegistration(redirectURI))
		if errors.Is(err, domain.ErrOAuth2ClientExists) {
			return EnsureWebClient(ctx, admin, redirectURI, logger)
		}
		if err != nil {
			return "", fmt.Errorf("registering the web client: %w", err)
		}
		logger.Info().Str("client_id", created.ID).Str("redirect_uri", redirectURI).
			Msg("provisioned the web OAuth2 client")
		return created.ID, nil
	case err != nil:
		return "", fmt.Errorf("looking up the web client: %w", err)
	}

	if existing.HasRedirectURI(redirectURI) {
		return existing.ID, nil
	}
	updated := *existing
	updated.RedirectURIs = append(append([]string(nil), existing.RedirectURIs...), redirectURI)
	if _, err := admin.UpdateOAuth2Client(ctx, updated); err != nil {
		return "", fmt.Errorf("adding the redirect URI to the web client: %w", err)
	}
	logger.Info().Str("client_id", existing.ID).Str("redirect_uri", redirectURI).
		Msg("added the current redirect URI to the web OAuth2 client")
	return existing.ID, nil
}

// webClientRegistration is the registration the web frontend needs: authorization code with PKCE and refresh
// tokens, no client secret, the openid and offline scopes, and no consent screen.
func webClientRegistration(redirectURI string) domain.OAuth2Client {
	return domain.OAuth2Client{
		ID:                      WebClientID,
		Name:                    WebClientID,
		RedirectURIs:            []string{redirectURI},
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		Scope:                   "openid offline",
		TokenEndpointAuthMethod: "none",
		SkipConsent:             true,
	}
}
