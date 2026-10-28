package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
)

// ExternalIdentityService resolves an OIDC identity to a local user, creating the link row on first email-match.
// Never overwrites profile fields (first_name, last_name) on the existing user — the identity provider's profile is
// treated as read-only relative to the local user record.
type ExternalIdentityService struct {
	inTx RunInTx[IdentityTx]
}

// IdentityTx holds the repositories ExternalIdentityService reads and writes through within one transaction.
type IdentityTx struct {
	Identities identityLinker
	Users      identityUsers
}

// identityLinker stores the links between local users and external identity provider subjects.
type identityLinker interface {
	// Create persists a new link between a local user and an external provider's subject. Returns an error wrapping
	// the underlying constraint failure if (provider, provider_subject) already exists.
	Create(ctx context.Context, identity *domain.ExternalIdentity) error

	// FindByProviderSubject returns the identity for the given provider + provider_subject. Returns
	// domain.ErrExternalIdentityNotFound if no such row exists.
	FindByProviderSubject(ctx context.Context, provider, providerSubject string) (*domain.ExternalIdentity, error)
}

// identityUsers finds the local user an external identity resolves to and records its email as verified.
type identityUsers interface {
	// FindByID returns the user with the given ID. Returns domain.ErrUserNotFound if not found.
	FindByID(ctx context.Context, id uuid.UUID) (*domain.User, error)

	// FindByEmail returns the user with the given email. Returns domain.ErrUserNotFound if not found.
	FindByEmail(ctx context.Context, email string) (*domain.User, error)

	// MarkEmailVerified sets email_verified_at to the given timestamp iff it is currently NULL. Idempotent: a no-op
	// when the column is already populated. Returns domain.ErrUserNotFound if no row with the given id exists.
	MarkEmailVerified(ctx context.Context, id uuid.UUID, when time.Time) error
}

// NewExternalIdentityService creates a new ExternalIdentityService reading and writing through inTx.
func NewExternalIdentityService(inTx RunInTx[IdentityTx]) *ExternalIdentityService {
	return &ExternalIdentityService{inTx: inTx}
}

// LookupOrLinkByEmail resolves an OIDC user identity to a local user, optionally creating a link row.
//
// Returns one of three outcomes:
//   - (user, false, nil) — an external_identities row for (provider, info.Subject) already exists; user fetched.
//   - (user, true,  nil) — no link existed, but a local user with info.Email exists; a new link row was inserted.
//   - (nil,  false, nil) — neither match; the caller must hand off to a signup flow.
//
// In every "user found" branch, email_verified_at is set to now() iff currently NULL. All reads and writes run in a
// single database transaction.
func (s *ExternalIdentityService) LookupOrLinkByEmail(
	ctx context.Context, provider string, info domain.OIDCUserInfo,
) (*domain.User, bool, error) {
	var (
		user      *domain.User
		linkedNow bool
	)

	err := s.inTx(ctx, func(tx IdentityTx) error {
		now := time.Now().UTC()

		link, err := tx.Identities.FindByProviderSubject(ctx, provider, info.Subject)
		switch {
		case err == nil:
			u, err := tx.Users.FindByID(ctx, link.UserID)
			if err != nil {
				return fmt.Errorf("loading user for existing link: %w", err)
			}
			user = u
			return maybeMarkVerified(ctx, tx.Users, u, now)
		case errors.Is(err, domain.ErrExternalIdentityNotFound):
			// fall through to email-match branch
		default:
			return fmt.Errorf("looking up external identity: %w", err)
		}

		u, err := tx.Users.FindByEmail(ctx, info.Email)
		switch {
		case errors.Is(err, domain.ErrUserNotFound):
			return nil
		case err != nil:
			return fmt.Errorf("looking up user by email: %w", err)
		}

		newLink := &domain.ExternalIdentity{
			ID:              uuid.New(),
			UserID:          u.ID,
			Provider:        provider,
			ProviderSubject: info.Subject,
			Email:           info.Email,
			CreatedAt:       now,
		}
		if err := tx.Identities.Create(ctx, newLink); err != nil {
			return fmt.Errorf("creating external identity link: %w", err)
		}

		user = u
		linkedNow = true
		return maybeMarkVerified(ctx, tx.Users, u, now)
	})
	if err != nil {
		return nil, false, err
	}
	return user, linkedNow, nil
}

// maybeMarkVerified populates users.email_verified_at iff currently NULL. Idempotent.
func maybeMarkVerified(ctx context.Context, users identityUsers, u *domain.User, when time.Time) error {
	if u.EmailVerifiedAt != nil {
		return nil
	}
	if err := users.MarkEmailVerified(ctx, u.ID, when); err != nil {
		return fmt.Errorf("marking email verified: %w", err)
	}
	u.EmailVerifiedAt = &when
	return nil
}
