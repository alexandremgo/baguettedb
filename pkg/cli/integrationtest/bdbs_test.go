//go:build integration

package integrationtest

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// createdBdb is a bdb as bdbs create --json prints it.
type createdBdb struct {
	listedBdb
	StarterKey string `json:"starter_key"`
	Formulas   []struct {
		Name string `json:"name"`
	} `json:"formulas"`
}

// listedBdb is a bdb as bdbs list --json prints it.
type listedBdb struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Role        string `json:"role"`
	Tables      []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"tables"`
}

// TestRun_Bdbs_CreateAndList checks bdbs create and bdbs list on a fresh data directory: an empty listing prints a
// hint on stderr and nothing on stdout (an empty array under --json); a blank bdb named by argument and a bdb seeded
// from a starter are created as the owner, the starter's creation reporting the starter, tables and formulas it set
// up (counted in text, listed in JSON); the listing shows both, as a table with full IDs and table counts, and as
// JSON with descriptions and tables; the bare bdbs command prints the same listing, with the same flags.
func TestRun_Bdbs_CreateAndList(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")

	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "list")
	if code != exitOK || stdout != "" || !strings.Contains(stderr, "No bdbs yet.") {
		t.Errorf("empty list = %d %q, stderr %q; want %d, no output and a hint", code, stdout, stderr, exitOK)
	}
	if code, stdout, _ := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "list", "--json"); stdout != "[]\n" {
		t.Errorf("empty list --json = %d %q, want []", code, stdout)
	}

	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir,
		"bdbs", "create", "Groceries", "--description", "Weekly shopping")
	created := regexp.MustCompile(`^Created Groceries \(([0-9a-f-]{36})\)\n$`).FindStringSubmatch(stdout)
	if code != exitOK || created == nil {
		t.Fatalf("create = %d %q, stderr %q; want the new bdb's name and ID", code, stdout, stderr)
	}
	groceriesID := created[1]

	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "create", "--starter", "kanban", "--json")
	var kanban createdBdb
	if err := json.Unmarshal([]byte(stdout), &kanban); code != exitOK || err != nil {
		t.Fatalf("create --starter --json = %d %q (%v), stderr %q; want the new bdb as JSON", code, stdout, err, stderr)
	}
	if kanban.Name != "Kanban" || kanban.Role != "owner" || kanban.Description == "" || kanban.StarterKey != "kanban" ||
		len(kanban.Tables) != 1 || len(kanban.Formulas) == 0 {
		t.Errorf("bdb from the kanban starter = %+v, want the starter's name, description, table and formulas, owned",
			kanban)
	}

	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "create", "Spots", "--starter", "places")
	summary := `^Created Spots \([0-9a-f-]{36}\) from places: [2-9] tables, [1-9][0-9]* formulas\n$`
	if code != exitOK || !regexp.MustCompile(summary).MatchString(stdout) {
		t.Errorf("create Spots --starter places = %d %q, stderr %q; want the starter and its counts", code, stdout,
			stderr)
	}

	code, stdout, _ = runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "list")
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if code != exitOK || len(lines) != 4 || !strings.HasPrefix(lines[0], "NAME") {
		t.Fatalf("list = %d %q, want a header and three rows", code, stdout)
	}
	for _, want := range [][]string{
		{"Groceries", groceriesID, "owner", "0"},
		{"Kanban", kanban.ID, "owner", "1"},
	} {
		if !hasRow(lines[1:], want) {
			t.Errorf("list %q has no row %v", stdout, want)
		}
	}

	if _, bare, _ := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs"); bare != stdout {
		t.Errorf("bare bdbs = %q, want the bdbs list output %q", bare, stdout)
	}

	code, stdout, _ = runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "list", "--json")
	if _, bare, _ := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "--json"); bare != stdout {
		t.Errorf("bare bdbs --json = %q, want the bdbs list --json output %q", bare, stdout)
	}
	var listed []listedBdb
	if err := json.Unmarshal([]byte(stdout), &listed); code != exitOK || err != nil || len(listed) != 3 {
		t.Fatalf("list --json = %d %q (%v), want three bdbs", code, stdout, err)
	}
	for _, b := range listed {
		switch b.ID {
		case groceriesID:
			if b.Description != "Weekly shopping" || b.Tables == nil || len(b.Tables) != 0 {
				t.Errorf("Groceries = %+v, want its description and an empty table list", b)
			}
		case kanban.ID:
			if len(b.Tables) != 1 || b.Tables[0].Name != "tasks" {
				t.Errorf("Kanban = %+v, want the starter's tasks table", b)
			}
		default:
			if b.Name != "Spots" || b.ID == "" {
				t.Errorf("unexpected bdb %+v", b)
			}
		}
	}
}

// TestRun_BdbsCreate_Errors checks that a blank bdb without a name or with two is a usage error, that an unknown
// starter fails, points at starters list and creates nothing, and that a name another bdb of yours has, whatever its
// case and spaces, is refused, blank or from a starter, pointing at the bdbs list.
func TestRun_BdbsCreate_Errors(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")

	code, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "create", "--description", "No name")
	if code != exitUsage || !strings.Contains(stderr, "a name is required") {
		t.Errorf("create without a name = %d, stderr %q; want %d", code, stderr, exitUsage)
	}
	if code, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "create", "One", "Two"); code != exitUsage {
		t.Errorf("create with two names = %d, stderr %q; want %d", code, stderr, exitUsage)
	}

	code, _, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "create", "--starter", "nope")
	if code != exitFailure || !strings.Contains(stderr, `"nope"`) || !strings.Contains(stderr, "bdb starters list") {
		t.Errorf("create --starter nope = %d, stderr %q; want %d naming the starter and pointing at starters list",
			code, stderr, exitFailure)
	}
	if _, stdout, _ := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "list", "--json"); stdout != "[]\n" {
		t.Errorf("list after the failures = %q, want no bdb", stdout)
	}

	if code, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "create", "Notes"); code != exitOK {
		t.Fatalf("create Notes = %d, stderr %q", code, stderr)
	}
	want := "Error: you already have a bdb with that name, whatever the case; `bdb bdbs` lists them\n"
	for _, args := range [][]string{{"notes"}, {" NOTES "}, {"nOtEs", "--starter", "notes"}} {
		code, _, stderr := runCLI(t, append([]string{"bdb", "--data-dir", dataDir, "bdbs", "create"}, args...)...)
		if code != exitFailure || stderr != want {
			t.Errorf("create %q = %d, stderr %q; want %d and %q", args, code, stderr, exitFailure, want)
		}
	}
}

// hasRow reports whether one of lines holds exactly the whitespace-separated fields want.
func hasRow(lines []string, want []string) bool {
	for _, line := range lines {
		if strings.Join(strings.Fields(line), " ") == strings.Join(want, " ") {
			return true
		}
	}
	return false
}

// TestRun_BdbsSoftDeletedHidden checks that a soft-deleted bdb, which only the trash of the web app shows, is neither
// listed nor resolved by name, so a new bdb may take its name without the name matching both.
func TestRun_BdbsSoftDeletedHidden(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")
	bdb := func(args ...string) (int, string, string) {
		t.Helper()
		return runCLI(t, append([]string{"bdb", "--data-dir", dataDir}, args...)...)
	}
	for _, name := range []string{"Gone", "Kept"} {
		if code, _, stderr := bdb("bdbs", "create", name); code != exitOK {
			t.Fatalf("bdbs create %s = %d, stderr %q", name, code, stderr)
		}
	}
	execInstanceStore(t, dataDir, `UPDATE bdbs SET deleted_at = CURRENT_TIMESTAMP WHERE name = 'Gone'`)

	if code, stdout, _ := bdb("bdbs"); code != exitOK || strings.Contains(stdout, "Gone") ||
		!strings.Contains(stdout, "Kept") {
		t.Errorf("bdbs = %d %q, want Kept alone", code, stdout)
	}
	if code, _, stderr := bdb("sql", "query", "Gone", "SELECT 1"); code != exitFailure ||
		!strings.Contains(stderr, `no bdb`) {
		t.Errorf("sql query on the soft-deleted bdb = %d, stderr %q; want it unknown", code, stderr)
	}
	if code, _, stderr := bdb("bdbs", "create", "Gone"); code != exitOK {
		t.Fatalf("bdbs create Gone again = %d, stderr %q", code, stderr)
	}
	if code, _, stderr := bdb("sql", "query", "Gone", "SELECT 1"); code != exitOK {
		t.Errorf("sql query on the new Gone = %d, stderr %q; want the new bdb alone to match", code, stderr)
	}
}

// TestRun_BdbsMissingDatabaseFile checks that a bdb whose database file is gone from the data directory fails to
// open, its query exiting with a failure that names the missing file, and that the file is not recreated empty in its
// place, which would hide the loss behind a bdb with no tables.
func TestRun_BdbsMissingDatabaseFile(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")
	if code, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "create", "Notes"); code != exitOK {
		t.Fatalf("create = %d, stderr %q", code, stderr)
	}
	files, err := filepath.Glob(filepath.Join(dataDir, "databases", "*.db"))
	if err != nil || len(files) != 1 {
		t.Fatalf("database files = %v (%v), want the one of Notes", files, err)
	}
	if err := os.Remove(files[0]); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "q", "Notes", "SELECT 1")
	if code != exitFailure || stdout != "" || !strings.Contains(stderr, "database file missing") {
		t.Errorf("query = %d %q, stderr %q; want %d and the missing file named", code, stdout, stderr, exitFailure)
	}
	if _, err := os.Stat(files[0]); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("after the query, stat %s = %v; want the file still missing", files[0], err)
	}
}

// TestRun_Bdbs_Update checks that bdbs update changes only what its flags give, the description or the name, prints
// the bdb or its JSON, and refuses no change and a name another of the owner's bdbs has, whatever the case.
func TestRun_Bdbs_Update(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")
	for _, name := range []string{"Notes", "Board"} {
		if code, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "create", name); code != exitOK {
			t.Fatalf("bdbs create %s = %d, stderr %q", name, code, stderr)
		}
	}

	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "update", "Notes", "--description",
		"Meeting notes", "--json")
	var updated struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(stdout), &updated); code != exitOK || err != nil || updated.Name != "Notes" ||
		updated.Description != "Meeting notes" {
		t.Errorf("bdbs update --description = %d %q (%v), stderr %q; want the description changed, the name kept",
			code, stdout, err, stderr)
	}

	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "update", "notes", "--name", "Journal")
	if code != exitOK || !strings.HasPrefix(stdout, "Updated Journal (") {
		t.Errorf("bdbs update --name = %d %q, stderr %q; want the bdb renamed", code, stdout, stderr)
	}
	if _, stdout, _ = runCLI(t, "bdb", "--data-dir", dataDir, "bdbs"); !strings.Contains(stdout, "Journal") ||
		strings.Contains(stdout, "Notes") {
		t.Errorf("bdbs after the rename = %q, want Journal and no Notes", stdout)
	}

	code, _, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "update", "Journal", "--name", "board")
	if code != exitFailure || !strings.Contains(stderr, "already have a bdb with that name") ||
		!strings.Contains(stderr, "`bdb bdbs` lists them") {
		t.Errorf("renaming to a taken name = %d, stderr %q; want it refused with a pointer to the list", code, stderr)
	}
	if code, _, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "update", "Journal"); code != exitUsage ||
		!strings.Contains(stderr, "nothing to change") {
		t.Errorf("bdbs update without a flag = %d, stderr %q; want a usage error", code, stderr)
	}
}
