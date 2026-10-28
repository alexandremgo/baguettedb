package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// OwnerService resolves the account owning a local instance.
type OwnerService struct {
	inTx RunInTx[OwnerTx]
}

// OwnerTx holds the repositories OwnerService reads and writes through within one transaction.
type OwnerTx struct {
	Owner ownerRecord
	Users ownerUsers
	Roles ownerRoles
}

// ownerRecord records which account owns the instance, by ID, so the owner keeps their bdbs whatever their email
// becomes.
type ownerRecord interface {
	// Find returns the owner's user ID. Returns domain.ErrInstanceOwnerNotSet when none is recorded.
	Find(ctx context.Context) (uuid.UUID, error)

	// Set records userID as the owner, in place of any other.
	Set(ctx context.Context, userID uuid.UUID) error
}

// ownerUsers finds the owner's account, or creates it.
type ownerUsers interface {
	// FindByID returns the user with the given ID. Returns domain.ErrUserNotFound if not found.
	FindByID(ctx context.Context, id uuid.UUID) (*domain.User, error)

	// FindByEmail returns the user with the given email. Returns domain.ErrUserNotFound if not found.
	FindByEmail(ctx context.Context, email string) (*domain.User, error)

	// Create persists a new user. Returns domain.ErrEmailAlreadyExists if the email is taken.
	Create(ctx context.Context, user *domain.User) error
}

// ownerRoles grants the owner's roles and reads them back.
type ownerRoles interface {
	// AssignRole grants the given role to a user. The operation is idempotent.
	AssignRole(ctx context.Context, userID uuid.UUID, role authz.Role) error

	// GetRoles returns all roles assigned to the given user.
	GetRoles(ctx context.Context, userID uuid.UUID) ([]authz.Role, error)
}

// NewOwnerService creates a new OwnerService reading and writing through inTx.
func NewOwnerService(inTx RunInTx[OwnerTx]) *OwnerService {
	return &OwnerService{inTx: inTx}
}

// EnsureOwner returns the account owning a local instance, with its roles, creating it on the instance's first use:
// email defaultEmail, firstName as its first name, no password, and the user and admin roles. The owner is recorded by
// ID, so it stays the owner whatever its email becomes; an instance with no owner recorded yet adopts the account with
// defaultEmail when one exists, as stored. All of it runs in one transaction.
//
// Two callers racing on the first use are serialized by the SQLite instance store's immediate transactions; on
// Postgres the loser fails on the unique email.
func (s *OwnerService) EnsureOwner(
	ctx context.Context, defaultEmail, firstName string,
) (*domain.User, []authz.Role, error) {
	var owner *domain.User
	var roles []authz.Role
	err := s.inTx(ctx, func(tx OwnerTx) error {
		id, err := tx.Owner.Find(ctx)
		switch {
		case err == nil:
			if owner, err = tx.Users.FindByID(ctx, id); err != nil {
				return fmt.Errorf("getting owner %s: %w", id, err)
			}
		case errors.Is(err, domain.ErrInstanceOwnerNotSet):
			if owner, err = adoptOrCreateOwner(ctx, tx, defaultEmail, firstName); err != nil {
				return err
			}
			if err := tx.Owner.Set(ctx, owner.ID); err != nil {
				return err
			}
		default:
			return err
		}

		if roles, err = tx.Roles.GetRoles(ctx, owner.ID); err != nil {
			return fmt.Errorf("getting roles for owner %s: %w", owner.ID, err)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return owner, roles, nil
}

// adoptOrCreateOwner returns the account with email in tx, or creates it with firstName as its first name, no
// password, and the user and admin roles.
func adoptOrCreateOwner(ctx context.Context, tx OwnerTx, email, firstName string) (*domain.User, error) {
	owner, err := tx.Users.FindByEmail(ctx, email)
	if err == nil {
		return owner, nil
	}
	if !errors.Is(err, domain.ErrUserNotFound) {
		return nil, fmt.Errorf("getting owner %s: %w", email, err)
	}

	now := time.Now().UTC()
	owner = &domain.User{
		ID:        uuid.New(),
		Email:     email,
		FirstName: firstName,
		IsActive:  true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := tx.Users.Create(ctx, owner); err != nil {
		return nil, fmt.Errorf("creating new owner %s with email %s: %w", owner.ID, email, err)
	}
	for _, role := range []authz.Role{authz.RoleUser, authz.RoleAdmin} {
		if err := tx.Roles.AssignRole(ctx, owner.ID, role); err != nil {
			return nil, fmt.Errorf("assigning role %s to owner %s: %w", role, owner.ID, err)
		}
	}
	return owner, nil
}
