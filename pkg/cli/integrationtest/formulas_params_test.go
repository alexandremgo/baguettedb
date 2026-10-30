//go:build integration

package integrationtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// addTasksFormula is an action adding several tasks at once from a group parameter, each row a name and an optional
// priority.
const addTasksFormula = `{
	"name": "add-tasks", "description": "Adds several tasks at once",
	"sql": "INSERT INTO tasks (name, priority, position) SELECT json_extract(je.value, '$.name'), ` +
	`json_extract(je.value, '$.priority'), 100 + je.key FROM json_each(:tasks) je",
	"params": [{"name": "tasks", "type": "group", "source": "prompt", "fields": [
		{"name": "name", "type": "text", "source": "prompt"},
		{"name": "priority", "type": "text", "source": "prompt", "optional": true}
	]}]
}`

// TestRun_Formulas_RunWithParamsJSON checks that formulas run takes every parameter as one JSON object: inline with
// --params, from stdin or a file with --params-file, a list as a JSON array of numbers, and a group's rows as an array
// of objects.
func TestRun_Formulas_RunWithParamsJSON(t *testing.T) {
	dataDir := newKanban(t)
	q := func(sql string) string {
		t.Helper()
		code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "q", "Kanban", sql)
		if code != exitOK {
			t.Fatalf("q %s = %d, stderr %q", sql, code, stderr)
		}
		return stdout
	}

	code, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "formulas", "run", "Kanban", "add-task", "--params",
		`{"name": "Buy bread", "priority": "high"}`)
	if code != exitOK {
		t.Fatalf("formulas run --params = %d, stderr %q", code, stderr)
	}
	if got := q("SELECT priority FROM tasks WHERE name = 'Buy bread'"); !strings.Contains(got, "high") {
		t.Errorf("the task --params added = %q, want it high priority", got)
	}

	code, _, stderr = runCLIWithStdin(t, `{"ordered_rowids": [2, 1]}`, "bdb", "--data-dir", dataDir, "formulas",
		"run", "Kanban", "reorder-tasks", "--params-file", "-")
	if code != exitOK {
		t.Fatalf("formulas run --params-file - = %d, stderr %q", code, stderr)
	}
	got := q("SELECT name FROM tasks WHERE id IN (1, 2) ORDER BY position")
	if ship, write := strings.Index(got, "Ship it"), strings.Index(got, "Write tests"); ship < 0 || write < ship {
		t.Errorf("tasks after reordering = %q, want Ship it before Write tests", got)
	}

	if code, _, stderr = runCLIWithStdin(t, addTasksFormula, "bdb", "--data-dir", dataDir, "formulas", "create",
		"Kanban", "--file", "-"); code != exitOK {
		t.Fatalf("formulas create add-tasks = %d, stderr %q", code, stderr)
	}
	file := filepath.Join(t.TempDir(), "params.json")
	rows := `{"tasks": [{"name": "Knead the dough"}, {"name": "Bake the loaves", "priority": "high"}]}`
	if err := os.WriteFile(file, []byte(rows), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "formulas", "run", "Kanban", "add-tasks",
		"--params-file", file)
	if code != exitOK {
		t.Fatalf("formulas run add-tasks --params-file = %d, stderr %q", code, stderr)
	}
	got = q("SELECT name, priority FROM tasks WHERE name IN ('Knead the dough', 'Bake the loaves') ORDER BY name")
	if !strings.Contains(got, "Knead the dough") || !strings.Contains(got, "Bake the loaves") ||
		!strings.Contains(got, "high") {
		t.Errorf("the tasks the group added = %q, want both rows, the second high priority", got)
	}
}

// TestRun_Formulas_RunWithParamsJSON_Refused checks what formulas run refuses about JSON parameters: two ways of
// giving them at once, an empty object, JSON that is not one object, and an unknown, mistyped or missing required
// parameter, each a usage error pointing at formulas show.
func TestRun_Formulas_RunWithParamsJSON_Refused(t *testing.T) {
	dataDir := newKanban(t)
	run := func(formula string, args ...string) (int, string) {
		t.Helper()
		code, _, stderr := runCLI(t,
			append([]string{"bdb", "--data-dir", dataDir, "formulas", "run", "Kanban", formula}, args...)...)
		return code, stderr
	}
	tests := []struct {
		name     string
		formula  string
		args     []string
		wantCode int
		want     []string
	}{
		{name: "--param and --params", formula: "add-task",
			args:     []string{"--param", "name=Bread", "--params", `{"priority": "high"}`},
			wantCode: exitUsage, want: []string{"only one of them"}},
		{name: "--params and --params-file", formula: "add-task",
			args:     []string{"--params", `{"name": "Bread"}`, "--params-file", "-"},
			wantCode: exitUsage, want: []string{"only one of them"}},
		{name: "empty --params", formula: "add-task", args: []string{"--params", " "},
			wantCode: exitUsage, want: []string{"the parameters are empty"}},
		{name: "a JSON array", formula: "add-task", args: []string{"--params", `[1]`},
			wantCode: exitFailure, want: []string{"want a JSON object"}},
		{name: "two JSON values", formula: "add-task", args: []string{"--params", `{"name": "A"} {"name": "B"}`},
			wantCode: exitFailure, want: []string{"more than one JSON value"}},
		{name: "an unknown parameter", formula: "add-task", args: []string{"--params", `{"name": "A", "nope": 1}`},
			wantCode: exitUsage, want: []string{`parameter "nope" is unknown`, "bdb formulas show Kanban add-task"}},
		{name: "a mistyped list", formula: "reorder-tasks", args: []string{"--params", `{"ordered_rowids": "2,1"}`},
			wantCode: exitUsage, want: []string{`parameter "ordered_rowids"`}},
		{name: "a missing required parameter", formula: "add-task", args: []string{"--params", `{}`},
			wantCode: exitUsage, want: []string{`parameter "name" needs a value`, "bdb formulas show Kanban add-task"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stderr := run(tt.formula, tt.args...)
			if code != tt.wantCode {
				t.Errorf("exit = %d, want %d; stderr %q", code, tt.wantCode, stderr)
			}
			for _, want := range tt.want {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr %q, want %q in it", stderr, want)
				}
			}
		})
	}
	_, stdout, _ := runCLI(t, "bdb", "--data-dir", dataDir, "q", "Kanban", "SELECT count(*) AS n FROM tasks")
	if !strings.Contains(stdout, "2") {
		t.Errorf("tasks after the refused runs = %q, want the two from the start only", stdout)
	}
}
