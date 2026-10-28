package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// Member represents a user's membership in a bdb with a specific role.
type Member struct {
	BdbID     uuid.UUID
	UserID    uuid.UUID
	Role      authz.BdbRole
	CreatedAt time.Time
	UpdatedAt time.Time
}

// DiffMembers compares the members storage holds for a bdb with the ones it wants, matching them by user. It returns
// the wanted members storage lacks (added), the wanted members storage holds with another role (changed, carrying the
// wanted role), and the stored members no longer wanted (removed). added and changed keep wanted's order, removed
// keeps stored's; a member found in both with the same role appears in none of them.
func DiffMembers(stored, wanted []Member) (added, changed, removed []Member) {
	storedRoles := make(map[uuid.UUID]authz.BdbRole, len(stored))
	for _, m := range stored {
		storedRoles[m.UserID] = m.Role
	}

	wantedUsers := make(map[uuid.UUID]struct{}, len(wanted))
	for _, m := range wanted {
		wantedUsers[m.UserID] = struct{}{}
		role, held := storedRoles[m.UserID]
		switch {
		case !held:
			added = append(added, m)
		case role != m.Role:
			changed = append(changed, m)
		}
	}

	for _, m := range stored {
		if _, ok := wantedUsers[m.UserID]; !ok {
			removed = append(removed, m)
		}
	}

	return added, changed, removed
}
