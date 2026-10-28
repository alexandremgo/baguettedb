package databases

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/filekek"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

type stubKEKProvider struct{}

func (stubKEKProvider) Wrap(plaintext []byte) ([]byte, error)    { return plaintext, nil }
func (stubKEKProvider) Unwrap(ciphertext []byte) ([]byte, error) { return ciphertext, nil }

func writeKEKFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "kek")
	key := make([]byte, filekek.KEKSize)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		t.Fatalf("write kek: %v", err)
	}
	return path
}

func TestResolveKEKProvider_OverrideTakesPrecedence(t *testing.T) {
	override := stubKEKProvider{}
	got, err := resolveKEKProvider(SetupOptions{KEKProvider: override}, "", false, zerolog.Nop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != service.KEKProvider(override) {
		t.Fatalf("expected the override to be returned, got %T", got)
	}
}

func TestResolveKEKProvider_DefaultFromFile(t *testing.T) {
	path := writeKEKFile(t, t.TempDir())
	got, err := resolveKEKProvider(SetupOptions{}, path, false, zerolog.Nop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := got.(*filekek.Provider); !ok {
		t.Fatalf("expected *filekek.Provider, got %T", got)
	}
}

func TestResolveKEKProvider_EmptyPathFailsFast(t *testing.T) {
	_, err := resolveKEKProvider(SetupOptions{}, "", false, zerolog.Nop())
	if err == nil {
		t.Fatal("expected error for empty kek path")
	}
	if !strings.Contains(err.Error(), "BAGUETTEDB_DATABASES_KEK_FILE") {
		t.Fatalf("expected error to mention env var, got: %v", err)
	}
}

func TestResolveKEKProvider_MissingFileFailsFast(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := resolveKEKProvider(SetupOptions{}, missing, false, zerolog.Nop())
	if err == nil {
		t.Fatal("expected error for missing kek file")
	}
	if !strings.Contains(err.Error(), "KEK file missing") {
		t.Fatalf("expected wrapped errKEKFileMissing, got: %v", err)
	}
}

// TestResolveKEKProvider_AutogenerateCreatesMissingFile checks that the autogenerate flag turns a missing key file
// into a generated one and returns a working file provider.
func TestResolveKEKProvider_AutogenerateCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kek")
	got, err := resolveKEKProvider(SetupOptions{}, path, true, zerolog.Nop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := got.(*filekek.Provider); !ok {
		t.Fatalf("expected *filekek.Provider, got %T", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected the KEK file to be generated: %v", err)
	}
	if info.Size() != filekek.KEKSize {
		t.Fatalf("generated KEK size = %d, want %d", info.Size(), filekek.KEKSize)
	}
}

// TestResolveKEKProvider_AutogenerateLeavesExistingFileAlone checks that the autogenerate flag never rewrites a
// key file that already exists.
func TestResolveKEKProvider_AutogenerateLeavesExistingFileAlone(t *testing.T) {
	path := writeKEKFile(t, t.TempDir())
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read kek: %v", err)
	}

	if _, err := resolveKEKProvider(SetupOptions{}, path, true, zerolog.Nop()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read kek again: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("an existing KEK must never be rewritten")
	}
}
