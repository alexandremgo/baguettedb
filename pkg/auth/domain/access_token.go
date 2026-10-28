package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// AccessTokenKind says what an access token is for.
type AccessTokenKind string

const (
	// AccessTokenPersonal is a token a user creates for scripts and local tools, sent as a bearer token.
	AccessTokenPersonal AccessTokenKind = "personal"

	// AccessTokenSession is the token a user's browser or app holds once signed in: it lasts a while and signing out
	// revokes it. A browser sends it in the session cookie, an app as a bearer token.
	AccessTokenSession AccessTokenKind = "session"
)

// Prefix returns the characters every token of the kind starts with: "bdb", a short code for the kind, and "_"
// ("bdbp_" for a personal token, "bdbsn_" for a session), or "" for an unknown kind. A
// bearer token tells itself apart from a JWT by its prefix, a leaked one is recognizable in a log or a secret scan, and
// a token presented as another kind is refused before any lookup.
func (k AccessTokenKind) Prefix() string {
	switch k {
	case AccessTokenPersonal:
		return "bdbp_"
	case AccessTokenSession:
		return "bdbsn_"
	default:
		return ""
	}
}

// AccessTokenPrefixLen is how many characters after its kind's prefix a token's TokenPrefix keeps in plain.
const AccessTokenPrefixLen = 8

// AccessTokenNameMaxLen is the longest name an access token may have.
const AccessTokenNameMaxLen = 100

// AccessToken is an opaque bearer token of a user. Only its hash is stored: the plain token is shown once, when it is
// created.
type AccessToken struct {
	ID     uuid.UUID
	UserID uuid.UUID
	Kind   AccessTokenKind
	Name   string

	// TokenPrefix is the first AccessTokenPrefixLen characters of the token after its kind's prefix, kept so a
	// listing can tell tokens apart.
	TokenPrefix string

	CreatedAt time.Time

	// ExpiresAt is nil for a token that never expires; LastUsedAt is nil until the token is first used; RevokedAt is
	// nil while the token is valid.
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// ValidateAccessTokenName returns name trimmed of surrounding spaces, or an ErrInvalidAccessTokenName when it is empty
// or longer than AccessTokenNameMaxLen.
func ValidateAccessTokenName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%w: the name is empty", ErrInvalidAccessTokenName)
	}
	if len(name) > AccessTokenNameMaxLen {
		return "", fmt.Errorf("%w: the name is longer than %d characters", ErrInvalidAccessTokenName,
			AccessTokenNameMaxLen)
	}
	return name, nil
}

// AccessTokenWithUser is an access token with what checking it needs about its user: whether the account is active,
// and its roles.
type AccessTokenWithUser struct {
	Token      *AccessToken
	UserActive bool
	Roles      []authz.Role
}
