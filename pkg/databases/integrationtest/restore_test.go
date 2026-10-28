//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// insertRow inserts one row into the test table of the given database.
func insertRow(t *testing.T, stack *backupStack, databaseID uuid.UUID, v string) {
	t.Helper()
	if _, err := stack.sqliteClient.Execute(
		context.Background(), databaseID.String(), fmt.Sprintf("INSERT INTO t (v) VALUES ('%s')", v), nil,
	); err != nil {
		t.Fatalf("inserting row %q: %v", v, err)
	}
}

// rowValues returns the v column of the test table in id order.
func rowValues(t *testing.T, stack *backupStack, databaseID uuid.UUID) []string {
	t.Helper()
	res, err := stack.sqliteClient.Execute(
		context.Background(), databaseID.String(), "SELECT v FROM t ORDER BY id", nil,
	)
	if err != nil {
		t.Fatalf("selecting rows: %v", err)
	}
	out := make([]string, 0, len(res.Rows))
	for _, row := range res.Rows {
		out = append(out, row[0].Value.(string))
	}
	return out
}

// removeDatabaseFile deletes the database's on-disk files while keeping its Postgres record — the shape of a
// fresh-volume restart for that database.
func removeDatabaseFile(t *testing.T, stack *backupStack, databaseID uuid.UUID) {
	t.Helper()
	if err := stack.sqliteClient.DeleteDatabase(context.Background(), databaseID.String()); err != nil {
		t.Fatalf("removing database file: %v", err)
	}
}

// TestRestoreDatabase_RoundTrip verifies a database whose file was lost comes back from its latest snapshot with
// exactly the backed-up content — rows written after the backup are gone (the accepted RPO) — including platform
// state (skills), which lives in the same file and must survive the round-trip.
func TestRestoreDatabase_RoundTrip(t *testing.T) {
	stack := setupBackupStack(t, "restore-roundtrip", time.Hour)
	ctx := context.Background()

	db := provisionDatabase(t, stack)
	dbRecord, err := newDatabaseRepo().FindByID(ctx, db)
	if err != nil {
		t.Fatalf("resolving database record: %v", err)
	}
	userID := uuid.New()

	insertRow(t, stack, db, "before-backup")
	if _, err := stack.skillSvc.CreateSkill(ctx, userID, dbRecord.BdbID, service.SkillInput{
		Name: "groceries", Description: "how to manage the list", Body: "Always check quantities.",
	}); err != nil {
		t.Fatalf("creating skill: %v", err)
	}
	if _, err := stack.backupSvc.BackupDatabase(ctx, db, time.Now().UTC()); err != nil {
		t.Fatalf("backing up: %v", err)
	}
	insertRow(t, stack, db, "after-backup")

	removeDatabaseFile(t, stack, db)

	info, err := stack.backupSvc.RestoreDatabase(ctx, db, false)
	if err != nil {
		t.Fatalf("RestoreDatabase: %v", err)
	}
	if info.Key == "" {
		t.Error("restored snapshot info has empty key")
	}

	if got := rowValues(t, stack, db); len(got) != 1 || got[0] != "before-backup" {
		t.Errorf("restored rows = %v, want [before-backup]", got)
	}

	skills, err := stack.skillSvc.ListSkills(ctx, userID, dbRecord.BdbID)
	if err != nil {
		t.Fatalf("listing skills after restore: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "groceries" {
		t.Errorf("skills after restore = %d, want the pre-backup skill back", len(skills))
	}
}

// TestRestoreDatabase_RefusesOverwriteWithoutForce verifies a live database file is never silently replaced: the
// restore fails with ErrDatabaseFileExists and the live content stays intact.
func TestRestoreDatabase_RefusesOverwriteWithoutForce(t *testing.T) {
	stack := setupBackupStack(t, "restore-no-force", time.Hour)
	ctx := context.Background()

	db := provisionDatabase(t, stack)
	insertRow(t, stack, db, "before-backup")
	if _, err := stack.backupSvc.BackupDatabase(ctx, db, time.Now().UTC()); err != nil {
		t.Fatalf("backing up: %v", err)
	}
	insertRow(t, stack, db, "after-backup")

	_, err := stack.backupSvc.RestoreDatabase(ctx, db, false)
	if !errors.Is(err, domain.ErrDatabaseFileExists) {
		t.Fatalf("err = %v, want ErrDatabaseFileExists", err)
	}

	if got := rowValues(t, stack, db); len(got) != 2 {
		t.Errorf("live rows = %v, want both rows untouched", got)
	}
}

// TestRestoreDatabase_ForceRevertsLiveDatabase verifies a forced restore replaces a live database — including one
// with an open cached connection — and that queries afterwards observe the snapshot's content, never the replaced
// file's.
func TestRestoreDatabase_ForceRevertsLiveDatabase(t *testing.T) {
	stack := setupBackupStack(t, "restore-force", time.Hour)
	ctx := context.Background()

	db := provisionDatabase(t, stack)
	insertRow(t, stack, db, "before-backup")
	if _, err := stack.backupSvc.BackupDatabase(ctx, db, time.Now().UTC()); err != nil {
		t.Fatalf("backing up: %v", err)
	}
	// Keeps writing through the cached connection so it is open and serving this database when the forced
	// restore replaces the file underneath it.
	insertRow(t, stack, db, "after-backup")

	if _, err := stack.backupSvc.RestoreDatabase(ctx, db, true); err != nil {
		t.Fatalf("forced RestoreDatabase: %v", err)
	}

	if got := rowValues(t, stack, db); len(got) != 1 || got[0] != "before-backup" {
		t.Errorf("rows after forced restore = %v, want [before-backup]", got)
	}
}

// TestRestoreDatabase_NoSnapshots verifies restoring a never-backed-up database surfaces ErrNoSnapshots.
func TestRestoreDatabase_NoSnapshots(t *testing.T) {
	stack := setupBackupStack(t, "restore-none", time.Hour)

	db := provisionDatabase(t, stack)
	removeDatabaseFile(t, stack, db)

	_, err := stack.backupSvc.RestoreDatabase(context.Background(), db, false)
	if !errors.Is(err, domain.ErrNoSnapshots) {
		t.Errorf("err = %v, want ErrNoSnapshots", err)
	}
}

// TestAdoptDatabaseFile_RemovesStaleSidecars verifies adoption wipes leftover -wal/-shm sidecars from a previous
// incarnation of the database — a stale WAL replayed against the restored main file would corrupt it.
func TestAdoptDatabaseFile_RemovesStaleSidecars(t *testing.T) {
	stack := setupBackupStack(t, "restore-sidecars", time.Hour)
	ctx := context.Background()

	db := provisionDatabase(t, stack)
	insertRow(t, stack, db, "before-backup")
	if _, err := stack.backupSvc.BackupDatabase(ctx, db, time.Now().UTC()); err != nil {
		t.Fatalf("backing up: %v", err)
	}
	removeDatabaseFile(t, stack, db)

	// Plants junk sidecars where the old incarnation's WAL/SHM would have survived a partial cleanup.
	dbRepo := newDatabaseRepo()
	backing := getOrCreateStackBacking(t, dbRepo)
	base := filepath.Join(backing.dataDir, db.String()+".db")
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(base+suffix, []byte("stale sidecar junk"), 0o600); err != nil {
			t.Fatalf("planting stale sidecar: %v", err)
		}
	}

	if _, err := stack.backupSvc.RestoreDatabase(ctx, db, false); err != nil {
		t.Fatalf("RestoreDatabase: %v", err)
	}

	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(base + suffix); !os.IsNotExist(err) {
			t.Errorf("stale sidecar %s still present after adoption, stat err = %v", suffix, err)
		}
	}
	if got := rowValues(t, stack, db); len(got) != 1 || got[0] != "before-backup" {
		t.Errorf("restored rows = %v, want [before-backup]", got)
	}
}

// TestReconcileOnBoot_RestoresMissingFiles verifies the boot pass restores exactly the databases whose files are
// gone and leaves present ones alone.
func TestReconcileOnBoot_RestoresMissingFiles(t *testing.T) {
	stack := setupBackupStack(t, "reconcile-restore", time.Hour)
	ctx := context.Background()

	db1 := provisionDatabase(t, stack)
	db2 := provisionDatabase(t, stack)
	insertRow(t, stack, db1, "db1-row")
	if _, err := stack.backupSvc.SweepOnce(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	removeDatabaseFile(t, stack, db1)

	stats, err := stack.backupSvc.ReconcileOnBoot(ctx)
	if err != nil {
		t.Fatalf("ReconcileOnBoot: %v", err)
	}
	if stats.Scanned != 2 || stats.Restored != 1 || stats.FilePresent != 1 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want Scanned=2 Restored=1 FilePresent=1 Failed=0", stats)
	}

	if got := rowValues(t, stack, db1); len(got) != 1 || got[0] != "db1-row" {
		t.Errorf("db1 rows after reconcile = %v, want [db1-row]", got)
	}
	if got := rowValues(t, stack, db2); len(got) != 0 {
		t.Errorf("db2 rows = %v, want untouched empty table", got)
	}
}

// TestReconcileOnBoot_NoSnapshotCountedUnrecoverable verifies a missing file with no stored snapshot is counted
// as NoSnapshot — loud but not a failure, since retrying forever cannot bring the data back.
func TestReconcileOnBoot_NoSnapshotCountedUnrecoverable(t *testing.T) {
	stack := setupBackupStack(t, "reconcile-unrecoverable", time.Hour)
	ctx := context.Background()

	db := provisionDatabase(t, stack)
	removeDatabaseFile(t, stack, db)

	stats, err := stack.backupSvc.ReconcileOnBoot(ctx)
	if err != nil {
		t.Fatalf("ReconcileOnBoot: %v", err)
	}
	if stats.NoSnapshot != 1 || stats.Failed != 0 || stats.Restored != 0 {
		t.Fatalf("stats = %+v, want NoSnapshot=1 Failed=0 Restored=0", stats)
	}
}
