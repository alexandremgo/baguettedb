//go:build integration

package integrationtest

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// batchInOutput matches the batch a command reports at the end of its line.
var batchInOutput = regexp.MustCompile(`\(batch ([0-9a-f-]{36})\)\n$`)

// placesSummary returns the row count and score total of the places table in the bdb Places.
func placesSummary(t *testing.T, dataDir string) string {
	t.Helper()
	_, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir,
		"q", "Places", "SELECT count(*) AS n, coalesce(sum(score), 0) AS s FROM places")
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("places summary = %q, stderr %q", stdout, stderr)
	}
	return strings.Join(strings.Fields(lines[1]), " ")
}

// TestRun_Changes_List checks the change listing: one line per batch, newest first, by "you", the bare changes command
// and changes list alike; the --kind, --table and --limit filters, an empty listing's note on stderr (a filtered one
// saying so), the JSON with
// every entry and its values, and an invalid --kind as a usage error.
func TestRun_Changes_List(t *testing.T) {
	dataDir, _ := newBdbWithPlaces(t)

	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "changes", "list", "Places")
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if code != exitOK || len(lines) != 3 || !strings.Contains(lines[1], " you  data    INSERT places ×2  ") ||
		!strings.Contains(lines[2], " you  schema  CREATE TABLE places  ") {
		t.Fatalf("changes list = %d %q, stderr %q; want the insert then the table creation", code, stdout, stderr)
	}
	if _, bare, _ := runCLI(t, "bdb", "--data-dir", dataDir, "changes", "Places"); bare != stdout {
		t.Errorf("bare changes = %q, want the changes list output", bare)
	}

	for _, tt := range []struct {
		args []string
		rows int
		want string
	}{
		{[]string{"--kind", "schema"}, 1, "CREATE TABLE places"},
		{[]string{"--table", "places", "--mine"}, 2, "INSERT places ×2"},
		{[]string{"--limit", "1"}, 1, "INSERT places ×2"},
	} {
		code, stdout, stderr := runCLI(t, append([]string{"bdb", "--data-dir", dataDir, "changes", "Places"},
			tt.args...)...)
		lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
		if code != exitOK || len(lines) != tt.rows+1 || !strings.Contains(lines[1], tt.want) {
			t.Errorf("changes %v = %d %q, stderr %q; want %d rows, first %q", tt.args, code, stdout, stderr,
				tt.rows, tt.want)
		}
	}
	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "changes", "Places", "--table", "nope")
	if code != exitOK || stdout != "" || stderr != "No matching changes in Places.\n" {
		t.Errorf("changes of an untouched table = %d %q, stderr %q; want a note on stderr only", code, stdout, stderr)
	}
	runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "create", "Empty")
	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "changes", "Empty")
	if code != exitOK || stdout != "" || stderr != "No changes in Empty.\n" {
		t.Errorf("changes of a new bdb = %d %q, stderr %q; want a note on stderr only", code, stdout, stderr)
	}

	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "changes", "Places", "--json")
	var batches []struct {
		BatchID string `json:"batch_id"`
		Kind    string `json:"kind"`
		Mine    bool   `json:"mine"`
		Entries []struct {
			ID        int64          `json:"id"`
			Operation string         `json:"operation"`
			NewValues map[string]any `json:"new_values"`
			Statement string         `json:"statement"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(stdout), &batches); code != exitOK || err != nil || len(batches) != 2 {
		t.Fatalf("changes --json = %d %q (%v), stderr %q; want two batches", code, stdout, err, stderr)
	}
	insert, create := batches[0], batches[1]
	if insert.Kind != "data" || !insert.Mine || len(insert.Entries) != 2 || insert.Entries[0].Operation != "INSERT" ||
		insert.Entries[0].NewValues["name"] == nil || create.Kind != "schema" ||
		!strings.HasPrefix(create.Entries[0].Statement, "CREATE TABLE places") {
		t.Errorf("changes --json = %+v, want the insert with its rows' values and the table creation with its SQL",
			batches)
	}

	code, _, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "changes", "Places", "--kind", "rows")
	if code != exitUsage {
		t.Errorf("changes --kind rows = %d, stderr %q; want %d", code, stderr, exitUsage)
	}
}

// TestRun_Changes_UndoRedo checks that changes undo reverses the last change and says so with its batch, that redo
// re-applies it, that a redo with nothing undone fails, that --anyone undoes the last change too, and that undoing a
// schema change counts no rows.
func TestRun_Changes_UndoRedo(t *testing.T) {
	dataDir, _ := newBdbWithPlaces(t)

	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "changes", "undo", "Places")
	if code != exitOK || !strings.HasPrefix(stdout, "Undid the last change: 2 rows changed (batch ") {
		t.Fatalf("changes undo = %d %q, stderr %q", code, stdout, stderr)
	}
	if got := placesSummary(t, dataDir); got != "0 0" {
		t.Errorf("places after the undo = %q, want empty", got)
	}

	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "changes", "redo", "places")
	if code != exitOK || !strings.HasPrefix(stdout, "Redid the last undone change: 2 rows changed (batch ") {
		t.Fatalf("changes redo = %d %q, stderr %q", code, stdout, stderr)
	}
	if got := placesSummary(t, dataDir); got != "2 8.5" {
		t.Errorf("places after the redo = %q, want both rows back", got)
	}

	code, _, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "changes", "redo", "Places")
	if code != exitFailure || stderr != "Error: in Places: nothing to redo\n" {
		t.Errorf("a second redo = %d, stderr %q; want nothing to redo", code, stderr)
	}

	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "changes", "undo", "Places", "--anyone")
	if code != exitOK || !strings.HasPrefix(stdout, "Undid the last change: 2 rows changed") {
		t.Errorf("changes undo --anyone = %d %q, stderr %q", code, stdout, stderr)
	}

	runCLI(t, "bdb", "--data-dir", dataDir, "sql", "schema", "Places", "CREATE TABLE visits (id INTEGER PRIMARY KEY)")
	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "changes", "undo", "Places")
	if code != exitOK || !regexp.MustCompile(`^Undid the last change \(batch [0-9a-f-]{36}\)\n$`).MatchString(stdout) {
		t.Errorf("undoing a schema change = %d %q, stderr %q; want no row count", code, stdout, stderr)
	}
}

// TestRun_Changes_Revert checks changes revert: a batch named by a prefix of its ID, a single entry with --entry, a
// change later changes block (a conflict naming the blocking entry and the command that reverts it, with no log
// line), and the usage and lookup errors, each alone on stderr and pointing at the changes list.
func TestRun_Changes_Revert(t *testing.T) {
	dataDir, _ := newBdbWithPlaces(t)

	_, stdout, _ := runCLI(t, "bdb", "--data-dir", dataDir, "sql", "data", "Places", "UPDATE places SET score = 0")
	bump := batchInOutput.FindStringSubmatch(stdout)
	if bump == nil {
		t.Fatalf("sql data = %q, want its batch", stdout)
	}
	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "changes", "revert", "Places", bump[1][:8])
	reverted := regexp.MustCompile(`^Reverted batch ` + bump[1] + `: 2 rows changed \(batch [0-9a-f-]{36}\)\n$`)
	if code != exitOK || !reverted.MatchString(stdout) {
		t.Errorf("changes revert by prefix = %d %q, stderr %q", code, stdout, stderr)
	}
	if got := placesSummary(t, dataDir); got != "2 8.5" {
		t.Errorf("places after the revert = %q, want the scores back", got)
	}

	runCLI(t, "bdb", "--data-dir", dataDir, "sql", "data", "Places", "UPDATE places SET score = 1")
	_, stdout, _ = runCLI(t, "bdb", "--data-dir", dataDir, "changes", "Places", "--json", "--limit", "1")
	var last []struct {
		Entries []struct {
			ID int64 `json:"id"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(stdout), &last); err != nil || len(last) != 1 || len(last[0].Entries) != 2 {
		t.Fatalf("last change = %q (%v), want one batch of two entries", stdout, err)
	}
	entry := strconv.FormatInt(last[0].Entries[0].ID, 10)
	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "changes", "revert", "Places", "--entry", entry)
	if code != exitOK || !strings.HasPrefix(stdout, "Reverted entry "+entry+": 1 row changed (batch ") {
		t.Errorf("changes revert --entry = %d %q, stderr %q", code, stdout, stderr)
	}
	if got := placesSummary(t, dataDir); got == "2 2" || got == "2 8.5" {
		t.Errorf("places after reverting one entry = %q, want exactly one row restored", got)
	}

	_, stdout, _ = runCLI(t, "bdb", "--data-dir", dataDir, "sql", "data", "Places",
		"UPDATE places SET score = 7 WHERE name = 'Paris'")
	first := batchInOutput.FindStringSubmatch(stdout)
	runCLI(t, "bdb", "--data-dir", dataDir,
		"sql", "data", "Places", "UPDATE places SET score = 8 WHERE name = 'Paris'")
	code, _, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "changes", "revert", "Places", first[1])
	conflict := regexp.MustCompile(`^Error: in Places: the change can't be reversed: row \d+ in "places" column ` +
		`"score" has changed \(expected 7, got 8\)\. Revert the newer change to the row first\. ` +
		"Blocked by entry (\\d+): `bdb changes revert Places --entry (\\d+)` reverts it\\.\n$")
	if code != exitFailure || !conflict.MatchString(stderr) {
		t.Errorf("reverting a change a later one blocks = %d, stderr %q; want the conflict alone, with the blocking "+
			"entry", code, stderr)
	}

	for _, tt := range []struct {
		args   []string
		code   int
		stderr string
	}{
		{[]string{"Places"}, exitUsage, "give the batch to revert, or --entry"},
		{[]string{"Places", first[1], "--entry", "1"}, exitUsage, "not both"},
		{[]string{"Places", "ffff"}, exitFailure, `in Places: no change with a batch ID starting with "ffff" among ` +
			"the recent ones; `bdb changes Places` lists them"},
		{[]string{"Places", "00000000-0000-0000-0000-000000000000"}, exitFailure, "in Places: no change with batch " +
			"ID 00000000-0000-0000-0000-000000000000; `bdb changes Places` lists them\n"},
		{[]string{"Places", "--entry", "99999"}, exitFailure, "in Places: no change with entry ID 99999"},
	} {
		code, _, stderr := runCLI(t, append([]string{"bdb", "--data-dir", dataDir, "changes", "revert"},
			tt.args...)...)
		if code != tt.code || !strings.Contains(stderr, tt.stderr) {
			t.Errorf("changes revert %v = %d, stderr %q; want %d and %q", tt.args, code, stderr, tt.code, tt.stderr)
		}
		if code == exitFailure && !strings.HasPrefix(stderr, "Error: ") {
			t.Errorf("changes revert %v printed %q before its error, want the error alone", tt.args, stderr)
		}
	}
}
