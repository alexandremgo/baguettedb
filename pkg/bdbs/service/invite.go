package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
)

// bdbFinder loads a bdb aggregate by id.
type bdbFinder interface {
	// FindByID returns the bdb with the given ID, including all its members.
	// Returns domain.ErrBdbNotFound if not found.
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Bdb, error)
}

// inviteRepository defines persistence operations for bdb invites.
type inviteRepository interface {
	// Create persists a new invite.
	Create(ctx context.Context, invite *domain.Invite) error

	// FindByToken returns the invite with the given token.
	// Returns domain.ErrInviteNotFound if not found.
	FindByToken(ctx context.Context, token string) (*domain.Invite, error)

	// FindByID returns the invite with the given ID.
	// Returns domain.ErrInviteNotFound if not found.
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Invite, error)

	// Update updates the invite's mutable fields (status, accepted_by, accepted_at).
	Update(ctx context.Context, invite *domain.Invite) error

	// ListByBdb returns all invites for the given bdb.
	ListByBdb(ctx context.Context, bdbID uuid.UUID) ([]*domain.Invite, error)
}

// InviteAcceptTx holds what accepting an invite reads and writes through within one transaction.
type InviteAcceptTx struct {
	Bdbs    lockingBdbFinder
	Members memberWriter
	Invites inviteAccepter
}

// inviteAccepter records an invite's acceptance.
type inviteAccepter interface {
	// MarkInviteAccepted stores invite as accepted, provided it is still pending; an invite accepted meanwhile is a
	// domain.ErrInviteAlreadyUsed.
	MarkInviteAccepted(ctx context.Context, invite *domain.Invite) error
}

// InviteService handles invite lifecycle operations.
type InviteService struct {
	inviteRepo inviteRepository
	bdbRepo    bdbFinder
	inTx       RunInTx[InviteAcceptTx]
	limits     quota.Resolver
	authApp    AuthApp
	logger     zerolog.Logger
}

// NewInviteService creates a new InviteService storing invites in inviteRepo, reading bdbs from bdbRepo, and
// accepting invites through inTx.
func NewInviteService(
	inviteRepo inviteRepository,
	bdbRepo bdbFinder,
	inTx RunInTx[InviteAcceptTx],
	limits quota.Resolver,
	authApp AuthApp,
	logger zerolog.Logger,
) *InviteService {
	return &InviteService{
		inviteRepo: inviteRepo,
		bdbRepo:    bdbRepo,
		inTx:       inTx,
		limits:     limits,
		authApp:    authApp,
		logger:     logger,
	}
}

// CreateInvite generates a new invite link for the given bdb. Only the bdb
// owner can create invites. The role must be editor or viewer.
func (s *InviteService) CreateInvite(
	ctx context.Context, requestingUserID, bdbID uuid.UUID, role authz.BdbRole, expiresAt time.Time,
) (*domain.Invite, error) {
	bdb, err := s.bdbRepo.FindByID(ctx, bdbID)
	if err != nil {
		return nil, fmt.Errorf("finding bdb: %w", err)
	}

	if !s.hasManageMembersPermission(bdb, requestingUserID) {
		return nil, domain.ErrNotBdbMember
	}

	invite, err := domain.NewInvite(bdbID, requestingUserID, role, expiresAt)
	if err != nil {
		return nil, err
	}

	if err := s.inviteRepo.Create(ctx, invite); err != nil {
		return nil, fmt.Errorf("creating invite: %w", err)
	}

	s.logger.Info().
		Str("invite_id", invite.ID.String()).
		Str("bdb_id", bdbID.String()).
		Str("created_by", requestingUserID.String()).
		Str("role", string(role)).
		Msg("Created bdb invite")

	return invite, nil
}

// InviteWithContext holds an invite along with resolved context (bdb, creator email).
type InviteWithContext struct {
	Invite       *domain.Invite
	Bdb          *domain.Bdb
	CreatorEmail string
}

// GetInviteByToken returns the invite with its associated bdb and creator email.
// Returns ErrInviteBdbDeleted if the target bdb has been soft-deleted.
func (s *InviteService) GetInviteByToken(ctx context.Context, token string) (*InviteWithContext, error) {
	invite, err := s.inviteRepo.FindByToken(ctx, token)
	if err != nil {
		return nil, err
	}

	bdb, err := s.bdbRepo.FindByID(ctx, invite.BdbID)
	if err != nil {
		return nil, fmt.Errorf("finding bdb for invite: %w", err)
	}

	if bdb.IsSoftDeleted() {
		return nil, domain.ErrInviteBdbDeleted
	}

	var creatorEmail string
	profiles, err := s.authApp.GetUserProfiles(ctx, []uuid.UUID{invite.CreatedBy})
	if err != nil {
		s.logger.Warn().Err(err).Str("user_id", invite.CreatedBy.String()).
			Msg("failed to resolve invite creator profile")
	} else if profile, ok := profiles[invite.CreatedBy]; ok {
		creatorEmail = profile.Email
	}

	return &InviteWithContext{
		Invite:       invite,
		Bdb:          bdb,
		CreatorEmail: creatorEmail,
	}, nil
}

// AcceptInvite redeems an invite, adding the user as a member of the bdb. The invite must be pending and not
// expired. The member limit is resolved from the bdb owner's roles.
//
// The bdb is loaded with its members held, so concurrent accepts and member changes run one after the other: the
// cap is checked against what every earlier change stored, and a single-use invite admits exactly one user
// (domain.ErrInviteAlreadyUsed for the others). Returns ErrInviteBdbDeleted if the target bdb has been soft-deleted
// between invite issue and acceptance.
func (s *InviteService) AcceptInvite(ctx context.Context, token string, userID uuid.UUID) (*domain.Bdb, error) {
	invite, err := s.inviteRepo.FindByToken(ctx, token)
	if err != nil {
		return nil, err
	}

	if err := invite.Accept(userID); err != nil {
		return nil, err
	}

	// The member cap comes from the owner's roles. A bdb keeps its owner for life, so they are resolved here rather
	// than while the transaction holds the bdb.
	snapshot, err := s.bdbRepo.FindByID(ctx, invite.BdbID)
	if err != nil {
		return nil, fmt.Errorf("finding bdb: %w", err)
	}
	owner := snapshot.Owner()
	if owner == nil {
		return nil, errors.New("bdb has no owner")
	}
	ownerRoles, err := s.authApp.GetUserRoles(ctx, owner.UserID)
	if err != nil {
		return nil, fmt.Errorf("getting owner roles: %w", err)
	}
	maxMembers := s.limits(ownerRoles).MaxMembersPerBdb

	err = s.inTx(ctx, func(tx InviteAcceptTx) error {
		bdb, err := tx.Bdbs.FindByIDForUpdate(ctx, invite.BdbID)
		if err != nil {
			return fmt.Errorf("finding bdb: %w", err)
		}
		if bdb.IsSoftDeleted() {
			return domain.ErrInviteBdbDeleted
		}

		before := slices.Clone(bdb.Members)
		if err := bdb.AdmitMember(userID, invite.Role, maxMembers); err != nil {
			return err
		}
		if err := tx.Invites.MarkInviteAccepted(ctx, invite); err != nil {
			return err
		}
		added, changed, removed := domain.DiffMembers(before, bdb.Members)
		return tx.Members.ApplyMemberChanges(ctx, bdb.ID, added, changed, removed)
	})
	if err != nil {
		return nil, fmt.Errorf("accepting invite: %w", err)
	}

	s.logger.Info().
		Str("invite_id", invite.ID.String()).
		Str("bdb_id", invite.BdbID.String()).
		Str("user_id", userID.String()).
		Str("role", string(invite.Role)).
		Msg("Invite accepted")

	// Reload bdb to include the new member.
	bdb, err := s.bdbRepo.FindByID(ctx, invite.BdbID)
	if err != nil {
		return nil, fmt.Errorf("reloading bdb: %w", err)
	}

	return bdb, nil
}

// RevokeInvite revokes a pending invite. Only the bdb owner can revoke.
func (s *InviteService) RevokeInvite(ctx context.Context, requestingUserID uuid.UUID, inviteID uuid.UUID) error {
	invite, err := s.inviteRepo.FindByID(ctx, inviteID)
	if err != nil {
		return err
	}

	bdb, err := s.bdbRepo.FindByID(ctx, invite.BdbID)
	if err != nil {
		return fmt.Errorf("finding bdb: %w", err)
	}

	if !s.hasManageMembersPermission(bdb, requestingUserID) {
		return domain.ErrNotBdbMember
	}

	if err := invite.Revoke(); err != nil {
		return err
	}

	if err := s.inviteRepo.Update(ctx, invite); err != nil {
		return fmt.Errorf("updating invite: %w", err)
	}

	s.logger.Info().
		Str("invite_id", invite.ID.String()).
		Str("bdb_id", bdb.ID.String()).
		Str("revoked_by", requestingUserID.String()).
		Msg("Invite revoked")

	return nil
}

// ListBdbInvites returns all invites for a bdb. Only the owner can list.
func (s *InviteService) ListBdbInvites(
	ctx context.Context, requestingUserID, bdbID uuid.UUID,
) ([]*domain.Invite, error) {
	bdb, err := s.bdbRepo.FindByID(ctx, bdbID)
	if err != nil {
		return nil, fmt.Errorf("finding bdb: %w", err)
	}

	if !s.hasManageMembersPermission(bdb, requestingUserID) {
		return nil, domain.ErrNotBdbMember
	}

	invites, err := s.inviteRepo.ListByBdb(ctx, bdbID)
	if err != nil {
		return nil, fmt.Errorf("listing invites: %w", err)
	}

	return invites, nil
}

// hasManageMembersPermission checks whether the user has bdb:manage_members permission.
func (s *InviteService) hasManageMembersPermission(bdb *domain.Bdb, userID uuid.UUID) bool {
	member := bdb.FindMember(userID)
	if member == nil {
		return false
	}
	return authz.BdbRoleHasPermission(member.Role, authz.BdbPermManageMembers)
}
