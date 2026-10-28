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

// exportedTaskCount opens the export at path with passphrase, as an external SQLCipher tool would, and returns its
// task count.
func exportedTaskCount(t *testing.T, path, passphrase string) int64 {
	t.Helper()
	conn, err := sqlitelocal.OpenWithPassphrase(path, passphrase)
	if err != nil {
		t.Fatalf("opening the export with its passphrase: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Errorf("closing the export: %v", err)
		}
	}()
	rows, err := conn.Query(context.Background(), "SELECT count(*) FROM tasks")
	if err != nil {
		t.Fatalf("counting the export's tasks: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("closing rows: %v", err)
		}
	}()
	var count int64
	if !rows.Next() {
		t.Fatalf("no count row: %v", rows.Err())
	}
	if err := rows.Scan(&count); err != nil {
		t.Fatalf("scanning the count: %v", err)
	}
	return count
}

// TestRun_BdbsExport checks bdbs export: the file opens with the printed passphrase and holds the bdb's data, an
// existing file is refused without --force, --passphrase-file writes the passphrase owner-only instead of printing
// it, several exports in a row are allowed to the local owner, and --out is required.
func TestRun_BdbsExport(t *testing.T) {
	dataDir := newKanban(t)
	out := filepath.Join(t.TempDir(), "kanban.sqlite")

	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "export", "Kanban", "--out", out)
	passphrase := regexp.MustCompile(`^Exported Kanban to .+ \(\d+ bytes\); passphrase: (\S+)\n$`)
	printed := passphrase.FindStringSubmatch(stdout)
	if code != exitOK || printed == nil {
		t.Fatalf("bdbs export = %d %q, stderr %q; want the size and passphrase", code, stdout, stderr)
	}
	if n := exportedTaskCount(t, out, printed[1]); n != 2 {
		t.Errorf("the export holds %d tasks, want 2", n)
	}

	code, _, stderr = runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "export", "Kanban", "--out", out)
	if code != exitFailure || !strings.Contains(stderr, "exists; pass --force to overwrite it") {
		t.Errorf("exporting over an existing file = %d, stderr %q; want a refusal", code, stderr)
	}

	keyFile := filepath.Join(t.TempDir(), "kanban.key")
	code, stdout, stderr = runCLI(t, "bdb", "--data-dir", dataDir,
		"bdbs", "export", "Kanban", "--out", out, "--passphrase-file", keyFile, "--force")
	if code != exitOK || !strings.HasSuffix(stdout, "; passphrase in "+keyFile+"\n") {
		t.Fatalf("bdbs export --passphrase-file --force = %d %q, stderr %q", code, stdout, stderr)
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	assertMode(t, keyFile, 0o600)
	if n := exportedTaskCount(t, out, strings.TrimSpace(string(key))); n != 2 {
		t.Errorf("the overwritten export holds %d tasks, want 2", n)
	}
	if entries, err := os.ReadDir(filepath.Dir(out)); err != nil || len(entries) != 1 {
		t.Errorf("export directory = %v (%v), want only the export, no temporary file", entries, err)
	}

	if code, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "export", "Kanban"); code != exitUsage {
		t.Errorf("bdbs export without --out = %d, stderr %q; want %d", code, stderr, exitUsage)
	}
}
