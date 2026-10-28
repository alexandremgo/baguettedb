package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
)

// purgeFakeRepo tracks ListPurgeable returns and HardDelete invocations. All other bdbRepository methods return
// zero values since PurgeExpired only touches these two.
type purgeFakeRepo struct {
	purgeable    []uuid.UUID
	hardDeleted  []uuid.UUID
	hardDeleteFn func(uuid.UUID) error
}

func (r *purgeFakeRepo) CreateOwned(_ context.Context, _ *domain.Bdb, _ int) error { return nil }
func (r *purgeFakeRepo) FindByID(_ context.Context, _ uuid.UUID) (*domain.Bdb, error) {
	return nil, nil
}
func (r *purgeFakeRepo) FindMember(_ context.Context, _, _ uuid.UUID) (*domain.Member, error) {
	return nil, nil
}
func (r *purgeFakeRepo) Update(_ context.Context, _ *domain.Bdb) error { return nil }
func (r *purgeFakeRepo) SoftDelete(_ context.Context, _ uuid.UUID, _ time.Time) error {
	return nil
}
func (r *purgeFakeRepo) Restore(_ context.Context, _ uuid.UUID) error { return nil }
func (r *purgeFakeRepo) HardDelete(_ context.Context, id uuid.UUID) error {
	r.hardDeleted = append(r.hardDeleted, id)
	if r.hardDeleteFn != nil {
		return r.hardDeleteFn(id)
	}
	return nil
}
func (r *purgeFakeRepo) ListPurgeable(_ context.Context, _ time.Time) ([]uuid.UUID, error) {
	return r.purgeable, nil
}
func (r *purgeFakeRepo) ListByUser(_ context.Context, _ uuid.UUID) ([]*domain.Bdb, error) {
	return nil, nil
}
func (r *purgeFakeRepo) ListOwnedIDsByUser(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (r *purgeFakeRepo) ReorderForUser(_ context.Context, _ uuid.UUID, _ []uuid.UUID) error {
	return nil
}

// purgeFakeDBApp records DeleteDatabase calls and optionally returns errors.
type purgeFakeDBApp struct {
	deleted  []uuid.UUID
	deleteFn func(uuid.UUID) error
}

func (a *purgeFakeDBApp) ListTables(_ context.Context, _, _ uuid.UUID) ([]apptypes.TableInfo, error) {
	return nil, nil
}
func (a *purgeFakeDBApp) ListTablesWithSchemas(
	_ context.Context, _, _ uuid.UUID,
) ([]apptypes.TableSchema, error) {
	return nil, nil
}
func (a *purgeFakeDBApp) ListSkills(_ context.Context, _, _ uuid.UUID) ([]apptypes.Skill, error) {
	return nil, nil
}
func (a *purgeFakeDBApp) ListFormulas(_ context.Context, _, _ uuid.UUID) ([]apptypes.Formula, error) {
	return nil, nil
}
func (a *purgeFakeDBApp) DeleteDatabase(_ context.Context, bdbID uuid.UUID) error {
	a.deleted = append(a.deleted, bdbID)
	if a.deleteFn != nil {
		return a.deleteFn(bdbID)
	}
	return nil
}

func TestPurgeExpired_HappyPath(t *testing.T) {
	idA := uuid.New()
	idB := uuid.New()
	repo := &purgeFakeRepo{purgeable: []uuid.UUID{idA, idB}}
	dbApp := &purgeFakeDBApp{}
	svc := NewBdbService(repo, nil, &mockAuthApp{}, nil, zerolog.Nop())
	svc.SetDatabasesApp(dbApp)

	stats, err := svc.PurgeExpired(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.Scanned != 2 || stats.Purged != 2 || stats.Failed != 0 {
		t.Errorf("stats = %+v, want {Scanned:2 Purged:2 Failed:0}", stats)
	}
	if len(dbApp.deleted) != 2 {
		t.Errorf("DeleteDatabase called %d times, want 2", len(dbApp.deleted))
	}
	if len(repo.hardDeleted) != 2 {
		t.Errorf("HardDelete called %d times, want 2", len(repo.hardDeleted))
	}
}

func TestPurgeExpired_DBDeleteFails_RowKept(t *testing.T) {
	badID := uuid.New()
	goodID := uuid.New()
	repo := &purgeFakeRepo{purgeable: []uuid.UUID{badID, goodID}}
	dbApp := &purgeFakeDBApp{
		deleteFn: func(id uuid.UUID) error {
			if id == badID {
				return errors.New("transient database delete failure")
			}
			return nil
		},
	}
	svc := NewBdbService(repo, nil, &mockAuthApp{}, nil, zerolog.Nop())
	svc.SetDatabasesApp(dbApp)

	stats, err := svc.PurgeExpired(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.Scanned != 2 || stats.Purged != 1 || stats.Failed != 1 {
		t.Errorf("stats = %+v, want {Scanned:2 Purged:1 Failed:1}", stats)
	}
	if len(repo.hardDeleted) != 1 || repo.hardDeleted[0] != goodID {
		t.Errorf("HardDelete = %v, want only [%s] (the row whose DB delete succeeded)",
			repo.hardDeleted, goodID)
	}
}

func TestPurgeExpired_NoDatabasesApp(t *testing.T) {
	repo := &purgeFakeRepo{purgeable: []uuid.UUID{uuid.New()}}
	svc := NewBdbService(repo, nil, &mockAuthApp{}, nil, zerolog.Nop())
	// SetDatabasesApp NOT called — emulates pre-wiring state.

	stats, err := svc.PurgeExpired(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.Scanned != 0 || stats.Purged != 0 || stats.Failed != 0 {
		t.Errorf("stats = %+v, want all zeros (sweep skipped)", stats)
	}
}

func (r *purgeFakeRepo) CountActive(context.Context) (int64, error) {
	return 0, nil
}
