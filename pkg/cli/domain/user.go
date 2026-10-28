package domain

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// User is an account of the instance as the command line sees it. Admin reports that it holds the admin role,
// HasPassword that it can sign in with a password.
type User struct {
	ID          uuid.UUID
	Email       string
	FirstName   string
	LastName    string
	Admin       bool
	HasPassword bool
	CreatedAt   time.Time
}

// AccessToken is a personal access token as the command line sees it, never with its value.
type AccessToken struct {
	ID   uuid.UUID
	Name string

	// Prefix is the start of the token's value, its kind prefix included, enough to tell it apart from the others.
	Prefix string

	CreatedAt time.Time

	// LastUsedAt is when the token last authenticated a request, to within a minute; nil when it never has.
	LastUsedAt *time.Time

	// ExpiresAt is when the token stops working; nil when it never does.
	ExpiresAt *time.Time
}

// ErrInvalidTokenLifetime reports a token lifetime that is not a positive duration.
var ErrInvalidTokenLifetime = errors.New("want a positive duration, such as 30d, 12h or 90m")

// ParseTokenLifetime returns the duration s gives: a whole number of days followed by d, such as 30d, or a Go duration,
// such as 12h or 90m. One that isn't positive, or too long to represent, is an ErrInvalidTokenLifetime.
func ParseTokenLifetime(s string) (time.Duration, error) {
	const day = 24 * time.Hour
	var d time.Duration
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.ParseInt(days, 10, 64)
		if err != nil || n > int64(math.MaxInt64/day) {
			return 0, fmt.Errorf("%q: %w", s, ErrInvalidTokenLifetime)
		}
		d = time.Duration(n) * day
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, fmt.Errorf("%q: %w", s, ErrInvalidTokenLifetime)
		}
	}
	if d <= 0 {
		return 0, fmt.Errorf("%q: %w", s, ErrInvalidTokenLifetime)
	}
	return d, nil
}

// ErrUserNotFound reports an email that matches no account.
var ErrUserNotFound = errors.New("no user")

// ErrInvalidEmail reports a string that is not an email address.
var ErrInvalidEmail = errors.New("not an email address")

// ErrUserExists reports an email that an account already has.
var ErrUserExists = errors.New("a user already has that email")

// ErrWeakPassword reports a password too short or too simple to be accepted.
var ErrWeakPassword = errors.New("the password is too weak")

// ErrTokenNotFound reports a token ID that matches none of an account's tokens that are not revoked.
var ErrTokenNotFound = errors.New("no token")

// AmbiguousTokenError reports a token reference that is a prefix of several token IDs.
type AmbiguousTokenError struct {
	Ref     string
	Matches []string
}

// Error lists the matching token IDs.
func (e *AmbiguousTokenError) Error() string {
	return fmt.Sprintf("%q matches several tokens: %s; give more of the token ID", e.Ref, strings.Join(e.Matches, ", "))
}

// ResolveToken returns the token among tokens that ref names: its full ID, or a prefix of it, in any case. Several
// matches are an *AmbiguousTokenError; none, or an empty ref, wraps ErrTokenNotFound.
func ResolveToken(ref string, tokens []AccessToken) (AccessToken, error) {
	prefix := strings.ToLower(ref)
	var matches []AccessToken
	for _, t := range tokens {
		if prefix != "" && strings.HasPrefix(t.ID.String(), prefix) {
			matches = append(matches, t)
		}
	}
	switch len(matches) {
	case 0:
		return AccessToken{}, fmt.Errorf("%w with an ID starting with %q", ErrTokenNotFound, ref)
	case 1:
		return matches[0], nil
	default:
		ids := make([]string, len(matches))
		for i, t := range matches {
			ids[i] = t.ID.String()
		}
		return AccessToken{}, &AmbiguousTokenError{Ref: ref, Matches: ids}
	}
}
