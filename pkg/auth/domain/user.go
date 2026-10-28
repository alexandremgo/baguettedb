// Package domain holds the auth model and its rules: users with their password and email checks, personal access tokens
// and sessions, links to external identity provider accounts, the state of OIDC sign-in flows, the exchanges with the
// OAuth2 authorization server (login, consent, tokens, client registrations), and the app's sentinel errors. The types
// carry no serialization tags: each adapter maps its own stored or wire form to them.
package domain

import (
	"regexp"
	"time"

	"github.com/google/uuid"
)

// emailRX is a sanity-check regex for email addresses.
// Taken from the WHATWG HTML spec: https://html.spec.whatwg.org/#valid-e-mail-address
var emailRX = regexp.MustCompile(`^[a-zA-Z0-9.!#$%&'*+\/=?^_` + "`" +
	`{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)

// User represents an authenticated user account. PasswordHash is a pointer so users created via an external
// identity provider (no local password) can hold nil — same convention as EmailVerifiedAt.
type User struct {
	ID              uuid.UUID
	Email           string
	PasswordHash    *string
	FirstName       string
	LastName        string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	IsActive        bool
	EmailVerifiedAt *time.Time
}

// UserProfile is the non-credential projection of a user, intended for read
// paths that display or expose user identity (member lists, audit views, etc.)
// without ever touching the password hash.
type UserProfile struct {
	ID        uuid.UUID
	Email     string
	FirstName string
	LastName  string
}

// ValidateEmail checks that the email matches a basic format.
func ValidateEmail(email string) error {
	if !emailRX.MatchString(email) {
		return ErrInvalidEmail
	}
	return nil
}
