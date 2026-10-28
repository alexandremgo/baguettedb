package datadir

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// TestServedFile_RecordAndCurrent checks that no record reads as none, that a record of a running process reads back
// from an owner-only file, replacing the previous one, and that a record of a process gone reads as none.
func TestServedFile_RecordAndCurrent(t *testing.T) {
	dir := t.TempDir()
	f := NewServedFile(dir)

	if _, ok, err := f.Current(); err != nil || ok {
		t.Fatalf("Current with no record = %t, %v; want none", ok, err)
	}

	for _, url := range []string{"http://127.0.0.1:3035", "http://nas.local:3035"} {
		want := domain.ServedInstance{URL: url, PID: os.Getpid()}
		if err := f.Record(want); err != nil {
			t.Fatalf("Record(%+v): %v", want, err)
		}
		if got, ok, err := f.Current(); err != nil || !ok || got != want {
			t.Errorf("Current = %+v, %t, %v; want %+v", got, ok, err, want)
		}
	}
	info, err := os.Stat(filepath.Join(dir, servedFileName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("record file: %v, %v; want mode 0600", info, err)
	}

	if err := f.Record(domain.ServedInstance{URL: "http://127.0.0.1:3035", PID: exitedPID(t)}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if got, ok, err := f.Current(); err != nil || ok {
		t.Errorf("Current of a process gone = %+v, %t, %v; want none", got, ok, err)
	}
}

// TestServedFile_Forget checks that Forget keeps the record of another process, removes the record of its own, and
// leaves no record without error.
func TestServedFile_Forget(t *testing.T) {
	dir := t.TempDir()
	f := NewServedFile(dir)
	own := domain.ServedInstance{URL: "http://127.0.0.1:3035", PID: os.Getpid()}
	if err := f.Record(own); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if err := f.Forget(own.PID + 1); err != nil {
		t.Fatalf("Forget of another process: %v", err)
	}
	if _, ok, err := f.Current(); err != nil || !ok {
		t.Errorf("Current after another process's Forget = %t, %v; want the record kept", ok, err)
	}

	if err := f.Forget(own.PID); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, servedFileName)); !os.IsNotExist(err) {
		t.Errorf("record file after Forget: %v, want it removed", err)
	}
	if err := f.Forget(own.PID); err != nil {
		t.Errorf("Forget with no record: %v, want nil", err)
	}
}

// TestServedFile_Corrupt checks that a record that isn't JSON is an error, not a missing record.
func TestServedFile_Corrupt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, servedFileName), []byte("http://127.0.0.1:3035\n"), 0o600); err != nil {
		t.Fatalf("writing a corrupt record: %v", err)
	}
	if _, _, err := NewServedFile(dir).Current(); err == nil {
		t.Error("Current of a corrupt record succeeded, want an error")
	}
}

// exitedPID returns the PID of a process that has run and exited.
func exitedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running true: %v", err)
	}
	return cmd.Process.Pid
}
