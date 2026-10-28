//go:build integration

package integrationtest

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	dbdomain "github.com/alexandremgo/baguettedb/pkg/databases/domain"
	dbservice "github.com/alexandremgo/baguettedb/pkg/databases/service"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/embedded"
)

// applyKanban applies the embedded kanban starter for a fresh user and returns the wired services
// with the new bdb's coordinates, ready for formula runs.
func applyKanban(t *testing.T) (deps engineDeps, userID, bdbID uuid.UUID) {
	t.Helper()
	source, err := embedded.NewSource()
	if err != nil {
		t.Fatalf("NewSource() error = %v", err)
	}
	svc, deps := setupEngine(t, source)
	userID = uuid.New()
	bdbID, err = svc.CreateFromStarter(t.Context(), userID, testRoles, "kanban", "", "")
	if err != nil {
		t.Fatalf("CreateFromStarter(kanban) error = %v", err)
	}
	return deps, userID, bdbID
}

// TestKanbanBoardSurface pins the seeded shape: the board display carries the full slot set with the
// move action's three labeled columns, the quick bar exposes only the name, and the two terminal
// statuses live in edit-task's wider vocabulary — wontdo deliberately absent from the move options.
func TestKanbanBoardSurface(t *testing.T) {
	deps, userID, bdbID := applyKanban(t)

	formulas, err := deps.formulaSvc.ListFormulas(t.Context(), userID, bdbID)
	if err != nil {
		t.Fatalf("ListFormulas() error = %v", err)
	}
	byName := make(map[string]*dbdomain.Formula, len(formulas))
	for _, f := range formulas {
		byName[f.Name] = f
	}

	board := byName["task-board"]
	if board == nil || board.Display == nil || board.Display.Mode != dbdomain.DisplayModeBoard {
		t.Fatalf("task-board display = %+v, want board mode", board)
	}
	slots := board.Display.Slots
	if slots.Move == "" || slots.Reorder == "" || slots.Add == "" || slots.Edit == "" || slots.Detail == "" {
		t.Errorf("board slots = %+v, want move/reorder/add/edit/detail all filled", slots)
	}
	if group := board.Display.RoleColumn(dbdomain.DisplayRoleGroup); group != "status" {
		t.Errorf("group-role column = %q, want status", group)
	}

	statusOptions := func(name string) []string {
		var values []string
		for _, p := range byName[name].Params {
			if p.Name == "status" {
				for _, o := range p.Options {
					values = append(values, o.Value)
				}
			}
		}
		return values
	}
	if got := statusOptions("move-task"); strings.Join(got, ",") != "todo,doing,done" {
		t.Errorf("move-task status options = %v, want the three columns without wontdo", got)
	}
	if got := statusOptions("edit-task"); strings.Join(got, ",") != "todo,doing,done,wontdo" {
		t.Errorf("edit-task status options = %v, want the full vocabulary", got)
	}

	for _, p := range byName["add-task"].Params {
		if p.Name == "name" && !p.Inline {
			t.Errorf("add-task name param: want inline for the quick bar")
		}
		if p.Name != "name" && !p.Optional {
			t.Errorf("add-task %s: want optional — the quick bar sends only the name", p.Name)
		}
	}
}

// TestKanbanFlowAndAging drives the board loop: captures append at the bottom of To do, a placing
// move re-homes and positions in one run while stamping the aging clock, and edit-task refreshes
// that clock only on a real status change.
func TestKanbanFlowAndAging(t *testing.T) {
	deps, userID, bdbID := applyKanban(t)

	runFormula(t, deps, userID, bdbID, "add-task", map[string]any{"name": "Write spec"})
	runFormula(t, deps, userID, bdbID, "add-task", map[string]any{"name": "Ship it", "priority": "high"})
	positions := execSQL(t, deps, userID, bdbID,
		"SELECT name, position FROM tasks ORDER BY position", dbdomain.IntentQuery)
	if cell(t, positions, 0, "name") != "Write spec" || cell(t, positions, 1, "position") != int64(2) {
		t.Errorf("positions after captures = %v, want appends in order", positions.Rows)
	}

	board := runFormula(t, deps, userID, bdbID, "task-board", nil)
	if got := cell(t, board.Result, 1, "card"); got != "🔴 Ship it" {
		t.Errorf("high-priority card = %v, want the marker prefix", got)
	}
	if body, _ := cell(t, board.Result, 0, "body").(string); !strings.Contains(body, "⏱ <1d") {
		t.Errorf("fresh card body = %q, want the young-age marker", body)
	}

	// The placing move: one run lands "Write spec" in doing and stamps its aging clock.
	rowid := cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM tasks WHERE name = 'Write spec'", dbdomain.IntentQuery), 0, "id").(int64)
	runFormula(t, deps, userID, bdbID, "move-task",
		map[string]any{"rowid": float64(rowid), "status": "doing", "ordered_rowids": []any{float64(rowid)}})
	moved := execSQL(t, deps, userID, bdbID,
		"SELECT status, position, status_changed_at >= datetime('now', '-1 minute') AS fresh "+
			"FROM tasks WHERE name = 'Write spec'", dbdomain.IntentQuery)
	if cell(t, moved, 0, "status") != "doing" || cell(t, moved, 0, "fresh") != int64(1) {
		t.Errorf("after move = %v, want doing with a fresh stamp", moved.Rows)
	}

	// A same-status edit preserves the aging clock; a status-changing one refreshes it.
	execSQL(t, deps, userID, bdbID,
		"UPDATE tasks SET status_changed_at = datetime('now', '-3 days') WHERE name = 'Write spec'",
		dbdomain.IntentExecute)
	runFormula(t, deps, userID, bdbID, "edit-task",
		map[string]any{"rowid": float64(rowid), "name": "Write spec", "status": "doing"})
	kept := execSQL(t, deps, userID, bdbID,
		"SELECT status_changed_at <= datetime('now', '-2 days') AS old FROM tasks WHERE name = 'Write spec'",
		dbdomain.IntentQuery)
	if cell(t, kept, 0, "old") != int64(1) {
		t.Errorf("same-status edit restamped the aging clock — it must preserve it")
	}
	board = runFormula(t, deps, userID, bdbID, "task-board", nil)
	if body, _ := cell(t, board.Result, 0, "body").(string); !strings.Contains(body, "⏱ 3d") {
		t.Errorf("aged card body = %q, want the day count", body)
	}
	runFormula(t, deps, userID, bdbID, "edit-task",
		map[string]any{"rowid": float64(rowid), "name": "Write spec", "status": "todo"})
	restamped := execSQL(t, deps, userID, bdbID,
		"SELECT status_changed_at >= datetime('now', '-1 minute') AS fresh FROM tasks WHERE name = 'Write spec'",
		dbdomain.IntentQuery)
	if cell(t, restamped, 0, "fresh") != int64(1) {
		t.Errorf("status-changing edit kept the old stamp — it must refresh it")
	}
}

// TestKanbanDerivedArchive pins the two exits and their timing: wontdo leaves the board immediately,
// done lingers with a countdown through the 2-day window and then derives into the archive — no
// writer ever moves a row there.
func TestKanbanDerivedArchive(t *testing.T) {
	deps, userID, bdbID := applyKanban(t)

	runFormula(t, deps, userID, bdbID, "add-task", map[string]any{"name": "Bad idea"})
	runFormula(t, deps, userID, bdbID, "add-task", map[string]any{"name": "Good idea"})
	badID := cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM tasks WHERE name = 'Bad idea'", dbdomain.IntentQuery), 0, "id").(int64)
	goodID := cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM tasks WHERE name = 'Good idea'", dbdomain.IntentQuery), 0, "id").(int64)

	runFormula(t, deps, userID, bdbID, "wontdo-task", map[string]any{"rowid": float64(badID)})
	board := runFormula(t, deps, userID, bdbID, "task-board", nil)
	if len(board.Result.Rows) != 1 {
		t.Fatalf("board rows after wontdo = %d, want the abandoned task gone immediately", len(board.Result.Rows))
	}
	archive := runFormula(t, deps, userID, bdbID, "task-archive", nil)
	if len(archive.Result.Rows) != 1 {
		t.Fatalf("archive rows = %d, want the abandoned task", len(archive.Result.Rows))
	}
	if body, _ := cell(t, archive.Result, 0, "body").(string); !strings.Contains(body, "🚫 won't do") {
		t.Errorf("archived wontdo body = %q, want its label", body)
	}

	// Fresh done: still on the board, with the countdown; absent from the archive.
	runFormula(t, deps, userID, bdbID, "move-task", map[string]any{"rowid": float64(goodID), "status": "done"})
	board = runFormula(t, deps, userID, bdbID, "task-board", nil)
	if len(board.Result.Rows) != 1 {
		t.Fatalf("board rows after done = %d, want the fresh done still lingering", len(board.Result.Rows))
	}
	if body, _ := cell(t, board.Result, 0, "body").(string); !strings.Contains(body, "🗄 archives in 2d") {
		t.Errorf("fresh done body = %q, want the countdown", body)
	}
	if archive = runFormula(t, deps, userID, bdbID, "task-archive", nil); len(archive.Result.Rows) != 1 {
		t.Errorf("archive rows with a fresh done = %d, want it not there yet", len(archive.Result.Rows))
	}

	// Past the window the same row derives across, no mutation involved.
	execSQL(t, deps, userID, bdbID,
		"UPDATE tasks SET status_changed_at = datetime('now', '-3 days') WHERE name = 'Good idea'",
		dbdomain.IntentExecute)
	if board = runFormula(t, deps, userID, bdbID, "task-board", nil); len(board.Result.Rows) != 0 {
		t.Errorf("board rows past the window = %d, want the done task derived away", len(board.Result.Rows))
	}
	archive = runFormula(t, deps, userID, bdbID, "task-archive", nil)
	if len(archive.Result.Rows) != 2 {
		t.Fatalf("archive rows past the window = %d, want both exits", len(archive.Result.Rows))
	}
	// Newest exit first: the wontdo from today leads, the backdated done follows with its age.
	if body, _ := cell(t, archive.Result, 1, "body").(string); !strings.Contains(body, "✅ done · 3d ago") {
		t.Errorf("archived done body = %q, want the label with its age", body)
	}

	// The board's vocabulary is closed: a column that doesn't exist is rejected up front.
	_, err := deps.formulaSvc.RunFormula(t.Context(), userID, bdbID, "move-task",
		dbservice.FormulaRunInput{Params: map[string]any{"rowid": float64(goodID), "status": "review"}})
	if !errors.Is(err, dbdomain.ErrFormulaParamValue) {
		t.Errorf("move to an undeclared column error = %v, want ErrFormulaParamValue", err)
	}
}

// TestKanbanRestoreAndPurge pins the archive's two actions: restore sends a task back to the bottom
// of To do with a fresh aging stamp, and the permanent delete lives only here.
func TestKanbanRestoreAndPurge(t *testing.T) {
	deps, userID, bdbID := applyKanban(t)

	runFormula(t, deps, userID, bdbID, "add-task", map[string]any{"name": "Keeper"})
	runFormula(t, deps, userID, bdbID, "add-task", map[string]any{"name": "Mistake"})
	for _, name := range []string{"Keeper", "Mistake"} {
		id := cell(t, execSQL(t, deps, userID, bdbID,
			"SELECT id FROM tasks WHERE name = '"+name+"'", dbdomain.IntentQuery), 0, "id").(int64)
		runFormula(t, deps, userID, bdbID, "wontdo-task", map[string]any{"rowid": float64(id)})
	}

	keeperID := cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM tasks WHERE name = 'Keeper'", dbdomain.IntentQuery), 0, "id").(int64)
	runFormula(t, deps, userID, bdbID, "restore-task", map[string]any{"rowid": float64(keeperID)})
	restored := execSQL(t, deps, userID, bdbID,
		"SELECT status, position FROM tasks WHERE name = 'Keeper'", dbdomain.IntentQuery)
	// Both tasks had left To do, so the append lands at 1 — positions are per-column, and an
	// emptied column restarts.
	if cell(t, restored, 0, "status") != "todo" || cell(t, restored, 0, "position") != int64(1) {
		t.Errorf("restored task = %v, want todo at the appended position", restored.Rows)
	}
	if board := runFormula(t, deps, userID, bdbID, "task-board", nil); len(board.Result.Rows) != 1 {
		t.Errorf("board rows after restore = %d, want the keeper back", len(board.Result.Rows))
	}

	mistakeID := cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM tasks WHERE name = 'Mistake'", dbdomain.IntentQuery), 0, "id").(int64)
	runFormula(t, deps, userID, bdbID, "purge-task", map[string]any{"rowid": float64(mistakeID)})
	left := execSQL(t, deps, userID, bdbID, "SELECT COUNT(*) AS n FROM tasks", dbdomain.IntentQuery)
	if cell(t, left, 0, "n") != int64(1) {
		t.Errorf("tasks after purge = %v, want only the keeper", left.Rows)
	}
}
