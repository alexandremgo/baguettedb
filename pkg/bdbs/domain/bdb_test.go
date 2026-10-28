package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

func TestAddMember_MemberLimit(t *testing.T) {
	ownerID := uuid.New()

	tests := []struct {
		name          string
		existingCount int
		maxMembers    int
		wantErr       bool
		wantMax       int
	}{
		{"below limit", 3, 5, false, 0},
		{"one below limit", 4, 5, false, 0},
		{"at limit", 5, 5, true, 5},
		{"above limit", 6, 5, true, 5},
		{"unlimited", 50, 0, false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bdb := NewBdb(uuid.New(), "test", "", ownerID)

			// Add existing members (owner is already member 1).
			for i := 1; i < tt.existingCount; i++ {
				err := bdb.AddMember(ownerID, uuid.New(), authz.BdbRoleEditor, 0)
				if err != nil {
					t.Fatalf("adding member %d: %v", i, err)
				}
			}

			err := bdb.AddMember(ownerID, uuid.New(), authz.BdbRoleViewer, tt.maxMembers)

			if tt.wantErr {
				limitErr, ok := errors.AsType[MemberLimitReachedError](err)
				if !ok {
					t.Fatalf("expected MemberLimitReachedError, got %v", err)
				}
				if limitErr.Max != tt.wantMax {
					t.Errorf("expected Max=%d, got Max=%d", tt.wantMax, limitErr.Max)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
