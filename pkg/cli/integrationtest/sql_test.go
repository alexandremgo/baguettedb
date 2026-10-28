//go:build integration

package integrationtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// newBdbWithPlaces creates, in a fresh data directory, a blank bdb named Places holding a places table with two
// rows (Paris with a NULL note, Lyon with an empty one), and returns the data directory and the bdb's ID.
func newBdbWithPlaces(t *testing.T) (dataDir, bdbID string) {
	t.Helper()
	clearDataDirEnv(t)
	dataDir = filepath.Join(t.TempDir(), ".baguettedb")

	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "create", "Places")
	created := regexp.MustCompile(`\(([0-9a-f-]{36})\)`).FindStringSubmatch(stdout)
	if code != exitOK || created == nil {
		t.Fatalf("bdbs create Places = %d %q, stderr %q", code, stdout, stderr)
	}
	for _, step := range []struct{ args, want string }{
		{"schema|CREATE TABLE places (id INTEGER PRIMARY KEY, name TEXT NOT NULL, note TEXT, score REAL)",
			`^Schema updated \(batch [0-9a-f-]{36}\)\n$`},
		{"data|INSERT INTO places (name, note, score) VALUES ('Paris', NULL, 4.5), ('Lyon', '', 4)",
			`^2 rows changed, last inserted rowid 2 \(batch [0-9a-f-]{36}\)\n$`},
	} {
		kind, sql, _ := strings.Cut(step.args, "|")
		code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "sql", kind, "Places", sql)
		if code != exitOK || !regexp.MustCompile(step.want).MatchString(stdout) {
			t.Fatalf("sql %s = %d %q, stderr %q; want %s", kind, code, stdout, stderr, step.want)
		}
	}
	return dataDir, created[1]
}

// TestRun_SQL_Query checks reads: the table output with NULL told apart from an empty string and the row count on
// stderr; bdb q as sql query; the bdb named in any case or by an ID prefix; the SQL read from a file or from stdin;
// and the JSON output, an array of rows (empty for no rows), each an object keyed by column in the result's order,
// with <, > and & as they are.
func TestRun_SQL_Query(t *testing.T) {
	dataDir, bdbID := newBdbWithPlaces(t)
	const sql = "SELECT name, note, score FROM places ORDER BY id"

	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "sql", "query", "Places", sql)
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if code != exitOK || len(lines) != 3 || !hasRow(lines, []string{"name", "note", "score"}) ||
		!hasRow(lines, []string{"Paris", "NULL", "4.5"}) || !hasRow(lines, []string{"Lyon", "4"}) {
		t.Fatalf("sql query = %d %q, stderr %q; want the header, Paris with NULL and Lyon with an empty note", code,
			stdout, stderr)
	}
	if stderr != "(2 rows)\n" {
		t.Errorf("sql query stderr = %q, want the row count", stderr)
	}

	sqlFile := filepath.Join(t.TempDir(), "report.sql")
	if err := os.WriteFile(sqlFile, []byte(sql+";\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"q", "places", sql},
		{"q", bdbID[:6], sql},
		{"sql", "query", "PLACES", "--file", sqlFile},
	} {
		if code, got, stderr := runCLI(t, append([]string{"bdb", "--data-dir", dataDir}, args...)...); got != stdout {
			t.Errorf("%v = %d %q, stderr %q; want the sql query output", args, code, got, stderr)
		}
	}
	code, got, stderr := runCLIWithStdin(t, sql, "bdb", "--data-dir", dataDir, "q", "Places", "--file", "-")
	if got != stdout {
		t.Errorf("q --file - = %d %q, stderr %q; want the sql query output", code, got, stderr)
	}

	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "q", "Places", sql, "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); code != exitOK || err != nil {
		t.Fatalf("q --json = %d %q (%v), stderr %q; want a JSON array", code, stdout, err, stderr)
	}
	if len(rows) != 2 || rows[0]["name"] != "Paris" || rows[0]["note"] != nil || rows[0]["score"] != 4.5 ||
		rows[1]["note"] != "" {
		t.Errorf("q --json = %+v, want both rows with their values", rows)
	}
	if strings.Index(stdout, `"name": "Paris"`) > strings.Index(stdout, `"note": null`) ||
		strings.Index(stdout, `"note": null`) > strings.Index(stdout, `"score": 4.5`) {
		t.Errorf("q --json %q does not keep the column order in rows", stdout)
	}

	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "q", "Places", sql+" LIMIT 0", "--json")
	if code != exitOK || stdout != "[]\n" {
		t.Errorf("q --json with no rows = %d %q, stderr %q; want []", code, stdout, stderr)
	}

	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "q", "Places", `SELECT '<b> & </b>' AS "a<b>"`,
		"--json")
	if code != exitOK || !strings.Contains(stdout, `"a<b>": "<b> & </b>"`) {
		t.Errorf("q --json of <, > and & = %d %q, stderr %q; want them unescaped", code, stdout, stderr)
	}
}

// TestRun_SQL_Data checks that sql data prints how many rows a change touched and its batch, any rows RETURNING gives
// before the count (with the true count, in text and JSON), and that its JSON carries the changed rows with their
// values before and after.
func TestRun_SQL_Data(t *testing.T) {
	dataDir, _ := newBdbWithPlaces(t)

	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir,
		"sql", "data", "Places", "UPDATE places SET score = 5 WHERE name = 'Lyon'")
	if code != exitOK || !regexp.MustCompile(`^1 row changed \(batch [0-9a-f-]{36}\)\n$`).MatchString(stdout) {
		t.Errorf("sql data = %d %q, stderr %q; want the count and the batch", code, stdout, stderr)
	}

	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir,
		"sql", "data", "Places", "UPDATE places SET score = score + 1 RETURNING name, score")
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if code != exitOK || len(lines) != 4 || !hasRow(lines, []string{"Paris", "5.5"}) ||
		!hasRow(lines, []string{"Lyon", "6"}) || !strings.HasPrefix(lines[3], "2 rows changed (batch ") {
		t.Errorf("sql data with RETURNING = %d %q, stderr %q; want both returned rows, then 2 rows changed", code,
			stdout, stderr)
	}
	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir,
		"sql", "data", "Places", "INSERT INTO places (name) VALUES ('Nice') RETURNING id", "--json")
	var returned struct {
		Rows             []map[string]any `json:"rows"`
		AffectedRowCount int64            `json:"affected_row_count"`
	}
	if err := json.Unmarshal([]byte(stdout), &returned); code != exitOK || err != nil ||
		len(returned.Rows) != 1 || returned.Rows[0]["id"] != float64(3) || returned.AffectedRowCount != 1 {
		t.Errorf("sql data RETURNING --json = %d %q (%v), stderr %q; want the new row's id and a count of 1", code,
			stdout, err, stderr)
	}

	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir,
		"sql", "data", "Places", "DELETE FROM places WHERE name = 'Paris'", "--json")
	var doc struct {
		AffectedRowCount *int64 `json:"affected_row_count"`
		BatchID          string `json:"batch_id"`
		AffectedRows     []struct {
			TableName string         `json:"table_name"`
			Operation string         `json:"operation"`
			OldValues map[string]any `json:"old_values"`
		} `json:"affected_rows"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); code != exitOK || err != nil {
		t.Fatalf("sql data --json = %d %q (%v), stderr %q", code, stdout, err, stderr)
	}
	if doc.AffectedRowCount == nil || *doc.AffectedRowCount != 1 || doc.BatchID == "" || len(doc.AffectedRows) != 1 ||
		doc.AffectedRows[0].Operation != "DELETE" || doc.AffectedRows[0].OldValues["name"] != "Paris" {
		t.Errorf("sql data --json = %q, want one deleted row, Paris, in a batch", stdout)
	}
}

// TestRun_SQL_Refusals checks that each sql subcommand refuses a statement of another kind, numbering it within a
// batch and pointing at the subcommand that runs it, refuses more than one statement in a read, and refuses a second
// RETURNING in one change; and that a refused change leaves the data untouched.
func TestRun_SQL_Refusals(t *testing.T) {
	dataDir, _ := newBdbWithPlaces(t)

	for _, tt := range []struct {
		kind, sql, hint string
	}{
		{"query", "DELETE FROM places", "the statement is a row change; run it with `bdb sql data`"},
		{"query", "SELECT 1; SELECT 2", "`bdb sql query` runs a single statement; got 2"},
		{"data", "DROP TABLE places", "the statement is a schema change; run it with `bdb sql schema`"},
		{"data", "UPDATE places SET score = 0; DROP TABLE places",
			"statement 2 is a schema change; run it with `bdb sql schema`"},
		{"data", "SELECT * FROM places", "the statement is a read; run it with `bdb sql query`"},
		{"data", "UPDATE places SET score = 0 RETURNING id; UPDATE places SET score = 1 RETURNING id",
			"RETURNING is supported on one statement per request"},
		{"schema", "DELETE FROM places", "the statement is a row change; run it with `bdb sql data`"},
	} {
		code, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "sql", tt.kind, "Places", tt.sql)
		if code != exitFailure || !strings.Contains(stderr, "in Places: ") || !strings.Contains(stderr, tt.hint) {
			t.Errorf("sql %s %q = %d, stderr %q; want %d and %q", tt.kind, tt.sql, code, stderr, exitFailure, tt.hint)
		}
	}

	_, stdout, _ := runCLI(t, "bdb", "--data-dir", dataDir,
		"q", "Places", "SELECT count(*) AS n, sum(score) AS s FROM places")
	if !strings.HasSuffix(stdout, "\n2  8.5\n") {
		t.Errorf("rows after the refusals = %q, want both rows and their scores untouched", stdout)
	}
}

// TestRun_SQL_Errors checks the errors around running SQL: a bdb reference matching nothing or several bdbs, SQL
// missing, given twice or blank (usage errors), an unreadable file, invalid SQL and a refused row change reported with
// SQLite's message alone, SQL with parameters, and a result naming a column twice under --json, which the table output
// still prints.
func TestRun_SQL_Errors(t *testing.T) {
	dataDir, _ := newBdbWithPlaces(t)
	for _, name := range []string{"Notes", "Other"} {
		if code, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "create", name); code != exitOK {
			t.Fatalf("bdbs create %s = %d, stderr %q", name, code, stderr)
		}
	}
	// Names differing only by case are refused today; older bdbs may still have them.
	execInstanceStore(t, dataDir, "UPDATE bdbs SET name = 'notes' WHERE name = 'Other'")
	emptyFile := filepath.Join(t.TempDir(), "empty.sql")
	if err := os.WriteFile(emptyFile, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		args   []string
		code   int
		stderr string
	}{
		{[]string{"q", "Work", "SELECT 1"}, exitFailure, `no bdb named "Work"`},
		{[]string{"q", "NOTES", "SELECT 1"}, exitFailure, `"NOTES" matches several bdbs`},
		{[]string{"q", "Notes", "SELECT 1"}, exitOK, "(1 row)"},
		{[]string{"q", "Places"}, exitUsage, "give the SQL as an argument or with --file"},
		{[]string{"q", "Places", "SELECT 1", "--file", emptyFile}, exitUsage, "not both"},
		{[]string{"q", "Places", "--file", emptyFile}, exitUsage, "the SQL is empty"},
		{[]string{"q", "Places", "--file", filepath.Join(dataDir, "missing.sql")}, exitFailure, "reading the SQL"},
		{[]string{"q", "Places", "SELECT nope FROM places"}, exitFailure, "Error: in Places: no such column: nope\n"},
		{[]string{"sql", "data", "Places", "INSERT INTO places (name) VALUES (NULL)"}, exitFailure,
			"Error: in Places: NOT NULL constraint failed: places.name\n"},
		{[]string{"q", "Places", "SELECT 1; SELECT nope"}, exitFailure, "in Places: statement 2: no such column: nope"},
		{[]string{"q", "Places", "SELECT * FROM places WHERE name = :name OR id = ?"}, exitFailure,
			"in Places: the SQL has parameters (name, ?), which sql commands don't take"},
		{[]string{"q", "Places", "SELECT 1 AS x, 2 AS x", "--json"}, exitFailure, `column "x" appears twice`},
		{[]string{"q", "Places", "SELECT 1 AS x, 2 AS x"}, exitOK, "(1 row)"},
	} {
		code, _, stderr := runCLI(t, append([]string{"bdb", "--data-dir", dataDir}, tt.args...)...)
		if code != tt.code || !strings.Contains(stderr, tt.stderr) {
			t.Errorf("%v = %d, stderr %q; want %d and %q", tt.args, code, stderr, tt.code, tt.stderr)
		}
	}
}

// TestRun_SQL_UnopenableFile checks that a bdb file the command can't open fails with the open's error, not as
// unsupported or invalid SQL.
func TestRun_SQL_UnopenableFile(t *testing.T) {
	dataDir, _ := newBdbWithPlaces(t)
	files, err := filepath.Glob(filepath.Join(dataDir, "databases", "*.db"))
	if err != nil || len(files) != 1 {
		t.Fatalf("bdb files = %v, %v; want one", files, err)
	}
	if err := os.Chmod(files[0], 0); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "q", "Places", "SELECT 1")
	if code != exitFailure || !strings.Contains(stderr, "unable to open database file") ||
		strings.Contains(stderr, "unsupported statement") {
		t.Errorf("q on an unopenable file = %d, stderr %q; want %d and the open's error alone", code, stderr,
			exitFailure)
	}
}
