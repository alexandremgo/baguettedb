package domain

import (
	"time"

	"github.com/google/uuid"
)

// ExternalIdentity links a local user to an external identity provider (e.g. Google). (provider, ProviderSubject)
// is globally unique; one user can have multiple external identities to support linking several providers over time.
type ExternalIdentity struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	Provider        string
	ProviderSubject string
	Email           string
	CreatedAt       time.Time
}

// OIDCUserInfo is the verified subset of an OIDC ID token / userinfo response the auth flow needs.
// EmailVerified MUST be the value the provider stamped in the ID token — never a default filled in when it is absent.
type OIDCUserInfo struct {
	Subject       string
	Email         string
	EmailVerified bool
	FirstName     string
	LastName      string
}
