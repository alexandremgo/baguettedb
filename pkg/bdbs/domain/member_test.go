package domain

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// TestDiffMembers checks that DiffMembers sorts each member into added, changed or removed by user, carries the
// wanted role into changed, leaves a member found in both with the same role out of all three, and keeps wanted's
// order for added and changed and stored's order for removed.
func TestDiffMembers(t *testing.T) {
	owner, ann, bob, cid := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	member := func(user uuid.UUID, role authz.BdbRole) Member { return Member{UserID: user, Role: role} }

	tests := []struct {
		name                                string
		stored, wanted                      []Member
		wantAdded, wantChanged, wantRemoved []Member
	}{
		{
			name:   "nothing on either side",
			stored: nil, wanted: nil,
		},
		{
			name:      "every wanted member is new",
			wanted:    []Member{member(owner, authz.BdbRoleOwner), member(ann, authz.BdbRoleEditor)},
			wantAdded: []Member{member(owner, authz.BdbRoleOwner), member(ann, authz.BdbRoleEditor)},
		},
		{
			name:        "every stored member is gone",
			stored:      []Member{member(owner, authz.BdbRoleOwner), member(ann, authz.BdbRoleViewer)},
			wantRemoved: []Member{member(owner, authz.BdbRoleOwner), member(ann, authz.BdbRoleViewer)},
		},
		{
			name:   "same members, same roles",
			stored: []Member{member(owner, authz.BdbRoleOwner), member(ann, authz.BdbRoleViewer)},
			wanted: []Member{member(ann, authz.BdbRoleViewer), member(owner, authz.BdbRoleOwner)},
		},
		{
			name: "one of each, in each side's order",
			stored: []Member{
				member(owner, authz.BdbRoleOwner), member(bob, authz.BdbRoleViewer), member(ann, authz.BdbRoleViewer),
				member(cid, authz.BdbRoleEditor),
			},
			wanted: []Member{
				member(cid, authz.BdbRoleEditor), member(ann, authz.BdbRoleEditor), member(owner, authz.BdbRoleOwner),
			},
			wantChanged: []Member{member(ann, authz.BdbRoleEditor)},
			wantRemoved: []Member{member(bob, authz.BdbRoleViewer)},
		},
		{
			name:   "added and changed keep wanted's order",
			stored: []Member{member(owner, authz.BdbRoleOwner), member(ann, authz.BdbRoleViewer)},
			wanted: []Member{
				member(cid, authz.BdbRoleViewer), member(ann, authz.BdbRoleEditor), member(bob, authz.BdbRoleViewer),
				member(owner, authz.BdbRoleOwner),
			},
			wantAdded:   []Member{member(cid, authz.BdbRoleViewer), member(bob, authz.BdbRoleViewer)},
			wantChanged: []Member{member(ann, authz.BdbRoleEditor)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			added, changed, removed := DiffMembers(tt.stored, tt.wanted)
			if !slices.Equal(added, tt.wantAdded) {
				t.Errorf("added = %v, want %v", added, tt.wantAdded)
			}
			if !slices.Equal(changed, tt.wantChanged) {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
			if !slices.Equal(removed, tt.wantRemoved) {
				t.Errorf("removed = %v, want %v", removed, tt.wantRemoved)
			}
		})
	}
}
