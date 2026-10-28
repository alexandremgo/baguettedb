//go:build integration

package integrationtest

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	dbdomain "github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/embedded"
	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
)

// testRoles is the role set every test user acts with.
var testRoles = []authz.Role{authz.RoleUser}

// tasksManifest exercises every manifest component: schema (table + index), a checklist view with declared
// slots, an action attached to it, a hidden view, and a table-targeted skill.
func tasksManifest() domain.StarterManifest {
	return domain.StarterManifest{
		Key:         "tasks",
		Version:     2,
		Name:        "Tasks",
		Description: "A tiny task tracker.",
		Schema: []string{
			"CREATE TABLE tasks (id INTEGER PRIMARY KEY, title TEXT NOT NULL, done_at TEXT)",
			"CREATE INDEX idx_tasks_open ON tasks(done_at) WHERE done_at IS NULL",
		},
		Formulas: []domain.StarterFormula{
			{
				Name:        "open-tasks",
				Label:       "Open tasks",
				Description: "Tasks not yet done, as a checklist.",
				SQL:         "SELECT id AS rowid, title, 0 AS done FROM tasks WHERE done_at IS NULL ORDER BY id",
				Display: &domain.StarterFormulaDisplay{
					Mode: "checklist",
					Columns: []domain.StarterDisplayColumn{
						{Column: "title", Role: "title"},
						{Column: "done", Role: "check"},
					},
					Slots: domain.StarterDisplaySlots{Add: "add-task", Toggle: "complete-task"},
				},
			},
			{
				Name:        "complete-task",
				Description: "Marks the selected task done.",
				SQL:         "UPDATE tasks SET done_at = CURRENT_TIMESTAMP WHERE id = :rowid",
				Params:      []domain.StarterFormulaParam{{Name: "rowid", Type: "integer", Source: "selection-single"}},
				AttachedTo:  "open-tasks",
			},
			{
				Name:        "add-task",
				Description: "Adds a task.",
				SQL:         "INSERT INTO tasks (title) VALUES (:title)",
				Params:      []domain.StarterFormulaParam{{Name: "title", Type: "text", Source: "prompt"}},
				AttachedTo:  "open-tasks",
			},
			{
				Name:        "done-tasks",
				Description: "Completed tasks, newest first.",
				SQL: "SELECT id AS rowid, title, done_at FROM tasks WHERE done_at IS NOT NULL " +
					"ORDER BY done_at DESC",
				Hidden: true,
			},
		},
		Skills: []domain.StarterSkill{
			{
				Name:        "tasks-orientation",
				Description: "How to work with tasks.",
				Body:        "Open tasks have done_at IS NULL; complete them via complete-task.",
				Targets:     []string{"tasks"},
			},
		},
	}
}

// TestCreateFromStarterAppliesManifest applies a fixture manifest end-to-end and verifies every component
// landed: provenance and defaults on the bdb row, schema in the real SQLite database, formulas with
// classifier-derived kinds and the attachment resolved to the view's fresh ID, and the table-targeted skill.
func TestCreateFromStarterAppliesManifest(t *testing.T) {
	svc, deps := setupEngine(t, &testSource{manifests: []domain.StarterManifest{tasksManifest()}})
	ctx := t.Context()
	userID := uuid.New()

	bdbID, err := svc.CreateFromStarter(ctx, userID, testRoles, "tasks", "", "")
	if err != nil {
		t.Fatalf("CreateFromStarter() error = %v", err)
	}

	// Provenance and defaults on the bdb row.
	bdb, err := deps.bdbRepo.FindByID(ctx, bdbID)
	if err != nil {
		t.Fatalf("FindByID(%v) error = %v", bdbID, err)
	}
	if bdb.Name != "Tasks" || bdb.Description != "A tiny task tracker." {
		t.Errorf("bdb = (%q, %q), want the manifest's name and description", bdb.Name, bdb.Description)
	}
	if bdb.StarterKey == nil || *bdb.StarterKey != "tasks" || bdb.StarterVersion == nil || *bdb.StarterVersion != 2 {
		t.Errorf("provenance = (%v, %v), want (tasks, 2)", bdb.StarterKey, bdb.StarterVersion)
	}

	// Schema landed in the real SQLite database.
	tables, err := deps.querySvc.ListTablesInfo(ctx, userID, bdbID)
	if err != nil {
		t.Fatalf("ListTablesInfo() error = %v", err)
	}
	if !hasTable(tables, "tasks") {
		t.Errorf("tables = %v, want tasks present", tables)
	}

	// Formulas classified by the real classifier, attachment resolved to the view's fresh ID.
	formulas, err := deps.formulaSvc.ListFormulas(ctx, userID, bdbID)
	if err != nil {
		t.Fatalf("ListFormulas() error = %v", err)
	}
	byName := make(map[string]*dbdomain.Formula, len(formulas))
	for _, f := range formulas {
		byName[f.Name] = f
	}
	if len(byName) != 4 {
		t.Fatalf("formulas = %d, want 4", len(byName))
	}
	view, action, add := byName["open-tasks"], byName["complete-task"], byName["add-task"]
	if view == nil || action == nil || add == nil {
		t.Fatalf("formulas missing: view=%v action=%v add=%v", view, action, add)
	}
	if view.Kind != dbdomain.FormulaKindView {
		t.Errorf("open-tasks kind = %q, want view (real classifier)", view.Kind)
	}
	if action.AttachedToFormulaID == nil || *action.AttachedToFormulaID != view.ID {
		t.Errorf("complete-task attached to %v, want open-tasks' ID %v", action.AttachedToFormulaID, view.ID)
	}

	// The display was applied in the post-pass — slot refs given as names, stored as the fresh UUIDs — and
	// the hidden flag landed at create time.
	if view.Display == nil {
		t.Fatal("open-tasks display = nil, want the manifest's checklist config")
	}
	if view.Display.Mode != dbdomain.DisplayModeChecklist ||
		view.Display.RoleColumn(dbdomain.DisplayRoleCheck) != "done" {
		t.Errorf("display = %+v, want checklist with the done check column", view.Display)
	}
	if view.Display.Slots.Toggle != action.ID.String() || view.Display.Slots.Add != add.ID.String() {
		t.Errorf("slots = %+v, want toggle=%v add=%v (normalized UUIDs)", view.Display.Slots, action.ID, add.ID)
	}
	if done := byName["done-tasks"]; done == nil || !done.Hidden {
		t.Errorf("done-tasks = %+v, want present and hidden", byName["done-tasks"])
	}

	// Skill with its target table, verified against the real schema.
	skills, err := deps.skillSvc.ListSkills(ctx, userID, bdbID)
	if err != nil {
		t.Fatalf("ListSkills() error = %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "tasks-orientation" {
		t.Fatalf("skills = %v, want [tasks-orientation]", skills)
	}
	if len(skills[0].TargetTables) != 1 || skills[0].TargetTables[0] != "tasks" {
		t.Errorf("skill targets = %v, want [tasks]", skills[0].TargetTables)
	}
}

// TestCreateFromStarterOverrides verifies a caller-provided name and description replace the manifest defaults.
func TestCreateFromStarterOverrides(t *testing.T) {
	svc, deps := setupEngine(t, &testSource{manifests: []domain.StarterManifest{tasksManifest()}})
	ctx := t.Context()
	userID := uuid.New()

	bdbID, err := svc.CreateFromStarter(ctx, userID, testRoles, "tasks", "My week", "Personal planning.")
	if err != nil {
		t.Fatalf("CreateFromStarter() error = %v", err)
	}
	bdb, err := deps.bdbRepo.FindByID(ctx, bdbID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if bdb.Name != "My week" || bdb.Description != "Personal planning." {
		t.Errorf("bdb = (%q, %q), want the caller overrides", bdb.Name, bdb.Description)
	}
}

// TestCreateFromStarterUnknownKey verifies an unknown key fails with ErrStarterNotFound and creates no bdb.
func TestCreateFromStarterUnknownKey(t *testing.T) {
	svc, deps := setupEngine(t, &testSource{manifests: []domain.StarterManifest{tasksManifest()}})
	ctx := t.Context()
	userID := uuid.New()

	if _, err := svc.CreateFromStarter(ctx, userID, testRoles, "ghost", "", ""); !errors.Is(err,
		domain.ErrStarterNotFound) {
		t.Fatalf("CreateFromStarter(ghost) error = %v, want ErrStarterNotFound", err)
	}
	bdbs, err := deps.bdbRepo.ListByUser(ctx, userID)
	if err != nil {
		t.Fatalf("ListByUser() error = %v", err)
	}
	if len(bdbs) != 0 {
		t.Errorf("bdbs = %d, want none created for an unknown key", len(bdbs))
	}
}

// TestCreateFromStarterCleansUpOnFailure drives real apply failures — early (broken DDL) and late (a skill
// targeting a missing table, after schema and formulas already landed) — and verifies the whole bdb is gone.
func TestCreateFromStarterCleansUpOnFailure(t *testing.T) {
	brokenSchema := tasksManifest()
	brokenSchema.Key = "broken-schema"
	brokenSchema.Schema = []string{"CREATE TABLE tasks (id INTEGER PRIMARY KEY,"}

	brokenSkill := tasksManifest()
	brokenSkill.Key = "broken-skill"
	brokenSkill.Skills[0].Targets = []string{"no-such-table"}

	tests := []struct {
		name string
		key  string
	}{
		{"schema failure", "broken-schema"},
		{"late skill failure", "broken-skill"},
	}

	svc, deps := setupEngine(t, &testSource{manifests: []domain.StarterManifest{brokenSchema, brokenSkill}})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			userID := uuid.New()

			_, err := svc.CreateFromStarter(ctx, userID, testRoles, tt.key, "", "")
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
		})
	}
}

// TestBuiltinStartersApply is the machine check that every embedded manifest actually works: schema, formulas,
// and skills all run through the real classifier and validators.
func TestBuiltinStartersApply(t *testing.T) {
	source, err := embedded.NewSource()
	if err != nil {
		t.Fatalf("NewSource() error = %v", err)
	}
	svc, deps := setupEngine(t, source)
	ctx := t.Context()

	manifests, err := source.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(manifests) == 0 {
		t.Fatal("no built-in starters to apply")
	}

	for _, m := range manifests {
		t.Run(m.Key, func(t *testing.T) {
			userID := uuid.New()
			bdbID, err := svc.CreateFromStarter(ctx, userID, testRoles, m.Key, "", "")
			if err != nil {
				t.Fatalf("CreateFromStarter(%q) error = %v", m.Key, err)
			}

			formulas, err := deps.formulaSvc.ListFormulas(ctx, userID, bdbID)
			if err != nil {
				t.Fatalf("ListFormulas() error = %v", err)
			}
			if len(formulas) != len(m.Formulas) {
				t.Errorf("formulas = %d, want %d", len(formulas), len(m.Formulas))
			}

			// Displays and hidden flags round-trip: every declared config landed with each slot ref
			// resolved to a UUID, and every hidden mark stuck.
			byName := make(map[string]*dbdomain.Formula, len(formulas))
			for _, f := range formulas {
				byName[f.Name] = f
			}
			for _, mf := range m.Formulas {
				created := byName[mf.Name]
				if created == nil {
					t.Errorf("formula %q missing after apply", mf.Name)
					continue
				}
				if created.Hidden != mf.Hidden {
					t.Errorf("formula %q hidden = %v, want %v", mf.Name, created.Hidden, mf.Hidden)
				}
				if (mf.Display != nil) != (created.Display != nil) {
					t.Errorf("formula %q display present = %v, want %v",
						mf.Name, created.Display != nil, mf.Display != nil)
					continue
				}
				if mf.Display == nil {
					continue
				}
				var stored int
				for _, ref := range created.Display.Slots.All() {
					stored++
					if _, err := uuid.Parse(ref); err != nil {
						t.Errorf("formula %q stores slot ref %q, want a normalized UUID", mf.Name, ref)
					}
				}
				if declared := len(mf.Display.Slots.Refs()); stored != declared {
					t.Errorf("formula %q stored %d slot refs, want %d", mf.Name, stored, declared)
				}
			}

			skills, err := deps.skillSvc.ListSkills(ctx, userID, bdbID)
			if err != nil {
				t.Fatalf("ListSkills() error = %v", err)
			}
			if len(skills) != len(m.Skills) {
				t.Errorf("skills = %d, want %d", len(skills), len(m.Skills))
			}
		})
	}
}

func hasTable(tables []dbdomain.TableInfo, name string) bool {
	for _, tbl := range tables {
		if tbl.Name == name {
			return true
		}
	}
	return false
}
