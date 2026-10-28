package filekek

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func writeKEK(t *testing.T, dir string, size int) string {
	t.Helper()
	path := filepath.Join(dir, "kek")
	key := make([]byte, size)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		t.Fatalf("write kek: %v", err)
	}
	return path
}

func newProvider(t *testing.T) *Provider {
	t.Helper()
	path := writeKEK(t, t.TempDir(), KEKSize)
	p, err := NewProvider(path)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return p
}

func TestNewProvider_MissingFile(t *testing.T) {
	_, err := NewProvider(filepath.Join(t.TempDir(), "nope"))
	if !errors.Is(err, errKEKFileMissing) {
		t.Fatalf("expected errKEKFileMissing, got %v", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected the os error to stay reachable, got %v", err)
	}
}

func TestNewProvider_WrongSize(t *testing.T) {
	for _, n := range []int{0, 1, KEKSize - 1, KEKSize + 1, 64} {
		path := writeKEK(t, t.TempDir(), n)
		_, err := NewProvider(path)
		if !errors.Is(err, errKEKWrongSize) {
			t.Fatalf("size %d: expected errKEKWrongSize, got %v", n, err)
		}
	}
}

func TestNewProvider_Success(t *testing.T) {
	newProvider(t) // fatals if construction fails
}

func TestWrapUnwrap_RoundTrip(t *testing.T) {
	p := newProvider(t)

	for _, size := range []int{0, 1, 32, 1024, 64 * 1024} {
		plaintext := make([]byte, size)
		if _, err := rand.Read(plaintext); err != nil {
			t.Fatalf("rand: %v", err)
		}

		ct, err := p.Wrap(plaintext)
		if err != nil {
			t.Fatalf("Wrap size=%d: %v", size, err)
		}

		pt, err := p.Unwrap(ct)
		if err != nil {
			t.Fatalf("Unwrap size=%d: %v", size, err)
		}

		if !bytes.Equal(pt, plaintext) {
			t.Fatalf("size=%d: round-trip mismatch", size)
		}
	}
}

func TestWrap_NonDeterministic(t *testing.T) {
	p := newProvider(t)
	plaintext := []byte("same input")

	a, err := p.Wrap(plaintext)
	if err != nil {
		t.Fatalf("Wrap a: %v", err)
	}
	b, err := p.Wrap(plaintext)
	if err != nil {
		t.Fatalf("Wrap b: %v", err)
	}

	if bytes.Equal(a, b) {
		t.Fatal("two wraps of the same plaintext produced identical ciphertext — nonce is not random")
	}
}

func TestUnwrap_WrongKEK(t *testing.T) {
	pA := newProvider(t)
	pB := newProvider(t) // different KEK

	ct, err := pA.Wrap([]byte("secret"))
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	if _, err := pB.Unwrap(ct); err == nil {
		t.Fatal("expected Unwrap with wrong KEK to fail")
	}
}

func TestUnwrap_TamperDetection(t *testing.T) {
	p := newProvider(t)

	ct, err := p.Wrap([]byte("secret"))
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	tampered := append([]byte(nil), ct...)
	tampered[len(tampered)-1] ^= 0xFF

	if _, err := p.Unwrap(tampered); err == nil {
		t.Fatal("expected Unwrap of tampered ciphertext to fail")
	}
}

func TestUnwrap_ShortCiphertext(t *testing.T) {
	p := newProvider(t)

	if _, err := p.Unwrap([]byte("too short")); !errors.Is(err, errCiphertextTooShort) {
		t.Fatalf("expected errCiphertextTooShort, got %v", err)
	}
}

// TestEnsureFile_CreatesThenLeavesAlone checks that the first call creates a KEKSize-byte 0600 file under a
// missing parent directory, that a second call reports nothing created and keeps the bytes, and that the result
// loads as a provider.
func TestEnsureFile_CreatesThenLeavesAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "kek")

	created, err := EnsureFile(path)
	if err != nil {
		t.Fatalf("EnsureFile (first): %v", err)
	}
	if !created {
		t.Fatal("first EnsureFile should report created=true")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != KEKSize {
		t.Fatalf("size = %d, want %d", info.Size(), KEKSize)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o, want 0600", perm)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	created, err = EnsureFile(path)
	if err != nil {
		t.Fatalf("EnsureFile (second): %v", err)
	}
	if created {
		t.Fatal("second EnsureFile must not report created=true")
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read again: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("second EnsureFile must leave the existing key untouched")
	}

	if _, err := NewProvider(path); err != nil {
		t.Fatalf("NewProvider must accept a generated key: %v", err)
	}
}
