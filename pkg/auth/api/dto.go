package api

import (
	"html/template"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// changePasswordRequest holds the JSON body for password changes. EndSessions, true when absent, ends every session of
// the user with the change, the caller's included.
type changePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
	EndSessions *bool  `json:"end_sessions"`
}

// userResponse represents a user in API responses.
type userResponse struct {
	ID        uuid.UUID    `json:"id"`
	Email     string       `json:"email"`
	FirstName string       `json:"first_name"`
	LastName  string       `json:"last_name"`
	Roles     []authz.Role `json:"roles,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	IsActive  bool         `json:"is_active"`
}

// errorResponse represents an error in API responses.
type errorResponse struct {
	Error string `json:"error"`
}

// loginPageData holds template data for the login page.
type loginPageData struct {
	Challenge  string
	ClientName string
	// IsFirstParty is true when the OAuth client is the first-party SPA, so
	// the template can skip the "to continue to …" subtitle.
	IsFirstParty bool
	Error        string
	// Providers is the list of external identity provider buttons to render below the email+password form. Empty
	// when no OIDC providers are registered.
	Providers []providerButton
}

// providerButton carries one provider's pre-rendered login button for the login template. RenderedHTML is the
// output of executing the provider's button template against ButtonData{Href, DisplayName} — the framework runs it
// once per request and the template inlines the result. HasStylesheet is true when the provider shipped non-empty
// CSS, gating the <link rel="stylesheet" href="/static/oidc/{Name}.css"> tag in the page head.
type providerButton struct {
	Name          string
	RenderedHTML  template.HTML
	HasStylesheet bool
}

// consentPageData holds template data for the consent page.
type consentPageData struct {
	Challenge  string
	ClientName string
	Scopes     []scopeView
}

// errorPageData holds template data for the error page.
type errorPageData struct {
	Message string
}

// adminCreateUserRequest holds the JSON body for admin user creation.
type adminCreateUserRequest struct {
	Email     string `json:"email"`
	Password  string `json:"password"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

// assignRoleRequest holds the JSON body for role assignment.
type assignRoleRequest struct {
	Role string `json:"role"`
}

// userListResponse represents a paginated list of users.
type userListResponse struct {
	Users []userResponse `json:"users"`
	Total int            `json:"total"`
}

// toUserResponse converts a domain User to an API response.
func toUserResponse(u *domain.User) userResponse {
	return userResponse{
		ID:        u.ID,
		Email:     u.Email,
		FirstName: u.FirstName,
		LastName:  u.LastName,
		CreatedAt: u.CreatedAt,
		IsActive:  u.IsActive,
	}
}

// toUserResponseWithRoles converts a domain User to an API response with roles.
func toUserResponseWithRoles(u *domain.User, roles []authz.Role) userResponse {
	return userResponse{
		ID:        u.ID,
		Email:     u.Email,
		FirstName: u.FirstName,
		LastName:  u.LastName,
		Roles:     roles,
		CreatedAt: u.CreatedAt,
		IsActive:  u.IsActive,
	}
}

// toUserResponseMap converts a domain User to a map so the optional UserResponseEnricher hook can add extra fields
// before the response is serialized.
func toUserResponseMap(u *domain.User, roles []authz.Role) map[string]any {
	roleStrings := make([]string, len(roles))
	for i, r := range roles {
		roleStrings[i] = string(r)
	}
	return map[string]any{
		"id":         u.ID,
		"email":      u.Email,
		"first_name": u.FirstName,
		"last_name":  u.LastName,
		"roles":      roleStrings,
		"created_at": u.CreatedAt,
		"is_active":  u.IsActive,
	}
}

// createAccessTokenRequest is the body of POST /api/auth/v1/personal-tokens. ExpiresAt, when set, is when the token
// stops working; absent, it never does.
type createAccessTokenRequest struct {
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// accessTokenResponse is a personal access token as the API returns it: never its value, only the prefix that tells
// it apart.
type accessTokenResponse struct {
	ID          uuid.UUID  `json:"id"`
	Name        string     `json:"name"`
	TokenPrefix string     `json:"token_prefix"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

// createdAccessTokenResponse is a token just created, with its plain value: the only response that carries it.
type createdAccessTokenResponse struct {
	accessTokenResponse
	Token string `json:"token"`
}

// accessTokenListResponse is the body of GET /api/auth/v1/personal-tokens.
type accessTokenListResponse struct {
	Tokens []accessTokenResponse `json:"tokens"`
}

// toAccessTokenResponse converts t to its API shape.
func toAccessTokenResponse(t *domain.AccessToken) accessTokenResponse {
	return accessTokenResponse{
		ID:          t.ID,
		Name:        t.Name,
		TokenPrefix: t.TokenPrefix,
		CreatedAt:   t.CreatedAt,
		LastUsedAt:  t.LastUsedAt,
		ExpiresAt:   t.ExpiresAt,
	}
}
