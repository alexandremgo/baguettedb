//go:build integration

package integrationtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openTasksFormula is a view of the kanban's tasks not done yet, as formulas create reads it.
const openTasksFormula = `{
	"name": "open-tasks",
	"description": "Tasks not done yet",
	"sql": "SELECT name FROM tasks WHERE status != 'done' ORDER BY position"
}`

// TestRun_Formulas_Create checks that formulas create makes a formula from stdin or from a file, naming its kind in
// one line or printing it under --json, and that the created formulas run.
func TestRun_Formulas_Create(t *testing.T) {
	dataDir := newKanban(t)

	code, stdout, stderr := runCLIWithStdin(t, openTasksFormula,
		"bdb", "--data-dir", dataDir, "formulas", "create", "Kanban", "--file", "-")
	if code != exitOK || stdout != "Created formula open-tasks in Kanban (view)\n" {
		t.Fatalf("formulas create from stdin = %d %q, stderr %q", code, stdout, stderr)
	}
	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "formulas", "run", "Kanban", "open-tasks")
	if code != exitOK || !strings.Contains(stdout, "Write tests") || !strings.Contains(stdout, "Ship it") {
		t.Errorf("formulas run open-tasks = %d %q, stderr %q; want the two open tasks", code, stdout, stderr)
	}

	file := filepath.Join(t.TempDir(), "add-urgent.json")
	action := `{
		"name": "add-urgent", "label": "Add urgent task", "description": "Adds a high-priority task",
		"sql": "INSERT INTO tasks (name, priority) VALUES (:name, 'high')",
		"params": [{"name": "name", "type": "text", "source": "prompt"}]
	}`
	if err := os.WriteFile(file, []byte(action), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "formulas", "create", "Kanban", "--file", file,
		"--json")
	var created struct {
		Name   string `json:"name"`
		Kind   string `json:"kind"`
		Verb   string `json:"verb"`
		Params []struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(stdout), &created); code != exitOK || err != nil || created.Name != "add-urgent" ||
		created.Kind != "action" || created.Verb != "insert" || len(created.Params) != 1 {
		t.Fatalf("formulas create --file --json = %d %q (%v), stderr %q", code, stdout, err, stderr)
	}
	code, _, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "formulas", "run", "Kanban", "add-urgent",
		"--param", "name=Fix the build")
	if code != exitOK {
		t.Fatalf("formulas run add-urgent = %d, stderr %q", code, stderr)
	}
	if _, stdout, _ = runCLI(t, "bdb", "--data-dir", dataDir, "q", "Kanban",
		"SELECT priority FROM tasks WHERE name = 'Fix the build'"); !strings.Contains(stdout, "high") {
		t.Errorf("the task add-urgent inserted = %q, want it high priority", stdout)
	}
}

// TestRun_Formulas_Create_Refused checks what formulas create refuses before anything is written: no --file, an empty
// formula, JSON that is not one object, an unknown field, and a formula the databases app finds invalid.
func TestRun_Formulas_Create_Refused(t *testing.T) {
	dataDir := newKanban(t)
	create := func(stdin string, args ...string) (int, string) {
		code, _, stderr := runCLIWithStdin(t, stdin,
			append([]string{"bdb", "--data-dir", dataDir, "formulas", "create", "Kanban"}, args...)...)
		return code, stderr
	}

	if code, stderr := create(openTasksFormula); code != exitUsage || !strings.Contains(stderr, "--file") {
		t.Errorf("create without --file = %d, stderr %q; want a usage error naming --file", code, stderr)
	}
	if code, stderr := create("  \n", "--file", "-"); code != exitUsage ||
		!strings.Contains(stderr, "the formula is empty") {
		t.Errorf("create from an empty stdin = %d, stderr %q; want a usage error", code, stderr)
	}
	if code, stderr := create(`[1]`, "--file", "-"); code != exitFailure ||
		!strings.Contains(stderr, "reading the formula") {
		t.Errorf("create from a JSON array = %d, stderr %q; want a decoding error", code, stderr)
	}
	unknown := `{"name": "open-tasks", "desc": "Tasks not done yet", "sql": "SELECT name FROM tasks"}`
	if code, stderr := create(unknown, "--file", "-"); code != exitFailure ||
		!strings.Contains(stderr, `unknown field "desc"`) {
		t.Errorf("create with an unknown field = %d, stderr %q; want the unknown field named", code, stderr)
	}
	invalid := `{"name": "Open Tasks", "description": "Tasks not done yet", "sql": "SELECT name FROM tasks"}`
	if code, stderr := create(invalid, "--file", "-"); code != exitFailure || !strings.Contains(stderr, "in Kanban:") ||
		!strings.Contains(stderr, "name") {
		t.Errorf("create with an invalid name = %d, stderr %q; want the databases app's refusal", code, stderr)
	}
	if _, stdout, _ := runCLI(t, "bdb", "--data-dir", dataDir, "formulas", "list", "Kanban"); strings.Contains(stdout,
		"open-tasks") || strings.Contains(stdout, "Open Tasks") {
		t.Errorf("formulas list = %q, want no formula created by a refused command", stdout)
	}
}

// TestRun_Formulas_Update checks that formulas update changes only the fields its JSON object holds, replaces the
// parameters, resets a display with null and refuses any other display, and points at the list for an unknown formula.
func TestRun_Formulas_Update(t *testing.T) {
	dataDir := newKanban(t)
	update := func(formula, spec string) (int, string, string) {
		return runCLIWithStdin(t, spec, "bdb", "--data-dir", dataDir, "formulas", "update", "Kanban", formula,
			"--file", "-")
	}
	if code, _, stderr := runCLIWithStdin(t, openTasksFormula,
		"bdb", "--data-dir", dataDir, "formulas", "create", "Kanban", "--file", "-"); code != exitOK {
		t.Fatalf("formulas create = %d, stderr %q", code, stderr)
	}

	code, stdout, stderr := update("open-tasks", `{
		"sql": "SELECT name FROM tasks WHERE status = :status ORDER BY position",
		"params": [{"name": "status", "type": "text", "source": "prompt", "options": ["todo", "done"]}]
	}`)
	if code != exitOK || stdout != "Updated formula open-tasks in Kanban (view)\n" {
		t.Fatalf("formulas update = %d %q, stderr %q", code, stdout, stderr)
	}
	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "formulas", "show", "Kanban", "open-tasks", "--json")
	var f struct {
		Description string `json:"description"`
		SQL         string `json:"sql"`
		Params      []struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(stdout), &f); code != exitOK || err != nil ||
		f.Description != "Tasks not done yet" || !strings.Contains(f.SQL, ":status") || len(f.Params) != 1 ||
		f.Params[0].Name != "status" {
		t.Errorf("formulas show after update = %d %q (%v), stderr %q; want the new SQL and parameter, the description "+
			"kept", code, stdout, err, stderr)
	}

	if code, _, stderr = update("task-board", `{"display": null}`); code != exitOK {
		t.Errorf("resetting the board's display = %d, stderr %q", code, stderr)
	}
	if code, _, stderr = update("task-board", `{"display": "list"}`); code != exitFailure ||
		!strings.Contains(stderr, "display must be an object or null") {
		t.Errorf("a string display = %d, stderr %q; want it refused", code, stderr)
	}

	code, _, stderr = update("nope", `{"description": "Nothing"}`)
	want := `in Kanban: no formula named "nope"; ` + "`bdb formulas Kanban`"
	if code != exitFailure || !strings.Contains(stderr, want) {
		t.Errorf("updating an unknown formula = %d, stderr %q; want a pointer to the list", code, stderr)
	}
}

// TestRun_Formulas_Delete checks that formulas delete removes a formula from the list, and points at the list for a
// formula that is not there.
func TestRun_Formulas_Delete(t *testing.T) {
	dataDir := newKanban(t)
	if code, _, stderr := runCLIWithStdin(t, openTasksFormula,
		"bdb", "--data-dir", dataDir, "formulas", "create", "Kanban", "--file", "-"); code != exitOK {
		t.Fatalf("formulas create = %d, stderr %q", code, stderr)
	}

	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "formulas", "delete", "Kanban", "open-tasks")
	if code != exitOK || stdout != "Deleted formula open-tasks from Kanban\n" {
		t.Fatalf("formulas delete = %d %q, stderr %q", code, stdout, stderr)
	}
	if _, stdout, _ = runCLI(t, "bdb", "--data-dir", dataDir, "formulas", "list", "Kanban"); strings.Contains(stdout,
		"open-tasks") {
		t.Errorf("formulas list after delete = %q, want open-tasks gone", stdout)
	}

	code, _, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "formulas", "delete", "Kanban", "open-tasks")
	want := `in Kanban: no formula named "open-tasks"; ` + "`bdb formulas Kanban`"
	if code != exitFailure || !strings.Contains(stderr, want) {
		t.Errorf("deleting it again = %d, stderr %q; want a pointer to the list", code, stderr)
	}
}
