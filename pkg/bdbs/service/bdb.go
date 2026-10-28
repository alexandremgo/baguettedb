// Package service holds the bdbs application services: bdb lifecycle, membership and per-user ordering, the purge of
// soft-deleted bdbs past their grace period, invites, and the resolution of membership into database permissions. It
// reaches storage and the other apps only through ports: AuthApp and DatabasesApp for the auth and databases apps, and
// unexported repository interfaces, declared next to the service consuming each, for storage.
package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
)

var tracer = otel.Tracer("baguettedb/bdbs/service")

// bdbRepository defines persistence operations for the Bdb aggregate. The multi-statement operations
// (CreateOwned, Update) are transactional inside the implementation — callers get atomicity without managing a
// transaction.
type bdbRepository interface {
	// CreateOwned persists a new bdb and all its members in one transaction, enforcing the owner's bdb cap:
	// when maxOwned > 0, concurrent creations by the same owner serialize and the call returns
	// domain.BdbLimitReachedError at or above the cap. maxOwned = 0 skips the guard.
	CreateOwned(ctx context.Context, bdb *domain.Bdb, maxOwned int) error

	// FindByID returns the bdb with the given ID, including all its members.
	// Returns domain.ErrBdbNotFound if not found.
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Bdb, error)

	// FindMember returns the member for the given bdb and user.
	// Returns domain.ErrMemberNotFound if not found.
	FindMember(ctx context.Context, bdbID, userID uuid.UUID) (*domain.Member, error)

	// Update saves the bdb's name and description; its members are left as stored. A name another of the owner's
	// bdbs has, whatever the case, is a domain.ErrBdbNameTaken.
	Update(ctx context.Context, bdb *domain.Bdb) error

	// SoftDelete marks the bdb as deleted by setting deleted_at on the row.
	// Returns domain.ErrBdbNotFound if no active bdb matches the id (already
	// soft-deleted rows are also reported as not-found).
	SoftDelete(ctx context.Context, id uuid.UUID, at time.Time) error

	// Restore clears deleted_at on a soft-deleted bdb.
	// Returns domain.ErrBdbNotFound if no soft-deleted bdb matches the id.
	Restore(ctx context.Context, id uuid.UUID) error

	// HardDelete removes the bdb row. Members are cascade-deleted by the database.
	// Returns domain.ErrBdbNotFound if not found.
	HardDelete(ctx context.Context, id uuid.UUID) error

	// ListPurgeable returns the ids of soft-deleted bdbs whose deleted_at is
	// strictly before cutoff.
	ListPurgeable(ctx context.Context, cutoff time.Time) ([]uuid.UUID, error)

	// ListByUser returns all bdbs the given user belongs to, including their members.
	ListByUser(ctx context.Context, userID uuid.UUID) ([]*domain.Bdb, error)

	// ListOwnedIDsByUser returns the IDs of every bdb the given user owns, including soft-deleted bdbs
	// still awaiting purge (their storage remains allocated until the purge removes it).
	ListOwnedIDsByUser(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error)

	// ReorderForUser renumbers the given user's memberships so their bdbs sit in the order the ids arrive
	// in. Ids the user is not a member of are ignored, so callers must validate the set first.
	ReorderForUser(ctx context.Context, userID uuid.UUID, orderedBdbIDs []uuid.UUID) error

	// CountActive returns the number of bdbs that are not soft-deleted.
	CountActive(ctx context.Context) (int64, error)
}

// MembershipTx holds what a member change reads and writes through within one transaction.
type MembershipTx struct {
	Bdbs    lockingBdbFinder
	Members memberWriter
}

// lockingBdbFinder loads a bdb for a change that must see what every earlier change to it stored.
type lockingBdbFinder interface {
	// FindByIDForUpdate returns the bdb with id and its members, holding it until the transaction ends so changes
	// to the same bdb run one after the other. Returns domain.ErrBdbNotFound if not found.
	FindByIDForUpdate(ctx context.Context, id uuid.UUID) (*domain.Bdb, error)
}

// memberWriter stores the member changes domain.DiffMembers computes.
type memberWriter interface {
	// ApplyMemberChanges inserts added as members of the bdb bdbID, updates the role of each of changed, and deletes
	// removed. A user already a member is a domain.ErrAlreadyMember.
	ApplyMemberChanges(ctx context.Context, bdbID uuid.UUID, added, changed, removed []domain.Member) error
}

// BdbService handles bdb lifecycle and membership operations.
type BdbService struct {
	repo      bdbRepository
	inTx      RunInTx[MembershipTx]
	authApp   AuthApp
	limits    quota.Resolver
	logger    zerolog.Logger
	dbApp     DatabasesApp
	purgeLock purgeLock
}

// NewBdbService creates a new BdbService reading and saving bdbs through repo, and changing their members through
// inTx.
func NewBdbService(
	repo bdbRepository,
	inTx RunInTx[MembershipTx],
	authApp AuthApp,
	limits quota.Resolver,
	logger zerolog.Logger,
) *BdbService {
	return &BdbService{
		repo:    repo,
		inTx:    inTx,
		authApp: authApp,
		limits:  limits,
		logger:  logger,
	}
}

// SetDatabasesApp injects the databases module dependency after construction. The databases module is set up after
// bdbs, so this is wired lazily once both modules exist.
func (s *BdbService) SetDatabasesApp(app DatabasesApp) {
	s.dbApp = app
}

// CountActiveBdbs returns the number of bdbs that are not soft-deleted, across every user.
func (s *BdbService) CountActiveBdbs(ctx context.Context) (int64, error) {
	return s.repo.CountActive(ctx)
}

// SetPurgeLock injects the cross-replica sweep lock used by PurgeExpired. When left unset the sweep runs unguarded,
// which is correct for a single-replica deployment where there is no contender.
func (s *BdbService) SetPurgeLock(lock purgeLock) {
	s.purgeLock = lock
}

// CreateBdb creates a new bdb and adds the creator as owner.
// It enforces the bdb count limit based on the user's roles.
func (s *BdbService) CreateBdb(
	ctx context.Context, userID uuid.UUID, roles []authz.Role, name, description string,
	opts ...domain.BdbOption,
) (*domain.Bdb, error) {
	ctx, span := tracer.Start(ctx, "BdbService.CreateBdb",
		trace.WithAttributes(attribute.String("user.id", userID.String())),
	)
	defer span.End()
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)

	if err := domain.ValidateBdbName(name); err != nil {
		return nil, err
	}

	maxBdbs := s.limits(roles).MaxBdbs
	bdb := domain.NewBdb(uuid.New(), name, description, userID, opts...)

	if err := s.repo.CreateOwned(ctx, bdb, maxBdbs); err != nil {
		return nil, err
	}

	s.logger.Info().
		Str("bdb_id", bdb.ID.String()).
		Str("owner_id", userID.String()).
		Msg("Created new bdb")

	return bdb, nil
}

// GetBdb returns a bdb if the requesting user is a member.
// Once a bdb is soft-deleted, only the owner can read it; other members
// receive ErrNotBdbMember (matching list visibility).
func (s *BdbService) GetBdb(ctx context.Context, userID, bdbID uuid.UUID) (*domain.Bdb, error) {
	ctx, span := tracer.Start(ctx, "BdbService.GetBdb",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
		),
	)
	defer span.End()
	bdb, err := s.repo.FindByID(ctx, bdbID)
	if err != nil {
		return nil, fmt.Errorf("finding bdb: %w", err)
	}

	if !bdb.IsMember(userID) {
		return nil, domain.ErrNotBdbMember
	}

	if bdb.IsSoftDeleted() {
		if owner := bdb.Owner(); owner == nil || owner.UserID != userID {
			return nil, domain.ErrNotBdbMember
		}
	}

	return bdb, nil
}

// UpdateBdb updates a bdb's name and description. Only the owner can update.
// Rejects with ErrBdbAlreadyDeleted if the bdb is in its purge grace period.
//
// A nil name or description leaves the stored value in place; a supplied one is trimmed, and a supplied name
// must still pass validation.
func (s *BdbService) UpdateBdb(
	ctx context.Context, userID, bdbID uuid.UUID, name, description *string,
) (*domain.Bdb, error) {
	bdb, err := s.repo.FindByID(ctx, bdbID)
	if err != nil {
		return nil, fmt.Errorf("finding bdb: %w", err)
	}

	if owner := bdb.Owner(); owner == nil || owner.UserID != userID {
		return nil, domain.ErrNotBdbMember
	}

	if bdb.IsSoftDeleted() {
		return nil, domain.ErrBdbAlreadyDeleted
	}

	if name != nil {
		trimmed := strings.TrimSpace(*name)
		if err := domain.ValidateBdbName(trimmed); err != nil {
			return nil, err
		}
		bdb.Name = trimmed
	}
	if description != nil {
		bdb.Description = strings.TrimSpace(*description)
	}

	if err := s.repo.Update(ctx, bdb); err != nil {
		return nil, fmt.Errorf("updating bdb: %w", err)
	}
	return bdb, nil
}

// DeleteBdb soft-deletes a bdb by setting its deleted_at timestamp,
// starting the GracePeriod window during which RestoreBdb can revert it.
// Only the owner can delete. Returns ErrBdbAlreadyDeleted if the bdb is
// already soft-deleted.
func (s *BdbService) DeleteBdb(ctx context.Context, userID, bdbID uuid.UUID) error {
	bdb, err := s.repo.FindByID(ctx, bdbID)
	if err != nil {
		return fmt.Errorf("finding bdb: %w", err)
	}

	if owner := bdb.Owner(); owner == nil || owner.UserID != userID {
		return domain.ErrNotBdbMember
	}

	if bdb.IsSoftDeleted() {
		return domain.ErrBdbAlreadyDeleted
	}

	if err := s.repo.SoftDelete(ctx, bdbID, time.Now().UTC()); err != nil {
		return fmt.Errorf("soft-deleting bdb: %w", err)
	}

	s.logger.Info().
		Str("bdb_id", bdbID.String()).
		Str("owner_id", userID.String()).
		Msg("Soft-deleted bdb")

	return nil
}

// RestoreBdb clears the deleted_at on a soft-deleted bdb, cancelling the
// scheduled purge. Only the owner can restore. Returns ErrBdbNotFound if
// the bdb is not currently soft-deleted.
func (s *BdbService) RestoreBdb(ctx context.Context, userID, bdbID uuid.UUID) (*domain.Bdb, error) {
	ctx, span := tracer.Start(ctx, "BdbService.RestoreBdb",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
		),
	)
	defer span.End()

	bdb, err := s.repo.FindByID(ctx, bdbID)
	if err != nil {
		return nil, fmt.Errorf("finding bdb: %w", err)
	}

	if owner := bdb.Owner(); owner == nil || owner.UserID != userID {
		return nil, domain.ErrNotBdbMember
	}

	if !bdb.IsSoftDeleted() {
		return nil, domain.ErrBdbNotFound
	}

	if err := s.repo.Restore(ctx, bdbID); err != nil {
		return nil, fmt.Errorf("restoring bdb: %w", err)
	}

	bdb.DeletedAt = nil

	s.logger.Info().
		Str("bdb_id", bdbID.String()).
		Str("owner_id", userID.String()).
		Msg("Restored bdb")

	return bdb, nil
}

// ListUserBdbs returns all bdbs the user belongs to.
// Soft-deleted bdbs remain visible only to their owner — other members see them drop
// from the list immediately on delete.
func (s *BdbService) ListUserBdbs(ctx context.Context, userID uuid.UUID) ([]*domain.Bdb, error) {
	ctx, span := tracer.Start(ctx, "BdbService.ListUserBdbs",
		trace.WithAttributes(attribute.String("user.id", userID.String())),
	)
	defer span.End()
	bdbs, err := s.repo.ListByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("listing bdbs: %w", err)
	}

	filtered := bdbs[:0]
	for _, b := range bdbs {
		if b.IsSoftDeleted() {
			if owner := b.Owner(); owner == nil || owner.UserID != userID {
				continue
			}
		}
		filtered = append(filtered, b)
	}
	return filtered, nil
}

// AddMember adds a user to a bdb. Only the owner can add members.
// The member limit is resolved from the requesting user's roles (only the owner can add members,
// so the requesting user's roles are the owner's roles).
func (s *BdbService) AddMember(
	ctx context.Context, requestingUserID, bdbID, targetUserID uuid.UUID,
	role authz.BdbRole, roles []authz.Role,
) (*domain.Bdb, error) {
	ctx, span := tracer.Start(ctx, "BdbService.AddMember",
		trace.WithAttributes(
			attribute.String("user.id", requestingUserID.String()),
			attribute.String("bdb.id", bdbID.String()),
		),
	)
	defer span.End()
	if !authz.IsValidBdbRole(role) {
		return nil, fmt.Errorf("%w: %q", domain.ErrInvalidRole, role)
	}

	maxMembers := s.limits(roles).MaxMembersPerBdb
	return s.changeMembers(ctx, bdbID, func(bdb *domain.Bdb) error {
		return bdb.AddMember(requestingUserID, targetUserID, role, maxMembers)
	})
}

// RemoveMember removes a user from a bdb. Only the owner can remove members.
func (s *BdbService) RemoveMember(
	ctx context.Context, requestingUserID, bdbID, targetUserID uuid.UUID,
) (*domain.Bdb, error) {
	ctx, span := tracer.Start(ctx, "BdbService.RemoveMember",
		trace.WithAttributes(
			attribute.String("user.id", requestingUserID.String()),
			attribute.String("bdb.id", bdbID.String()),
		),
	)
	defer span.End()
	return s.changeMembers(ctx, bdbID, func(bdb *domain.Bdb) error {
		return bdb.RemoveMember(requestingUserID, targetUserID)
	})
}

// UpdateMemberRole changes a member's role. Only the owner can change roles.
func (s *BdbService) UpdateMemberRole(
	ctx context.Context, requestingUserID, bdbID, targetUserID uuid.UUID, newRole authz.BdbRole,
) (*domain.Bdb, error) {
	ctx, span := tracer.Start(ctx, "BdbService.UpdateMemberRole",
		trace.WithAttributes(
			attribute.String("user.id", requestingUserID.String()),
			attribute.String("bdb.id", bdbID.String()),
		),
	)
	defer span.End()
	if !authz.IsValidBdbRole(newRole) {
		return nil, fmt.Errorf("invalid role %q", newRole)
	}

	return s.changeMembers(ctx, bdbID, func(bdb *domain.Bdb) error {
		return bdb.UpdateMemberRole(requestingUserID, targetUserID, newRole)
	})
}

// changeMembers runs change on the bdb bdbID and stores the member changes it makes, in one transaction that loads
// the bdb with its members held: concurrent changes to the same bdb run one after the other, so change's checks (the
// requester's permission, the member cap, the owner rules) see what every earlier change stored, and only what
// change itself did is written. A soft-deleted bdb is a domain.ErrBdbAlreadyDeleted.
func (s *BdbService) changeMembers(
	ctx context.Context, bdbID uuid.UUID, change func(*domain.Bdb) error,
) (*domain.Bdb, error) {
	var bdb *domain.Bdb
	err := s.inTx(ctx, func(tx MembershipTx) error {
		var err error
		if bdb, err = tx.Bdbs.FindByIDForUpdate(ctx, bdbID); err != nil {
			return fmt.Errorf("finding bdb: %w", err)
		}
		if bdb.IsSoftDeleted() {
			return domain.ErrBdbAlreadyDeleted
		}

		before := slices.Clone(bdb.Members)
		if err := change(bdb); err != nil {
			return err
		}
		added, changed, removed := domain.DiffMembers(before, bdb.Members)
		if err := tx.Members.ApplyMemberChanges(ctx, bdbID, added, changed, removed); err != nil {
			return fmt.Errorf("saving members: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return bdb, nil
}

// ReorderBdbs rearranges the caller's own view of their bdb list so it follows the given ids. It is a
// personal display preference, not a change to any bdb, so every member of a bdb may reorder it in their
// own list — including viewers — and doing so leaves every other member's arrangement untouched.
// Returns domain.ErrInvalidBdbOrder unless the ids name each of the caller's bdbs exactly once.
func (s *BdbService) ReorderBdbs(ctx context.Context, userID uuid.UUID, orderedBdbIDs []uuid.UUID) error {
	ctx, span := tracer.Start(ctx, "BdbService.ReorderBdbs",
		trace.WithAttributes(attribute.String("user.id", userID.String())),
	)
	defer span.End()

	// Goes through ListUserBdbs, not the repository, so "the caller's bdbs" means exactly the set the
	// caller can see. A bdb soft-deleted by its owner disappears from every other member's list, and
	// they must still be able to arrange the bdbs they do see.
	bdbs, err := s.ListUserBdbs(ctx, userID)
	if err != nil {
		return fmt.Errorf("listing bdbs to reorder: %w", err)
	}
	current := make([]uuid.UUID, len(bdbs))
	for i, b := range bdbs {
		current[i] = b.ID
	}

	if err := domain.ValidateBdbOrder(current, orderedBdbIDs); err != nil {
		return err
	}

	if err := s.repo.ReorderForUser(ctx, userID, orderedBdbIDs); err != nil {
		return fmt.Errorf("reordering bdbs: %w", err)
	}
	return nil
}

// ResolveMemberProfiles resolves profile data for all members of a bdb in
// a single batched call. Returns a map of user ID to profile. A failure of
// the underlying lookup is logged and an empty map is returned (the members
// will appear without profile data).
func (s *BdbService) ResolveMemberProfiles(ctx context.Context, b *domain.Bdb) map[uuid.UUID]*apptypes.UserProfile {
	if len(b.Members) == 0 {
		return map[uuid.UUID]*apptypes.UserProfile{}
	}
	ids := make([]uuid.UUID, len(b.Members))
	for i, m := range b.Members {
		ids[i] = m.UserID
	}
	profiles, err := s.authApp.GetUserProfiles(ctx, ids)
	if err != nil {
		s.logger.Warn().Err(err).Str("bdb_id", b.ID.String()).Msg("failed to resolve member profiles")
		return map[uuid.UUID]*apptypes.UserProfile{}
	}
	return profiles
}

// ResolveOwnerProfiles resolves the owner profile of each given bdb in one batched lookup, keyed by bdb ID.
// Bdbs without an owner member or without a resolvable profile are absent from the map; a failure of the
// underlying lookup is logged and an empty map is returned.
func (s *BdbService) ResolveOwnerProfiles(ctx context.Context, bdbs []*domain.Bdb) map[uuid.UUID]*apptypes.UserProfile {
	ownerIDs := make([]uuid.UUID, 0, len(bdbs))
	ownerByBdb := make(map[uuid.UUID]uuid.UUID, len(bdbs))
	for _, b := range bdbs {
		if owner := b.Owner(); owner != nil {
			ownerIDs = append(ownerIDs, owner.UserID)
			ownerByBdb[b.ID] = owner.UserID
		}
	}
	if len(ownerIDs) == 0 {
		return map[uuid.UUID]*apptypes.UserProfile{}
	}

	profiles, err := s.authApp.GetUserProfiles(ctx, ownerIDs)
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to resolve owner profiles")
		return map[uuid.UUID]*apptypes.UserProfile{}
	}

	out := make(map[uuid.UUID]*apptypes.UserProfile, len(ownerByBdb))
	for bdbID, ownerID := range ownerByBdb {
		if p, ok := profiles[ownerID]; ok {
			out[bdbID] = p
		}
	}
	return out
}
