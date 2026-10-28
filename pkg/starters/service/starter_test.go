package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
)

// The happy paths and real failure-cleanup paths are covered against the real stores in
// pkg/starters/integrationtest; the fakes here exist only for fault injection the real services can't
// produce on demand (a failing bdb delete during cleanup).

type fakeBdbsApp struct {
	deleteErr     error
	deletedBdbIDs []uuid.UUID
}

func (f *fakeBdbsApp) CreateBdb(_ context.Context, _ uuid.UUID, _ []authz.Role, _, _ string) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (f *fakeBdbsApp) CreateBdbWithProvenance(
	_ context.Context, _ uuid.UUID, _ []authz.Role, _, _, _ string, _ int,
) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (f *fakeBdbsApp) GetBdb(_ context.Context, _, bdbID uuid.UUID) (apptypes.Bdb, error) {
	return apptypes.Bdb{ID: bdbID}, nil
}

func (f *fakeBdbsApp) DeleteBdb(_ context.Context, _, bdbID uuid.UUID) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletedBdbIDs = append(f.deletedBdbIDs, bdbID)
	return nil
}

type fakeDatabasesApp struct {
	skillErr         error
	deletedDatabases []uuid.UUID
}

func (f *fakeDatabasesApp) ExecuteSchema(_ context.Context, _, _ uuid.UUID, _ []string) error {
	return nil
}

func (f *fakeDatabasesApp) InsertRows(_ context.Context, _, _ uuid.UUID, _ domain.StarterSeed) error {
	return nil
}

func (f *fakeDatabasesApp) CreateFormula(
	_ context.Context, _, _ uuid.UUID, _ domain.StarterFormula, _ *uuid.UUID,
) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (f *fakeDatabasesApp) UpdateFormulaDisplay(
	_ context.Context, _, _ uuid.UUID, _ string, _ domain.StarterFormulaDisplay,
) error {
	return nil
}

func (f *fakeDatabasesApp) CreateSkill(_ context.Context, _, _ uuid.UUID, _ domain.StarterSkill) error {
	return f.skillErr
}

func (f *fakeDatabasesApp) DeleteDatabase(_ context.Context, bdbID uuid.UUID) error {
	f.deletedDatabases = append(f.deletedDatabases, bdbID)
	return nil
}

// TestCreateFromStarterLeavesPartialWhenCleanupFails verifies the leave-partial fallback: when the bdb delete
// itself fails during cleanup, the apply error is still returned and the database delete is skipped.
func TestCreateFromStarterLeavesPartialWhenCleanupFails(t *testing.T) {
	m := domain.StarterManifest{
		Key:     "fixture",
		Version: 1,
		Name:    "Fixture starter",
		Schema:  []string{"CREATE TABLE a (id INTEGER PRIMARY KEY)"},
		Skills:  []domain.StarterSkill{{Name: "a-orientation", Description: "How to use a.", Body: "Keep it simple."}},
	}
	bdbs := &fakeBdbsApp{deleteErr: errors.New("delete down")}
	skillErr := errors.New("bad skill")
	dbs := &fakeDatabasesApp{skillErr: skillErr}
	reg := NewRegistry(&stubSource{manifests: []domain.StarterManifest{m}})
	svc := NewStarterService(reg, bdbs, dbs, zerolog.Nop())

	_, err := svc.CreateFromStarter(t.Context(), uuid.New(), nil, "fixture", "", "")
	if !errors.Is(err, domain.ErrStarterApplyFailed) {
		t.Fatalf("CreateFromStarter() error = %v, want the apply error even when cleanup fails", err)
	}
	if !errors.Is(err, skillErr) {
		t.Errorf("CreateFromStarter() error = %v, want the seeding cause to stay reachable", err)
	}
	if len(dbs.deletedDatabases) != 0 {
		t.Error("database was deleted although the bdb delete failed first")
	}
}

// TestAttachTargetID pins the slug resolution contract: (nil, true) for no attachment, a pointer to the
// created view's ID for a known slug, and ok = false while the target has not been created yet.
func TestAttachTargetID(t *testing.T) {
	viewID := uuid.New()
	createdIDs := map[string]uuid.UUID{"pending": viewID}

	t.Run("no attachment", func(t *testing.T) {
		got, ok := attachTargetID(createdIDs, domain.StarterFormula{Name: "add"})
		if !ok || got != nil {
			t.Fatalf("attachTargetID() = (%v, %v), want (nil, true) for an unattached formula", got, ok)
		}
	})

	t.Run("resolves to the created view's ID", func(t *testing.T) {
		got, ok := attachTargetID(createdIDs, domain.StarterFormula{Name: "mark", AttachedTo: "pending"})
		if !ok {
			t.Fatal("attachTargetID() ok = false, want true for a created target")
		}
		if got == nil || *got != viewID {
			t.Fatalf("attachTargetID() = %v, want pointer to %v", got, viewID)
		}
	})

	t.Run("uncreated target reports not ok", func(t *testing.T) {
		_, ok := attachTargetID(createdIDs, domain.StarterFormula{Name: "mark", AttachedTo: "ghost"})
		if ok {
			t.Fatal("attachTargetID() ok = true, want false for a target that was never created")
		}
	})
}
