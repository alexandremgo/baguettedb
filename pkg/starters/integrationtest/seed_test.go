//go:build integration

package integrationtest

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	dbdomain "github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
)

// slotsManifest is a fixture whose schema carries a catalog table the manifest seeds: the rows a formula
// expects to exist before any member adds data.
func slotsManifest() domain.StarterManifest {
	return domain.StarterManifest{
		Key:         "slots",
		Version:     1,
		Name:        "Slots",
		Description: "A seeded catalog.",
		Schema: []string{
			"CREATE TABLE slots (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, position INTEGER NOT NULL, " +
				"note TEXT)",
		},
		Seed: []domain.StarterSeed{{
			Table:   "slots",
			Columns: []string{"name", "position", "note"},
			Rows:    [][]any{{"breakfast", int64(1), nil}, {"lunch", int64(2), "midday"}, {"dinner", int64(3), nil}},
		}},
		Formulas: []domain.StarterFormula{{
			Name:        "slot-list",
			Description: "The slots in order.",
			SQL:         "SELECT id AS rowid, name, position, note FROM slots ORDER BY position",
		}},
	}
}

// assertSeededSlots reads the seeded catalog back through the real database and checks its rows, order, and
// the NULL that a nil cell became.
func assertSeededSlots(t *testing.T, deps engineDeps, userID, bdbID uuid.UUID) {
	t.Helper()
	rows := execSQL(t, deps, userID, bdbID,
		"SELECT name, position, note FROM slots ORDER BY position", dbdomain.IntentQuery)
	if len(rows.Rows) != 3 {
		t.Fatalf("seeded slots = %d rows, want 3", len(rows.Rows))
	}
	if cell(t, rows, 0, "name") != "breakfast" || cell(t, rows, 2, "name") != "dinner" {
		t.Errorf("seeded slot names = %v, want breakfast first and dinner last", rows.Rows)
	}
	if cell(t, rows, 1, "position") != int64(2) {
		t.Errorf("lunch position = %v, want the integer 2", cell(t, rows, 1, "position"))
	}
	if cell(t, rows, 1, "note") != "midday" || cell(t, rows, 0, "note") != nil {
		t.Errorf("notes = %v / %v, want midday and NULL", cell(t, rows, 1, "note"), cell(t, rows, 0, "note"))
	}
}

// TestCreateFromStarterSeedsRows applies a seeded manifest in-process and reads the catalog back.
func TestCreateFromStarterSeedsRows(t *testing.T) {
	svc, deps := setupEngine(t, &testSource{manifests: []domain.StarterManifest{slotsManifest()}})
	userID := uuid.New()

	bdbID, err := svc.CreateFromStarter(t.Context(), userID, testRoles, "slots", "", "")
	if err != nil {
		t.Fatalf("CreateFromStarter() error = %v", err)
	}
	assertSeededSlots(t, deps, userID, bdbID)
}

// TestCreateFromStarterSeedsRowsOverHTTP applies the same manifest through the internal data endpoint: the
// bound values cross the wire typed and land identically.
func TestCreateFromStarterSeedsRowsOverHTTP(t *testing.T) {
	svc, deps := setupHTTPEngine(t, &testSource{manifests: []domain.StarterManifest{slotsManifest()}})
	userID := uuid.New()

	bdbID, err := svc.CreateFromStarter(t.Context(), userID, testRoles, "slots", "", "")
	if err != nil {
		t.Fatalf("CreateFromStarter() error = %v", err)
	}
	assertSeededSlots(t, deps, userID, bdbID)
}

// TestCreateFromStarterSeedFailureCleansUp pins that a seed the table refuses (a UNIQUE violation here) fails
// the creation like any other apply error and removes the half-built bdb.
func TestCreateFromStarterSeedFailureCleansUp(t *testing.T) {
	broken := slotsManifest()
	broken.Seed[0].Rows = append(broken.Seed[0].Rows, []any{"dinner", int64(4), nil})
	svc, deps := setupEngine(t, &testSource{manifests: []domain.StarterManifest{broken}})
	userID := uuid.New()

	_, err := svc.CreateFromStarter(t.Context(), userID, testRoles, "slots", "", "")
	if err == nil {
		t.Fatal("CreateFromStarter() error = nil, want the seed's UNIQUE violation")
	}
	if !errors.Is(err, domain.ErrStarterApplyFailed) {
		t.Errorf("CreateFromStarter() error = %v, want ErrStarterApplyFailed", err)
	}
	bdbs, err := deps.bdbRepo.ListByUser(t.Context(), userID)
	if err != nil {
		t.Fatalf("ListByUser() error = %v", err)
	}
	for _, bdb := range bdbs {
		if !bdb.IsSoftDeleted() {
			t.Errorf("bdb %v survived a failed seed without being deleted", bdb.ID)
		}
		if _, err := deps.dbRepo.FindByBdb(t.Context(), bdb.ID); !errors.Is(err, dbdomain.ErrDatabaseNotFound) {
			t.Errorf("database for bdb %v still exists after cleanup (err = %v)", bdb.ID, err)
		}
	}
}
