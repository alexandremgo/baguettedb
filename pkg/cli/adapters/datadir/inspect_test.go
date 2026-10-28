package datadir

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInspect checks a data directory in use: every file found, an owner-only key, the instance store's size with its
// journal, and the bdb files counted and sized with theirs; then a key readable by others, and a data directory that
// doesn't exist, reported with nothing created.
func TestInspect(t *testing.T) {
	dir := t.TempDir()
	key, store := filepath.Join(dir, "kek"), filepath.Join(dir, "instance.sqlite")
	databases := filepath.Join(dir, "databases")
	if err := os.Mkdir(databases, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, size := range map[string]int{
		key: 32, store: 100, store + "-wal": 20,
		filepath.Join(databases, "a.db"): 300, filepath.Join(databases, "b.db"): 400,
		filepath.Join(databases, "b.db-wal"): 50,
	} {
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	d, err := Inspect(dir, key, store, databases)
	switch {
	case err != nil:
		t.Fatalf("Inspect: %v", err)
	case !d.Exists || !d.Key.Exists || !d.InstanceStore.Exists || !d.Databases.Exists:
		t.Errorf("Inspect = %+v, want every file found", d)
	case !d.KeyOwnerOnly || d.InstanceStoreBytes != 120 || d.BdbFiles != 2 || d.BdbBytes != 750:
		t.Errorf("Inspect = %+v, want an owner-only key, 120 store bytes, 2 bdb files of 750 bytes", d)
	}

	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}
	if d, err := Inspect(dir, key, store, databases); err != nil || d.KeyOwnerOnly {
		t.Errorf("Inspect with a key readable by others = %t, %v; want it reported", d.KeyOwnerOnly, err)
	}

	missing := filepath.Join(dir, "nowhere")
	d, err = Inspect(missing, filepath.Join(missing, "kek"), filepath.Join(missing, "instance.sqlite"), "")
	if err != nil || d.Exists || d.Key.Exists || d.InstanceStore.Exists || d.Databases.Exists {
		t.Errorf("Inspect of a missing directory = %+v, %v; want nothing found", d, err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("after Inspect, stat %s = %v; want nothing created", missing, err)
	}
}
