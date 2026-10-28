package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// mockUserRepo implements UserRepository for tests.
type mockUserRepo struct {
	createErr         error
	findByEmailUser   *domain.User
	findByEmailErr    error
	findByIDUser      *domain.User
	findByIDErr       error
	findByIDsProfiles []*domain.UserProfile
	findByIDsErr      error
	findByIDsCalled   bool
	findByIDsArg      []uuid.UUID
	updatePassErr     error
	users             []*domain.User
	totalUsers        int
	listErr           error
	existsResult      bool
	existsErr         error
}

func (m *mockUserRepo) Create(_ context.Context, _ *domain.User) error {
	return m.createErr
}

func (m *mockUserRepo) FindByEmail(_ context.Context, _ string) (*domain.User, error) {
	if m.findByEmailErr != nil {
		return nil, m.findByEmailErr
	}
	return m.findByEmailUser, nil
}

func (m *mockUserRepo) FindByID(_ context.Context, _ uuid.UUID) (*domain.User, error) {
	if m.findByIDErr != nil {
		return nil, m.findByIDErr
	}
	return m.findByIDUser, nil
}

func (m *mockUserRepo) FindByIDs(_ context.Context, ids []uuid.UUID) ([]*domain.UserProfile, error) {
	m.findByIDsCalled = true
	m.findByIDsArg = ids
	if m.findByIDsErr != nil {
		return nil, m.findByIDsErr
	}
	return m.findByIDsProfiles, nil
}

func (m *mockUserRepo) UpdatePassword(_ context.Context, _ uuid.UUID, _ string) error {
	return m.updatePassErr
}

// UpdateIdentity accepts every update; no test reads it back.
func (m *mockUserRepo) UpdateIdentity(_ context.Context, _ uuid.UUID, _, _, _ string) error {
	return nil
}

func (m *mockUserRepo) MarkEmailVerified(_ context.Context, _ uuid.UUID, _ time.Time) error {
	return nil
}

func (m *mockUserRepo) ListUsers(_ context.Context, _ string, _, _ int) ([]*domain.User, int, error) {
	if m.listErr != nil {
		return nil, 0, m.listErr
	}
	return m.users, m.totalUsers, nil
}

func (m *mockUserRepo) Exists(_ context.Context) (bool, error) {
	return m.existsResult, m.existsErr
}

// mockRoleRepo implements RoleRepository for tests.
type mockRoleRepo struct {
	assignErr   error
	removeErr   error
	roles       []authz.Role
	getRolesErr error
}

func (m *mockRoleRepo) AssignRole(_ context.Context, _ uuid.UUID, _ authz.Role) error {
	return m.assignErr
}

func (m *mockRoleRepo) RemoveRole(_ context.Context, _ uuid.UUID, _ authz.Role) error {
	return m.removeErr
}

func (m *mockRoleRepo) GetRoles(_ context.Context, _ uuid.UUID) ([]authz.Role, error) {
	if m.getRolesErr != nil {
		return nil, m.getRolesErr
	}
	return m.roles, nil
}

// runWith returns a RunInTx handing every transaction tx, without opening one.
func runWith[T any](tx T) RunInTx[T] {
	return func(_ context.Context, fn func(T) error) error {
		return fn(tx)
	}
}

// mockSessionRepo revokes sessions for tests, recording whose sessions it revoked.
type mockSessionRepo struct {
	revokedFor []uuid.UUID
}

// RevokeSessions records userID.
func (m *mockSessionRepo) RevokeSessions(_ context.Context, userID uuid.UUID, _ time.Time) error {
	m.revokedFor = append(m.revokedFor, userID)
	return nil
}
