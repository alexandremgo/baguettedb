package datadir

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpenedFiles_Changed checks that watched files written in place are unchanged, and that a file replaced by
// another under its name, or removed, is reported, with its path; an empty path is not watched.
func TestOpenedFiles_Changed(t *testing.T) {
	dir := t.TempDir()
	kek, store := filepath.Join(dir, "kek"), filepath.Join(dir, "instance.sqlite")
	for _, path := range []string{kek, store} {
		if err := os.WriteFile(path, []byte("v1"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Held open for the whole test, as the instance holds its store, so the replacement cannot reuse its inode.
	held, err := os.Open(store)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := held.Close(); err != nil {
			t.Error(err)
		}
	}()

	var o OpenedFiles
	if err := o.Watch(kek, "", store); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if err := os.WriteFile(store, []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if path, changed := o.Changed(); changed {
		t.Errorf("Changed after writing in place = %s, want none", path)
	}

	if err := os.Rename(store, store+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store, []byte("another"), 0o600); err != nil {
		t.Fatal(err)
	}
	if path, changed := o.Changed(); !changed || path != store {
		t.Errorf("Changed after replacing the store = %q, %t; want %s", path, changed, store)
	}

	var gone OpenedFiles
	if err := gone.Watch(kek); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if err := os.Remove(kek); err != nil {
		t.Fatal(err)
	}
	if path, changed := gone.Changed(); !changed || path != kek {
		t.Errorf("Changed after removing the key = %q, %t; want %s", path, changed, kek)
	}
}
