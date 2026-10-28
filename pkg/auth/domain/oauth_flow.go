package domain

// LoginRequest is a login the authorization server delegates to the app: the user must be identified before the
// OAuth2 flow carries on. Challenge identifies it across the redirects.
type LoginRequest struct {
	Challenge       string
	RequestedScopes []string
	// Subject is the user the authorization server already knows for this browser, set when Skip is.
	Subject string
	// Skip is set when the authorization server remembers an earlier login, so the app accepts it without asking.
	Skip       bool
	RequestURL string
	ClientID   string
	ClientName string
}

// LoginAcceptance identifies the user a login request is accepted for, and how long the authorization server
// remembers it.
type LoginAcceptance struct {
	Subject     string
	Remember    bool
	RememberFor int
}

// ConsentRequest is a consent the authorization server delegates to the app: the user must agree to the scopes a
// client asks for. Challenge identifies it across the redirects.
type ConsentRequest struct {
	Challenge         string
	RequestedScopes   []string
	RequestedAudience []string
	Subject           string
	// Skip is set when the authorization server remembers an earlier consent for this client and these scopes.
	Skip bool
	// SkipConsent is a flag registered on the client: the authorization server passes it through without enforcing
	// it, so the app grants consent itself (first-party clients like the web app).
	SkipConsent bool
	ClientID    string
	ClientName  string
}

// ConsentGrant is the consent the app gives: the scopes and audience granted, how long the authorization server
// remembers it, and the claims the issued access and ID tokens carry.
type ConsentGrant struct {
	Scopes            []string
	Audience          []string
	Remember          bool
	RememberFor       int
	AccessTokenClaims map[string]any
	IDTokenClaims     map[string]any
}

// Rejection is the OAuth2 error the app answers a login or consent request with.
type Rejection struct {
	Error            string
	ErrorDescription string
}

// TokenRequest is a request to the authorization server's token endpoint: an authorization code exchange
// (GrantType "authorization_code", with Code, CodeVerifier and RedirectURI) or a refresh (GrantType
// "refresh_token", with RefreshToken). Fields a grant does not use are left empty.
type TokenRequest struct {
	GrantType    string
	ClientID     string
	Code         string
	CodeVerifier string
	RedirectURI  string
	RefreshToken string
}

// TokenSet is the tokens the authorization server's token endpoint issues.
type TokenSet struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresIn    int
}
