package domain

import (
	"crypto/rand"
	"encoding/base64"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

const inviteTokenBytes = 32

// InviteStatus represents the lifecycle state of a bdb invite.
type InviteStatus string

// The states an invite moves through.
const (
	InviteStatusPending  InviteStatus = "pending"
	InviteStatusAccepted InviteStatus = "accepted"
	InviteStatusRevoked  InviteStatus = "revoked"
)

// Invite represents a single-use, time-limited invitation to join a bdb
// with a specific role. It is a standalone entity with its own lifecycle,
// addressable by its token.
type Invite struct {
	ID         uuid.UUID
	BdbID      uuid.UUID
	Token      string
	Role       authz.BdbRole
	Status     InviteStatus
	CreatedBy  uuid.UUID
	ExpiresAt  time.Time
	AcceptedBy uuid.UUID
	AcceptedAt time.Time
	CreatedAt  time.Time
}

// NewInvite creates a pending invite for the given bdb and role.
// Generates a cryptographically random URL-safe token. The role must not be
// owner, and expiresAt must be in the future.
func NewInvite(bdbID, createdBy uuid.UUID, role authz.BdbRole, expiresAt time.Time) (*Invite, error) {
	if role == authz.BdbRoleOwner {
		return nil, ErrCannotChangeOwnerRole
	}
	if !authz.IsValidBdbRole(role) {
		return nil, ErrInvalidRole
	}
	if !expiresAt.After(time.Now().UTC()) {
		return nil, ErrInviteExpired
	}

	token, err := GenerateInviteToken()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	return &Invite{
		ID:        uuid.New(),
		BdbID:     bdbID,
		Token:     token,
		Role:      role,
		Status:    InviteStatusPending,
		CreatedBy: createdBy,
		ExpiresAt: expiresAt.UTC(),
		CreatedAt: now,
	}, nil
}

// IsUsable returns true if the invite is pending and has not expired.
func (i *Invite) IsUsable() bool {
	return i.Status == InviteStatusPending && i.ExpiresAt.After(time.Now().UTC())
}

// Accept marks the invite as accepted by the given user. Returns an error if
// the invite is expired, already used, or revoked.
func (i *Invite) Accept(userID uuid.UUID) error {
	if i.Status == InviteStatusAccepted {
		return ErrInviteAlreadyUsed
	}
	if i.Status == InviteStatusRevoked {
		return ErrInviteRevoked
	}
	if !i.ExpiresAt.After(time.Now().UTC()) {
		return ErrInviteExpired
	}

	now := time.Now().UTC()
	i.Status = InviteStatusAccepted
	i.AcceptedBy = userID
	i.AcceptedAt = now
	return nil
}

// Revoke marks a pending invite as revoked so it can no longer be accepted.
func (i *Invite) Revoke() error {
	if i.Status == InviteStatusAccepted {
		return ErrInviteAlreadyUsed
	}
	if i.Status == InviteStatusRevoked {
		return ErrInviteRevoked
	}

	i.Status = InviteStatusRevoked
	return nil
}

// GenerateInviteToken produces a cryptographically random URL-safe token
// (32 bytes, base64url-encoded without padding).
func GenerateInviteToken() (string, error) {
	b := make([]byte, inviteTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
