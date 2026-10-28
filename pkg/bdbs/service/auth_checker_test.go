package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// mockBdbReader is a canned-response bdbMembershipReader double.
type mockBdbReader struct {
	member *domain.Member
	err    error
}

func (m *mockBdbReader) FindByID(_ context.Context, _ uuid.UUID) (*domain.Bdb, error) {
	return nil, m.err
}

func (m *mockBdbReader) FindMember(_ context.Context, _, _ uuid.UUID) (*domain.Member, error) {
	return m.member, m.err
}

func (m *mockBdbReader) ListOwnedIDsByUser(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, m.err
}

func TestBdbAuthChecker_CheckMemberPermission(t *testing.T) {
	bdbID := uuid.New()
	userID := uuid.New()

	tests := []struct {
		name    string
		role    authz.BdbRole
		repoErr error
		perm    authz.DatabasePermission
		want    bool
	}{
		{"owner can read", authz.BdbRoleOwner, nil, authz.DBPermRead, true},
		{"owner can write", authz.BdbRoleOwner, nil, authz.DBPermWrite, true},
		{"owner can manage schema", authz.BdbRoleOwner, nil, authz.DBPermManageSchema, true},
		{"owner can export", authz.BdbRoleOwner, nil, authz.DBPermExport, true},
		{"editor can read", authz.BdbRoleEditor, nil, authz.DBPermRead, true},
		{"editor can write", authz.BdbRoleEditor, nil, authz.DBPermWrite, true},
		{"editor cannot manage schema", authz.BdbRoleEditor, nil, authz.DBPermManageSchema, false},
		{"editor cannot export", authz.BdbRoleEditor, nil, authz.DBPermExport, false},
		{"viewer can read", authz.BdbRoleViewer, nil, authz.DBPermRead, true},
		{"viewer cannot write", authz.BdbRoleViewer, nil, authz.DBPermWrite, false},
		{"viewer cannot manage schema", authz.BdbRoleViewer, nil, authz.DBPermManageSchema, false},
		{"viewer cannot export", authz.BdbRoleViewer, nil, authz.DBPermExport, false},
		{"non-member denied", "", domain.ErrMemberNotFound, authz.DBPermRead, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &mockBdbReader{}
			if tt.repoErr != nil {
				repo.err = tt.repoErr
			} else {
				repo.member = &domain.Member{
					BdbID:  bdbID,
					UserID: userID,
					Role:   tt.role,
				}
			}

			checker := NewBdbAuthChecker(repo, nil)
			got, err := checker.CheckMemberPermission(context.Background(), userID, bdbID, tt.perm)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("CheckMemberPermission(%s, %s) = %v, want %v", tt.role, tt.perm, got, tt.want)
			}
		})
	}
}

func TestBdbAuthChecker_CheckMemberPermission_RepoError(t *testing.T) {
	repoErr := errors.New("database error")
	repo := &mockBdbReader{err: repoErr}

	checker := NewBdbAuthChecker(repo, nil)
	_, err := checker.CheckMemberPermission(context.Background(), uuid.New(), uuid.New(), authz.DBPermRead)
	if !errors.Is(err, repoErr) {
		t.Errorf("expected repo error, got: %v", err)
	}
}
