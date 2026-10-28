//go:build integration

package integrationtest

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// newKanban creates, in a fresh data directory, a bdb from the kanban starter holding two open tasks, and returns the
// data directory.
func newKanban(t *testing.T) string {
	t.Helper()
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")
	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "create", "--starter", "kanban")
	if code != exitOK {
		t.Fatalf("bdbs create --starter kanban = %d %q, stderr %q", code, stdout, stderr)
	}
	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir,
		"sql", "data", "Kanban", "INSERT INTO tasks (name, position) VALUES ('Write tests', 1), ('Ship it', 2)")
	if code != exitOK {
		t.Fatalf("inserting tasks = %d %q, stderr %q", code, stdout, stderr)
	}
	return dataDir
}

// TestRun_Formulas_List checks that formulas list prints every formula of a bdb with its kind, label and description,
// that the bare formulas command prints the same, and that --json lists them all.
func TestRun_Formulas_List(t *testing.T) {
	dataDir := newKanban(t)

	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "formulas", "list", "Kanban")
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if code != exitOK || !strings.HasPrefix(lines[0], "NAME") ||
		!hasRowPrefix(lines, []string{"task-board", "view", "Board"}) ||
		!hasRowPrefix(lines, []string{"add-task", "action", "Add", "task"}) {
		t.Fatalf("formulas list = %d %q, stderr %q; want the board view and the add-task action", code, stdout, stderr)
	}
	if _, bare, _ := runCLI(t, "bdb", "--data-dir", dataDir, "formulas", "Kanban"); bare != stdout {
		t.Errorf("bare formulas = %q, want the formulas list output", bare)
	}

	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "formulas", "Kanban", "--json")
	var formulas []struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(stdout), &formulas); code != exitOK || err != nil || len(formulas) != len(lines)-1 {
		t.Errorf("formulas --json = %d %q (%v), stderr %q; want the %d formulas of the table", code, stdout, err,
			stderr, len(lines)-1)
	}
}

// TestRun_Formulas_Show checks that formulas show prints a formula's kind and verb, its parameters with how --param
// gives each one, and its SQL; that --json carries the same parameters; and that an unknown formula points at the list.
func TestRun_Formulas_Show(t *testing.T) {
	dataDir := newKanban(t)

	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "formulas", "show", "Kanban", "move-task")
	lines := strings.Split(stdout, "\n")
	if code != exitOK || lines[0] != "move-task  (action, updates)" || lines[1] != "Label:        Move task" ||
		!hasRow(lines, strings.Fields("rowid integer yes a row's rowid")) ||
		!hasRow(lines, strings.Fields("status text yes one of: todo, doing, done")) ||
		!hasRow(lines, strings.Fields("ordered_rowids integer no JSON array of rowids, in their new order")) ||
		!strings.Contains(stdout, "\nSQL\nUPDATE tasks SET status = :status") {
		t.Fatalf("formulas show = %d %q, stderr %q; want the heading, the three parameters and the SQL", code, stdout,
			stderr)
	}

	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "formulas", "show", "Kanban", "move-task", "--json")
	var f struct {
		Kind   string `json:"kind"`
		Verb   string `json:"verb"`
		SQL    string `json:"sql"`
		Params []struct {
			Name     string `json:"name"`
			Source   string `json:"source"`
			Required bool   `json:"required"`
			Options  []struct {
				Value string `json:"value"`
				Label string `json:"label"`
			} `json:"options"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(stdout), &f); code != exitOK || err != nil || f.Kind != "action" ||
		f.Verb != "update" || f.SQL == "" || len(f.Params) != 3 || f.Params[0].Source != "selection-single" ||
		!f.Params[1].Required || len(f.Params[1].Options) != 3 || f.Params[1].Options[1].Label != "In progress" ||
		f.Params[2].Required {
		t.Errorf("formulas show --json = %d %q (%v), stderr %q; want the action with its three parameters", code,
			stdout, err, stderr)
	}

	code, _, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "formulas", "show", "Kanban", "board")
	want := `in Kanban: no formula named "board"; ` + "`bdb formulas Kanban`"
	if code != exitFailure || !strings.Contains(stderr, want) {
		t.Errorf("an unknown formula = %d, stderr %q; want a pointer to the list", code, stderr)
	}
}

// TestRun_Formulas_RunView checks that formulas run prints a view's rows as a table with the count on stderr, and as
// a JSON array under --json, and that an unknown formula points at the list.
func TestRun_Formulas_RunView(t *testing.T) {
	dataDir := newKanban(t)

	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "formulas", "run", "kanban", "task-board")
	if code != exitOK || !strings.Contains(stdout, "Write tests") || !strings.Contains(stdout, "Ship it") ||
		stderr != "(2 rows)\n" {
		t.Errorf("formulas run task-board = %d %q, stderr %q; want both tasks and the count", code, stdout, stderr)
	}

	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "formulas", "run", "Kanban", "task-board", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); code != exitOK || err != nil || len(rows) != 2 ||
		rows[0]["name"] != "Write tests" || rows[0]["status"] != "todo" {
		t.Errorf("formulas run --json = %d %q (%v), stderr %q; want both tasks as objects", code, stdout, err, stderr)
	}

	code, _, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "formulas", "run", "Kanban", "board")
	want := `in Kanban: no formula named "board"; ` + "`bdb formulas Kanban`"
	if code != exitFailure || !strings.Contains(stderr, want) {
		t.Errorf("an unknown formula = %d, stderr %q; want a pointer to the list", code, stderr)
	}
}

// TestRun_Formulas_RunActionWithParams checks formula runs with --param: an action with typed values prints its count
// and batch (and its change under --json), a view filters by a parameter, and a value the parameter refuses, a missing
// or unknown parameter (listing the declared ones), a malformed --param and a parameter given twice are usage errors
// naming the --param, a refused value pointing at formulas show.
func TestRun_Formulas_RunActionWithParams(t *testing.T) {
	dataDir := newKanban(t)
	run := func(args ...string) (int, string, string) {
		t.Helper()
		return runCLI(t, append([]string{"bdb", "--data-dir", dataDir, "formulas", "run"}, args...)...)
	}

	code, stdout, stderr := run("Kanban", "add-task", "--param", "name=Review the PR", "--param", "due=2026-12-31")
	if code != exitOK || !batchInOutput.MatchString(stdout) || !strings.HasPrefix(stdout, "1 row changed") {
		t.Fatalf("add-task = %d %q, stderr %q; want one row changed and its batch", code, stdout, stderr)
	}
	code, stdout, stderr = run("Kanban", "task-board", "--param", "search=Review", "--json")
	var board []map[string]any
	if err := json.Unmarshal([]byte(stdout), &board); code != exitOK || err != nil || len(board) != 1 ||
		board[0]["name"] != "Review the PR" || board[0]["due"] != "2026-12-31" {
		t.Errorf("task-board search=Review = %d %q (%v), stderr %q; want the new task alone", code, stdout, err, stderr)
	}

	code, stdout, stderr = run("Kanban", "wontdo-task", "--param", "rowid=1", "--json")
	var change struct {
		AffectedRowCount int64  `json:"affected_row_count"`
		BatchID          string `json:"batch_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &change); code != exitOK || err != nil || change.AffectedRowCount != 1 ||
		change.BatchID == "" {
		t.Errorf("wontdo-task --json = %d %q (%v), stderr %q; want one row changed in a batch", code, stdout, err,
			stderr)
	}

	for _, tt := range []struct {
		args   []string
		code   int
		stderr string
	}{
		{[]string{"wontdo-task", "--param", "rowid=abc"}, exitUsage, `Error: --param rowid expects an integer, got ` +
			"\"abc\"\nRun `bdb formulas show Kanban wontdo-task` for its parameters.\n"},
		{[]string{"add-task", "--param", "name=x", "--param", "due=31/12"}, exitUsage,
			`Error: --param due expects a YYYY-MM-DD date, got "31/12"`},
		{[]string{"move-task", "--param", "rowid=1", "--param", "status=later"}, exitUsage,
			`Error: --param status must be one of "todo", "doing", "done", got "later"`},
		{[]string{"wontdo-task"}, exitUsage, "Error: --param rowid needs a value"},
		{[]string{"add-task", "--param", "name=x", "--param", "nope=1"}, exitUsage,
			"Error: --param nope is unknown: the formula takes name, description, due, priority\n"},
		{[]string{"add-task", "--param", "=x"}, exitUsage, `invalid argument "=x" for "--param" flag`},
		{[]string{"add-task", "--param", "name=A", "--param", "name=B"}, exitUsage,
			`invalid argument "name=B" for "--param" flag: "name" given twice`},
	} {
		code, _, stderr := run(append([]string{"Kanban"}, tt.args...)...)
		if code != tt.code || !strings.Contains(stderr, tt.stderr) {
			t.Errorf("formulas run %v = %d, stderr %q; want %d and %q", tt.args, code, stderr, tt.code, tt.stderr)
		}
	}
}

// TestRun_Formulas_RunBulkSelection checks that a bulk row selection is given as a JSON array of row IDs.
func TestRun_Formulas_RunBulkSelection(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")
	for _, args := range [][]string{
		{"bdbs", "create", "--starter", "notes"},
		{"sql", "data", "Notes", "INSERT INTO notes (body) VALUES ('one'), ('two'), ('three')"},
	} {
		code, stdout, stderr := runCLI(t, append([]string{"bdb", "--data-dir", dataDir}, args...)...)
		if code != exitOK {
			t.Fatalf("%v = %d %q, stderr %q", args, code, stdout, stderr)
		}
	}

	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir,
		"formulas", "run", "Notes", "delete-notes", "--param", "rowids=[1,3]")
	if code != exitOK || !strings.HasPrefix(stdout, "2 rows changed") {
		t.Errorf("delete-notes rowids=[1,3] = %d %q, stderr %q; want two rows deleted", code, stdout, stderr)
	}
	_, stdout, _ = runCLI(t, "bdb", "--data-dir", dataDir, "q", "Notes", "SELECT body FROM notes")
	if stdout != "body\ntwo\n" {
		t.Errorf("notes left = %q, want only two", stdout)
	}
}

// TestRun_Formulas_RunGroup checks that a group parameter is given as a JSON array of row objects, stored as the
// formula composes it, and that a value that is not such an array is a usage error naming the --param.
func TestRun_Formulas_RunGroup(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")
	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "create", "--starter", "recipes-meal-plan")
	if code != exitOK {
		t.Fatalf("bdbs create --starter recipes-meal-plan = %d %q, stderr %q", code, stdout, stderr)
	}
	run := func(args ...string) (int, string, string) {
		t.Helper()
		return runCLI(t, append([]string{"bdb", "--data-dir", dataDir, "formulas", "run", "Recipes & meal plan"},
			args...)...)
	}

	code, stdout, stderr = run("add-recipe", "--param", "name=Baguette",
		"--param", `tags=[{"tag":"bread"},{"tag":" quick "},{"tag":""}]`)
	if code != exitOK || !strings.HasPrefix(stdout, "1 row changed") {
		t.Fatalf("add-recipe with tags = %d %q, stderr %q; want one row changed", code, stdout, stderr)
	}
	_, stdout, _ = runCLI(t, "bdb", "--data-dir", dataDir, "q", "Recipes & meal plan", "SELECT tags FROM recipes")
	if stdout != "tags\n[\"bread\",\"quick\"]\n" {
		t.Errorf("stored tags = %q, want the two non-blank tags, trimmed", stdout)
	}

	code, _, stderr = run("add-recipe", "--param", "name=Croissant", "--param", "tags=bread")
	const wantErr = `Error: --param tags expects a JSON array of objects, got "bread"`
	if code != exitUsage || !strings.Contains(stderr, wantErr) {
		t.Errorf("add-recipe tags=bread = %d, stderr %q; want a usage error naming --param tags", code, stderr)
	}
}
