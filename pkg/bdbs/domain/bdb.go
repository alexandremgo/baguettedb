// Package domain holds the bdbs model: the bdb aggregate (a user's personal database, with its owner and members and
// their roles) and the single-use invites admitting new members. Its rules need no I/O: permission checks, name and
// ordering validation, member and bdb caps, the soft-delete grace period, and the errors reporting a broken rule.
// Domain types carry no serialization tags, and the package imports no other app.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

const maxBdbNameLength = 100

// GracePeriod is the window between a bdb being soft-deleted and the
// permanent purge of its row and per-bdb database file.
const GracePeriod = 7 * 24 * time.Hour

// Bdb represents a collaborative group that owns a single database.
// It is the aggregate root: it contains its members and enforces all
// membership invariants (only owner can manage members, etc.).
type Bdb struct {
	ID          uuid.UUID
	Name        string
	Description string
	Members     []Member
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
	// StarterKey and StarterVersion record which starter (and which version of it) a bdb was created from.
	// Both nil for bdbs created blank. Set once at creation via WithStarterProvenance and never mutated.
	StarterKey     *string
	StarterVersion *int
}

// IsSoftDeleted reports whether the bdb has been soft-deleted and is awaiting purge.
func (b *Bdb) IsSoftDeleted() bool {
	return b.DeletedAt != nil
}

// PurgeAt returns the time after which a soft-deleted bdb becomes eligible
// for permanent deletion, or nil if the bdb is not soft-deleted.
func (b *Bdb) PurgeAt() *time.Time {
	if b.DeletedAt == nil {
		return nil
	}
	t := b.DeletedAt.Add(GracePeriod)
	return &t
}

// BdbOption customizes a Bdb at construction time.
type BdbOption func(*Bdb)

// WithStarterProvenance records that the bdb was created from the given starter and version.
func WithStarterProvenance(starterKey string, starterVersion int) BdbOption {
	return func(b *Bdb) {
		b.StarterKey = &starterKey
		b.StarterVersion = &starterVersion
	}
}

// NewBdb creates a new bdb with the given user as the owner.
func NewBdb(id uuid.UUID, name, description string, ownerUserID uuid.UUID, opts ...BdbOption) *Bdb {
	now := time.Now().UTC()
	bdb := &Bdb{
		ID:          id,
		Name:        name,
		Description: description,
		Members: []Member{
			{
				BdbID:     id,
				UserID:    ownerUserID,
				Role:      authz.BdbRoleOwner,
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	for _, opt := range opts {
		opt(bdb)
	}
	return bdb
}

// Owner returns the owner member of the bdb.
func (b *Bdb) Owner() *Member {
	for i := range b.Members {
		if b.Members[i].Role == authz.BdbRoleOwner {
			return &b.Members[i]
		}
	}
	return nil
}

// FindMember returns the member with the given user ID, or nil if not found.
func (b *Bdb) FindMember(userID uuid.UUID) *Member {
	for i := range b.Members {
		if b.Members[i].UserID == userID {
			return &b.Members[i]
		}
	}
	return nil
}

// IsMember checks whether the given user is a member of the bdb.
func (b *Bdb) IsMember(userID uuid.UUID) bool {
	return b.FindMember(userID) != nil
}

// AddMember adds a new member to the bdb.
// Requires bdb:manage_members permission. The owner role cannot be assigned.
// maxMembers enforces the member limit (0 means unlimited).
func (b *Bdb) AddMember(requestingUserID, targetUserID uuid.UUID, role authz.BdbRole, maxMembers int) error {
	if err := b.requirePermission(requestingUserID, authz.BdbPermManageMembers); err != nil {
		return err
	}
	return b.AdmitMember(targetUserID, role, maxMembers)
}

// AdmitMember adds userID to the bdb with role, without checking who asks: the caller has already established the
// right to join, as an accepted invite does. The owner role cannot be given this way (ErrCannotChangeOwnerRole), a
// member cannot join twice (ErrAlreadyMember), and the bdb cannot grow past maxMembers (MemberLimitReachedError, 0
// meaning no cap).
func (b *Bdb) AdmitMember(userID uuid.UUID, role authz.BdbRole, maxMembers int) error {
	if role == authz.BdbRoleOwner {
		return ErrCannotChangeOwnerRole
	}
	if b.IsMember(userID) {
		return ErrAlreadyMember
	}
	if CapReached(len(b.Members), maxMembers) {
		return MemberLimitReachedError{Max: maxMembers}
	}

	now := time.Now().UTC()
	b.Members = append(b.Members, Member{
		BdbID:     b.ID,
		UserID:    userID,
		Role:      role,
		CreatedAt: now,
		UpdatedAt: now,
	})
	return nil
}

// RemoveMember removes a member from the bdb.
// Requires bdb:manage_members permission. The owner cannot be removed.
func (b *Bdb) RemoveMember(requestingUserID, targetUserID uuid.UUID) error {
	if err := b.requirePermission(requestingUserID, authz.BdbPermManageMembers); err != nil {
		return err
	}

	target := b.FindMember(targetUserID)
	if target == nil {
		return ErrMemberNotFound
	}
	if target.Role == authz.BdbRoleOwner {
		return ErrCannotRemoveOwner
	}

	filtered := make([]Member, 0, len(b.Members)-1)
	for _, m := range b.Members {
		if m.UserID != targetUserID {
			filtered = append(filtered, m)
		}
	}
	b.Members = filtered
	return nil
}

// UpdateMemberRole changes a member's role.
// Requires bdb:manage_members permission. The owner's own role cannot be changed,
// and the owner role cannot be assigned.
func (b *Bdb) UpdateMemberRole(requestingUserID, targetUserID uuid.UUID, newRole authz.BdbRole) error {
	if err := b.requirePermission(requestingUserID, authz.BdbPermManageMembers); err != nil {
		return err
	}
	if newRole == authz.BdbRoleOwner {
		return ErrCannotChangeOwnerRole
	}

	target := b.FindMember(targetUserID)
	if target == nil {
		return ErrMemberNotFound
	}
	if target.Role == authz.BdbRoleOwner {
		return ErrCannotChangeOwnerRole
	}

	target.Role = newRole
	target.UpdatedAt = time.Now().UTC()
	return nil
}

// ValidateBdbName checks that the name is non-empty and within the maximum length.
func ValidateBdbName(name string) error {
	if name == "" {
		return ErrBdbNameEmpty
	}
	if len(name) > maxBdbNameLength {
		return ErrBdbNameTooLong
	}
	return nil
}

// SameBdbName reports whether a and b name the same bdb among one owner's: equal once trimmed, whatever their case.
func SameBdbName(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// ValidateBdbOrder checks that requested is a valid arrangement of current: the same set of ids, each
// appearing exactly once. The order itself is what the user is choosing, so it is never wrong.
// Returns ErrInvalidBdbOrder otherwise.
func ValidateBdbOrder(current, requested []uuid.UUID) error {
	remaining := make(map[uuid.UUID]struct{}, len(current))

	for _, c := range current {
		remaining[c] = struct{}{}
	}

	for _, r := range requested {
		if _, ok := remaining[r]; !ok {
			return ErrInvalidBdbOrder
		}
		delete(remaining, r)
	}

	if len(remaining) > 0 {
		return ErrInvalidBdbOrder
	}

	return nil
}

// requirePermission checks that the given user is a member and has the specified bdb permission.
func (b *Bdb) requirePermission(userID uuid.UUID, perm authz.BdbPermission) error {
	member := b.FindMember(userID)
	if member == nil {
		return ErrNotBdbMember
	}
	if !authz.BdbRoleHasPermission(member.Role, perm) {
		return ErrNotBdbMember
	}
	return nil
}
