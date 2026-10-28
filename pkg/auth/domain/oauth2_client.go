package domain

import "slices"

// OAuth2Client is a client registration in the authorization server, in the shape the platform manages for its own
// first-party web frontend: a public client (no secret) that authenticates users with the authorization code flow
// and refreshes tokens, and whose consent screen is skipped.
type OAuth2Client struct {
	ID                      string
	Name                    string
	RedirectURIs            []string
	GrantTypes              []string
	ResponseTypes           []string
	Scope                   string
	TokenEndpointAuthMethod string
	SkipConsent             bool
}

// HasRedirectURI reports whether uri is one of the client's registered redirect URIs.
func (c OAuth2Client) HasRedirectURI(uri string) bool {
	return slices.Contains(c.RedirectURIs, uri)
}
