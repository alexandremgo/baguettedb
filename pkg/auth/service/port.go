package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// RunInTx runs fn in one database transaction, with the repositories of T bound to it: the transaction is committed
// when fn returns nil and rolled back otherwise. Each transactional service names the repositories it writes through
// in its own T. Exported so that the app's setup can build one for each storage backend.
type RunInTx[T any] func(ctx context.Context, fn func(T) error) error

// OIDCProvider is an external OpenID Connect identity provider users sign in through (Google, GitHub, …). Exported so
// that a distribution can register implementations through the app's SetupOptions.
type OIDCProvider interface {
	// Name is the URL-safe slug used in routes and stored identity links (e.g. "google").
	Name() string

	// AuthorizationURL builds the provider's authorize endpoint URL with the given state, nonce, and PKCE challenge.
	// The returned URL is what the user's browser is redirected to.
	AuthorizationURL(state, nonce, codeChallenge string) (string, error)

	// ExchangeCode trades the authorization code for an ID token, validates the token (signature, iss, aud, nonce,
	// exp) against the provider's JWKS, and returns the extracted user info, which is then taken as authoritative.
	ExchangeCode(ctx context.Context, code, codeVerifier, nonce string) (domain.OIDCUserInfo, error)
}

// UserRepository defines persistence operations for user accounts. Exported so that the app's setup can hold one
// for each storage backend.
type UserRepository interface {
	// Create persists a new user. Returns domain.ErrEmailAlreadyExists if the email is taken.
	Create(ctx context.Context, user *domain.User) error

	// FindByEmail returns the user with the given email. Returns domain.ErrUserNotFound if not found.
	FindByEmail(ctx context.Context, email string) (*domain.User, error)

	// FindByID returns the user with the given ID. Returns domain.ErrUserNotFound if not found.
	FindByID(ctx context.Context, id uuid.UUID) (*domain.User, error)

	// FindByIDs returns the non-credential profile of each user matching the given IDs. Unknown IDs are silently
	// omitted; the returned slice may be shorter than the input. Order is not guaranteed. An empty input returns a nil
	// slice without touching the database. Implementations must not project the password hash on this path.
	FindByIDs(ctx context.Context, ids []uuid.UUID) ([]*domain.UserProfile, error)

	// UpdatePassword updates the password hash for the given user. Returns domain.ErrUserNotFound if the user does not
	// exist.
	UpdatePassword(ctx context.Context, id uuid.UUID, passwordHash string) error

	// UpdateIdentity sets the email and names of the given user. Returns domain.ErrUserNotFound if the user does not
	// exist, domain.ErrEmailAlreadyExists if another user has the email.
	UpdateIdentity(ctx context.Context, id uuid.UUID, email, firstName, lastName string) error

	// MarkEmailVerified sets email_verified_at to the given timestamp iff it is currently NULL. Idempotent: a no-op
	// when the column is already populated. Returns domain.ErrUserNotFound if no row with the given id exists.
	MarkEmailVerified(ctx context.Context, id uuid.UUID, when time.Time) error

	// ListUsers returns a paginated list of users matching the search query, along with the total count.
	ListUsers(ctx context.Context, search string, limit, offset int) ([]*domain.User, int, error)

	// Exists returns true if at least one user exists.
	Exists(ctx context.Context) (bool, error)
}

// RoleRepository defines persistence operations for user roles. Exported so that the app's setup can hold one for
// each storage backend.
type RoleRepository interface {
	// AssignRole grants the given role to a user. The operation is idempotent.
	AssignRole(ctx context.Context, userID uuid.UUID, role authz.Role) error

	// RemoveRole revokes the given role from a user.
	RemoveRole(ctx context.Context, userID uuid.UUID, role authz.Role) error

	// GetRoles returns all roles assigned to the given user.
	GetRoles(ctx context.Context, userID uuid.UUID) ([]authz.Role, error)
}

// OIDCStateRepository defines persistence operations for in-flight external sign-in flow state. A row is written
// when a flow starts, consumed exactly once when the IdP redirects back, and garbage-collected after expiry. Exported
// so that the app's setup can hold one for each storage backend.
type OIDCStateRepository interface {
	// Create persists the flow state for a freshly started sign-in round-trip.
	Create(ctx context.Context, state *domain.OIDCFlowState) error

	// ConsumeByState atomically deletes and returns the flow state for the given state value, so concurrent calls
	// for the same value succeed at most once. Returns domain.ErrOIDCStateNotFound when no row matches or the matching
	// row has expired.
	ConsumeByState(ctx context.Context, state string) (*domain.OIDCFlowState, error)

	// DeleteExpired removes rows whose expiry is in the past and returns how many were deleted.
	DeleteExpired(ctx context.Context) (int64, error)
}
