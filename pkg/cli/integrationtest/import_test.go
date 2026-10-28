//go:build integration

package integrationtest

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlitelocal"
)

// TestRun_BdbsImport checks bdbs import: an export, opened with its --passphrase-file, becomes a new bdb named --name,
// holding the source's rows, formulas and change history, whose writes then land in its change log. Imported into
// another instance, whose accounts its authors are not, the history says someone else made them.
//
// It also checks the refusals, none of which leaves a bdb listed: a default name, the file's, that another bdb has; an
// encrypted export with no passphrase and no terminal to ask it in; a wrong passphrase; a file that isn't a database;
// a SQLite database whose tables take the platform tables' names alone, which passes the checks and fails once
// installed, its bdb soft-deleted; a passphrase file with nothing on its first line.
func TestRun_BdbsImport(t *testing.T) {
	dataDir := newKanban(t)
	bdb := func(args ...string) (int, string, string) {
		t.Helper()
		return runCLI(t, append([]string{"bdb", "--data-dir", dataDir}, args...)...)
	}
	dir := t.TempDir()
	out, keyFile := filepath.Join(dir, "kanban.sqlite"), filepath.Join(dir, "kanban.key")
	if code, stdout, stderr := bdb("bdbs", "export", "Kanban", "--out", out, "--passphrase-file", keyFile); code !=
		exitOK {
		t.Fatalf("bdbs export = %d %q, stderr %q", code, stdout, stderr)
	}

	code, stdout, stderr := bdb("bdbs", "import", out, "--passphrase-file", keyFile, "--name", "Kanban copy")
	imported := regexp.MustCompile(`^Imported .+kanban\.sqlite as Kanban copy \([0-9a-f-]{36}\)\n$`)
	if code != exitOK || !imported.MatchString(stdout) {
		t.Fatalf("bdbs import = %d %q, stderr %q; want the new bdb named", code, stdout, stderr)
	}
	for _, args := range [][]string{
		{"sql", "query", "%s", "SELECT name FROM tasks ORDER BY position"},
		{"formulas", "%s"},
		{"changes", "%s"},
	} {
		_, source, _ := bdb(withBdb(args, "Kanban")...)
		_, copied, stderr := bdb(withBdb(args, "Kanban copy")...)
		if copied != source || source == "" {
			t.Errorf("%v on the copy = %q (stderr %q), want the source's %q", args, copied, stderr, source)
		}
	}
	insert := "INSERT INTO tasks (name, position) VALUES ('Again', 3)"
	if code, _, stderr := bdb("sql", "data", "Kanban copy", insert); code != exitOK {
		t.Fatalf("writing to the copy = %d, stderr %q", code, stderr)
	}
	if _, changes, _ := bdb("changes", "Kanban copy"); strings.Count(changes, "INSERT tasks") != 2 {
		t.Errorf("changes of the copy after a write = %q, want the copied insert and the new one", changes)
	}

	elsewhere := filepath.Join(t.TempDir(), ".baguettedb")
	if code, _, stderr := runCLI(t, "bdb", "--data-dir", elsewhere, "bdbs", "import", out, "--passphrase-file",
		keyFile); code != exitOK {
		t.Fatalf("bdbs import into another instance = %d, stderr %q", code, stderr)
	}
	if _, changes, _ := runCLI(t, "bdb", "--data-dir", elsewhere, "changes", "kanban"); !strings.Contains(changes,
		"  someone else  ") || strings.Contains(changes, "  you  ") {
		t.Errorf("changes of the bdb imported elsewhere = %q, want them made by someone else", changes)
	}

	wrongKey := filepath.Join(dir, "wrong.key")
	emptyKey := filepath.Join(dir, "empty.key")
	notes := filepath.Join(dir, "notes.txt")
	for path, content := range map[string]string{wrongKey: "not-it\n", emptyKey: "\n", notes: "not a database\n"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fake := filepath.Join(dir, "fake.sqlite")
	createPlainDatabase(t, fake, "CREATE TABLE _change_log (x)", "CREATE TABLE _formulas (y)",
		"CREATE TABLE _skills (z)", "CREATE TABLE notes (body)")
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{out, "--passphrase-file", keyFile}, exitFailure, "you already have a bdb with that name, whatever " +
			"the case; `bdb bdbs` lists them, and --name names this one"},
		{[]string{out, "--name", "No passphrase"}, exitFailure, "the file isn't a SQLite database; if it's an " +
			"encrypted bdb export, give its passphrase with --passphrase-file"},
		{[]string{out, "--name", "Wrong", "--passphrase-file", wrongKey}, exitFailure, "the file can't be opened"},
		{[]string{notes}, exitFailure, "the file is not a bdb export"},
		{[]string{fake}, exitFailure, "importing into fake, deleted since: "},
		{[]string{out, "--passphrase-file", emptyKey}, exitUsage, "holds no passphrase on its first line"},
	} {
		code, _, stderr := bdb(append([]string{"bdbs", "import"}, tc.args...)...)
		if code != tc.code || !strings.Contains(stderr, tc.want) {
			t.Errorf("bdbs import %v = %d, stderr %q; want %d and %q", tc.args, code, stderr, tc.code, tc.want)
		}
	}
	if _, list, _ := bdb("bdbs"); strings.Count(list, "\n") != 3 {
		t.Errorf("bdbs after the refusals = %q, want Kanban and its copy alone", list)
	}
}

// withBdb returns args with each %s replaced by the bdb name.
func withBdb(args []string, name string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(a, "%s", name)
	}
	return out
}

// createPlainDatabase creates an unencrypted SQLite database at path and runs statements in it.
func createPlainDatabase(t *testing.T, path string, statements ...string) {
	t.Helper()
	conn, err := sqlitelocal.OpenPlaintext(path)
	if err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Errorf("closing %s: %v", path, err)
		}
	}()
	for _, stmt := range statements {
		if _, err := conn.Exec(context.Background(), stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}
