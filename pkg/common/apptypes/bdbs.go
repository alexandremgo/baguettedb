package apptypes

import (
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// Bdb is a bdb as one of its members sees it: MyRole is that member's role, and each member carries the profile
// data of their account when it resolved. DeletedAt and PurgeAt are set only when the bdb is soft-deleted; PurgeAt is
// the deletion time plus the configured grace period. Tables is set only when the bdb's schema was asked for.
type Bdb struct {
	ID          uuid.UUID     `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	MyRole      authz.BdbRole `json:"my_role,omitempty"`
	// Owner is the owner member with profile data, so clients can attribute a shared bdb to its owner without
	// scanning Members. Nil only when the bdb has no owner member.
	Owner     *BdbMember     `json:"owner,omitempty"`
	Members   []BdbMember    `json:"members"`
	Tables    *[]TableSchema `json:"tables,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt *time.Time     `json:"deleted_at,omitempty"`
	PurgeAt   *time.Time     `json:"purge_at,omitempty"`
}

// BdbMember is a member of a bdb. Email and the names are empty when the member's profile did not resolve.
type BdbMember struct {
	UserID    uuid.UUID     `json:"user_id"`
	Email     string        `json:"email,omitempty"`
	FirstName string        `json:"first_name,omitempty"`
	LastName  string        `json:"last_name,omitempty"`
	Role      authz.BdbRole `json:"role"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}
