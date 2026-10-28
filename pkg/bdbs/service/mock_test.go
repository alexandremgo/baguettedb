package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// mockBdbRepo implements bdbRepository for unit tests.
type mockBdbRepo struct {
	bdb          *domain.Bdb
	member       *domain.Member
	err          error
	reorderedIDs []uuid.UUID
	gotMaxOwned  int
}

func (m *mockBdbRepo) CreateOwned(_ context.Context, _ *domain.Bdb, maxOwned int) error {
	m.gotMaxOwned = maxOwned
	return m.err
}
func (m *mockBdbRepo) FindByID(_ context.Context, _ uuid.UUID) (*domain.Bdb, error) {
	return m.bdb, m.err
}
func (m *mockBdbRepo) FindMember(_ context.Context, _, _ uuid.UUID) (*domain.Member, error) {
	return m.member, m.err
}
func (m *mockBdbRepo) Update(_ context.Context, _ *domain.Bdb) error { return m.err }
func (m *mockBdbRepo) SoftDelete(_ context.Context, _ uuid.UUID, _ time.Time) error {
	return m.err
}
func (m *mockBdbRepo) Restore(_ context.Context, _ uuid.UUID) error    { return m.err }
func (m *mockBdbRepo) HardDelete(_ context.Context, _ uuid.UUID) error { return m.err }
func (m *mockBdbRepo) ListPurgeable(_ context.Context, _ time.Time) ([]uuid.UUID, error) {
	return nil, m.err
}
func (m *mockBdbRepo) ListByUser(_ context.Context, _ uuid.UUID) ([]*domain.Bdb, error) {
	return nil, m.err
}
func (m *mockBdbRepo) ListOwnedIDsByUser(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, m.err
}
func (m *mockBdbRepo) ReorderForUser(_ context.Context, _ uuid.UUID, ids []uuid.UUID) error {
	m.reorderedIDs = ids
	return m.err
}

type mockAuthApp struct{}

func (m *mockAuthApp) GetUserRoles(_ context.Context, _ uuid.UUID) ([]authz.Role, error) {
	return []authz.Role{"user"}, nil
}

func (m *mockAuthApp) GetUserProfiles(
	_ context.Context, userIDs []uuid.UUID,
) (map[uuid.UUID]*apptypes.UserProfile, error) {
	out := make(map[uuid.UUID]*apptypes.UserProfile, len(userIDs))
	for _, id := range userIDs {
		out[id] = &apptypes.UserProfile{Email: "test@test.com"}
	}
	return out, nil
}

func (m *mockBdbRepo) CountActive(context.Context) (int64, error) {
	return 0, nil
}
