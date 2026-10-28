//go:build integration

package integrationtest

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	dbdomain "github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
)

// TestCreateFromStarterOverHTTP applies a manifest with the databases leg going through the real internal HTTP
// endpoints (standalone topology) and verifies the same outcomes as the in-process path: provenance, schema,
// classifier-derived kinds with the attachment resolved, and the skill.
func TestCreateFromStarterOverHTTP(t *testing.T) {
	svc, deps := setupHTTPEngine(t, &testSource{manifests: []domain.StarterManifest{tasksManifest()}})
	ctx := t.Context()
	userID := uuid.New()

	bdbID, err := svc.CreateFromStarter(ctx, userID, testRoles, "tasks", "", "")
	if err != nil {
		t.Fatalf("CreateFromStarter() error = %v", err)
	}

	bdb, err := deps.bdbRepo.FindByID(ctx, bdbID)
	if err != nil {
		t.Fatalf("FindByID(%v) error = %v", bdbID, err)
	}
	if bdb.StarterKey == nil || *bdb.StarterKey != "tasks" {
		t.Errorf("provenance key = %v, want tasks", bdb.StarterKey)
	}

	tables, err := deps.querySvc.ListTablesInfo(ctx, userID, bdbID)
	if err != nil {
		t.Fatalf("ListTablesInfo() error = %v", err)
	}
	if !hasTable(tables, "tasks") {
		t.Errorf("tables = %v, want tasks present", tables)
	}

	formulas, err := deps.formulaSvc.ListFormulas(ctx, userID, bdbID)
	if err != nil {
		t.Fatalf("ListFormulas() error = %v", err)
	}
	byName := make(map[string]*dbdomain.Formula, len(formulas))
	for _, f := range formulas {
		byName[f.Name] = f
	}
	view, action := byName["open-tasks"], byName["complete-task"]
	if view == nil || action == nil || len(byName) != 4 {
		t.Fatalf("formulas = %v, want open-tasks / complete-task / add-task / done-tasks", formulas)
	}
	if view.Kind != dbdomain.FormulaKindView {
		t.Errorf("open-tasks kind = %q, want view", view.Kind)
	}
	// Guards createFormulaRequest (starters http adapter) and createFormulaInternalRequest (databases
	// internal handler) staying in sync — separate structs on either side of the endpoint.
	if view.Label != "Open tasks" {
		t.Errorf("open-tasks label = %q, want %q", view.Label, "Open tasks")
	}
	if action.Label != "" {
		t.Errorf("complete-task label = %q, want empty — it declares none", action.Label)
	}
	if action.AttachedToFormulaID == nil || *action.AttachedToFormulaID != view.ID {
		t.Errorf("complete-task attached to %v, want open-tasks' ID %v", action.AttachedToFormulaID, view.ID)
	}
	// The display flowed through the internal update endpoint over real HTTP, slot refs normalized.
	if view.Display == nil || view.Display.Slots.Toggle != action.ID.String() {
		t.Errorf("open-tasks display = %+v, want a checklist with toggle=%v", view.Display, action.ID)
	}

	skills, err := deps.skillSvc.ListSkills(ctx, userID, bdbID)
	if err != nil {
		t.Fatalf("ListSkills() error = %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "tasks-orientation" {
		t.Errorf("skills = %v, want [tasks-orientation]", skills)
	}
}

// TestCreateFromStarterOverHTTPCleansUpOnFailure verifies the delete-the-bdb path over HTTP: a late seeding
// failure (a skill targeting a missing table, surfaced by the internal endpoint's 422) triggers cleanup, whose
// database delete also travels the internal HTTP endpoint.
func TestCreateFromStarterOverHTTPCleansUpOnFailure(t *testing.T) {
	broken := tasksManifest()
	broken.Key = "broken-skill"
	broken.Skills[0].Targets = []string{"no-such-table"}

	svc, deps := setupHTTPEngine(t, &testSource{manifests: []domain.StarterManifest{broken}})
	ctx := t.Context()
	userID := uuid.New()

	_, err := svc.CreateFromStarter(ctx, userID, testRoles, "broken-skill", "", "")
	if !errors.Is(err, domain.ErrStarterApplyFailed) {
		t.Fatalf("CreateFromStarter() error = %v, want ErrStarterApplyFailed", err)
	}

	bdbs, err := deps.bdbRepo.ListByUser(ctx, userID)
	if err != nil {
		t.Fatalf("ListByUser() error = %v", err)
	}
	for _, bdb := range bdbs {
		if !bdb.IsSoftDeleted() {
			t.Errorf("bdb %v survived a failed apply without being deleted", bdb.ID)
		}
		if _, err := deps.dbRepo.FindByBdb(ctx, bdb.ID); !errors.Is(err, dbdomain.ErrDatabaseNotFound) {
			t.Errorf("database for bdb %v still exists after cleanup (err = %v)", bdb.ID, err)
		}
	}
}
