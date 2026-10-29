//go:build integration

package integrationtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_Skills_ListAndShow checks that skills list prints a bdb's skills with what they cover, that the bare skills
// command prints the same, that --json carries the bodies, and that skills show prints one skill's body or points at
// the list for an unknown one.
func TestRun_Skills_ListAndShow(t *testing.T) {
	dataDir := newKanban(t)

	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "skills", "list", "Kanban")
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if code != exitOK || !strings.HasPrefix(lines[0], "NAME") ||
		!hasRowPrefix(lines, []string{"kanban-orientation", "bdb-level"}) {
		t.Fatalf("skills list = %d %q, stderr %q; want the starter's bdb-level skill", code, stdout, stderr)
	}
	if _, bare, _ := runCLI(t, "bdb", "--data-dir", dataDir, "skills", "Kanban"); bare != stdout {
		t.Errorf("bare skills = %q, want the skills list output", bare)
	}

	code, stdout, _ = runCLI(t, "bdb", "--data-dir", dataDir, "skills", "Kanban", "--json")
	var skills []struct {
		Name         string   `json:"name"`
		Body         string   `json:"body"`
		TargetTables []string `json:"target_tables"`
	}
	if err := json.Unmarshal([]byte(stdout), &skills); code != exitOK || err != nil || len(skills) != 1 ||
		skills[0].Body == "" || skills[0].TargetTables == nil {
		t.Errorf("skills --json = %d %q (%v); want the one skill with its body and an empty table list", code, stdout,
			err)
	}

	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "skills", "show", "Kanban", "kanban-orientation")
	if code != exitOK || !strings.HasPrefix(stdout, "kanban-orientation  (bdb-level)\nDescription:  ") ||
		!strings.Contains(stdout, "This bdb tracks ONE project") {
		t.Errorf("skills show = %d %q, stderr %q; want the heading, the description and the body", code, stdout, stderr)
	}

	code, _, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "skills", "show", "Kanban", "nope")
	want := `in Kanban: no skill named "nope"; ` + "`bdb skills Kanban`"
	if code != exitFailure || !strings.Contains(stderr, want) {
		t.Errorf("an unknown skill = %d, stderr %q; want a pointer to the list", code, stderr)
	}
}

// TestRun_Skills_Create checks that skills create makes a table-targeted skill from stdin and a bdb-level one from a
// file, naming what each covers, and refuses a missing description or body, an unknown table and an invalid name.
func TestRun_Skills_Create(t *testing.T) {
	dataDir := newKanban(t)

	code, stdout, stderr := runCLIWithStdin(t, "Triage new tasks by due date first.\n", "bdb", "--data-dir", dataDir,
		"skills", "create", "Kanban", "triage", "--description", "How to triage new tasks", "--table", "tasks",
		"--file", "-")
	if code != exitOK || stdout != "Created skill triage in Kanban (tables: tasks)\n" {
		t.Fatalf("skills create from stdin = %d %q, stderr %q", code, stdout, stderr)
	}

	file := filepath.Join(t.TempDir(), "naming.md")
	if err := os.WriteFile(file, []byte("Name tasks with a verb first.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "skills", "create", "Kanban", "naming",
		"--description", "How to name tasks", "--file", file)
	if code != exitOK || stdout != "Created skill naming in Kanban (bdb-level)\n" {
		t.Fatalf("skills create from a file = %d %q, stderr %q", code, stdout, stderr)
	}
	_, stdout, _ = runCLI(t, "bdb", "--data-dir", dataDir, "skills", "Kanban")
	lines := strings.Split(stdout, "\n")
	if !hasRowPrefix(lines, []string{"triage", "tasks"}) || !hasRowPrefix(lines, []string{"naming", "bdb-level"}) {
		t.Errorf("skills list = %q, want both new skills with what they cover", stdout)
	}

	create := func(args ...string) (int, string) {
		code, _, stderr := runCLIWithStdin(t, "A body.\n",
			append([]string{"bdb", "--data-dir", dataDir, "skills", "create", "Kanban"}, args...)...)
		return code, stderr
	}
	if code, stderr := create("other", "--file", "-"); code != exitUsage ||
		!strings.Contains(stderr, "--description is required") {
		t.Errorf("create without --description = %d, stderr %q; want a usage error", code, stderr)
	}
	if code, stderr := create("other", "--description", "Other"); code != exitUsage ||
		!strings.Contains(stderr, "give the skill's body with --file") {
		t.Errorf("create without --file = %d, stderr %q; want a usage error", code, stderr)
	}
	if code, stderr := create("other", "--description", "Other", "--table", "nope", "--file", "-"); code !=
		exitFailure || !strings.Contains(stderr, "in Kanban:") {
		t.Errorf("create for an unknown table = %d, stderr %q; want the databases app's refusal", code, stderr)
	}
	if code, stderr := create("Other Skill", "--description", "Other", "--file", "-"); code != exitFailure ||
		!strings.Contains(stderr, "in Kanban:") {
		t.Errorf("create with an invalid name = %d, stderr %q; want the databases app's refusal", code, stderr)
	}
}

// TestRun_Skills_Update checks that skills update changes only what its flags give: the description, the tables (and
// back to bdb-level with --no-tables), the body from a file and the name; and that it refuses no change, --table with
// --no-tables, and an unknown skill.
func TestRun_Skills_Update(t *testing.T) {
	dataDir := newKanban(t)
	if code, _, stderr := runCLIWithStdin(t, "Triage by due date.\n", "bdb", "--data-dir", dataDir, "skills",
		"create", "Kanban", "triage", "--description", "How to triage", "--file", "-"); code != exitOK {
		t.Fatalf("skills create = %d, stderr %q", code, stderr)
	}
	update := func(stdin string, args ...string) (int, string, string) {
		return runCLIWithStdin(t, stdin,
			append([]string{"bdb", "--data-dir", dataDir, "skills", "update", "Kanban"}, args...)...)
	}

	if code, stdout, stderr := update("", "triage", "--table", "tasks"); code != exitOK ||
		stdout != "Updated skill triage in Kanban (tables: tasks)\n" {
		t.Errorf("update --table = %d %q, stderr %q", code, stdout, stderr)
	}
	if code, stdout, stderr := update("", "triage", "--no-tables"); code != exitOK ||
		stdout != "Updated skill triage in Kanban (bdb-level)\n" {
		t.Errorf("update --no-tables = %d %q, stderr %q", code, stdout, stderr)
	}
	if code, _, stderr := update("Triage by priority, then due date.\n", "triage", "--description",
		"How to triage new tasks", "--file", "-"); code != exitOK {
		t.Errorf("update --description --file = %d, stderr %q", code, stderr)
	}
	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "skills", "show", "Kanban", "triage")
	if code != exitOK || !strings.Contains(stdout, "Description:  How to triage new tasks") ||
		!strings.Contains(stdout, "Triage by priority, then due date.") {
		t.Errorf("skills show after update = %d %q, stderr %q; want the new description and body", code, stdout, stderr)
	}
	if code, stdout, stderr := update("", "triage", "--name", "triage-rules"); code != exitOK ||
		stdout != "Updated skill triage-rules in Kanban (bdb-level)\n" {
		t.Errorf("update --name = %d %q, stderr %q", code, stdout, stderr)
	}

	if code, _, stderr := update("", "triage-rules"); code != exitUsage ||
		!strings.Contains(stderr, "nothing to change") {
		t.Errorf("update without a flag = %d, stderr %q; want a usage error", code, stderr)
	}
	if code, _, stderr := update("", "triage-rules", "--table", "tasks", "--no-tables"); code != exitUsage ||
		!strings.Contains(stderr, "don't go together") {
		t.Errorf("update --table --no-tables = %d, stderr %q; want a usage error", code, stderr)
	}
	code, _, stderr = update("", "triage", "--description", "Old name")
	want := `in Kanban: no skill named "triage"; ` + "`bdb skills Kanban`"
	if code != exitFailure || !strings.Contains(stderr, want) {
		t.Errorf("updating a renamed skill by its old name = %d, stderr %q; want a pointer to the list", code, stderr)
	}
}
