// Package filekek wraps and unwraps data-encryption keys with a key-encryption key loaded from a local file at
// construction time. The file holds exactly KEKSize raw bytes (no base64 / hex). Wrapping uses AES-256-GCM; its output
// is `nonce ‖ ciphertext ‖ tag`, the authentication tag appended by Go's crypto/cipher AEAD inside the ciphertext
// slice. EnsureFile mints that file with a fresh random key when a deployment opts into generating its own.
//
// The package performs crypto only and emits no logs.
package filekek

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// KEKSize is the required byte length of the on-disk key-encryption key.
// AES-256 requires a 32-byte key.
const KEKSize = 32

// nonceSize is the AES-GCM standard 96-bit nonce length.
const nonceSize = 12

var (
	// errKEKFileMissing indicates the configured KEK file does not exist or is unreadable.
	errKEKFileMissing = errors.New("KEK file missing or unreadable")

	// errKEKWrongSize indicates the KEK file does not contain exactly KEKSize bytes.
	errKEKWrongSize = errors.New("KEK file has wrong size")

	// errCiphertextTooShort indicates the input ciphertext is shorter than nonceSize.
	errCiphertextTooShort = errors.New("ciphertext too short")
)

// Provider wraps and unwraps DEKs using a KEK loaded from a local file.
type Provider struct {
	aead cipher.AEAD
}

// NewProvider loads the KEK from path, validates its length, and returns
// a provider ready to wrap/unwrap. Fails fast if the file is missing,
// unreadable, or not exactly KEKSize bytes — Setup propagates this to abort
// startup.
func NewProvider(path string) (*Provider, error) {
	kek, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errKEKFileMissing, path, err)
	}
	if len(kek) != KEKSize {
		return nil, fmt.Errorf("%w: %s: got %d bytes, want %d", errKEKWrongSize, path, len(kek), KEKSize)
	}

	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, fmt.Errorf("constructing AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("constructing AES-GCM: %w", err)
	}

	return &Provider{aead: aead}, nil
}

// EnsureFile creates a KEK of KEKSize random bytes at path with mode 0600 when no file exists there, creating the
// parent directory as needed. It reports whether it created the file and never touches an existing one: two
// processes racing on the same path both end up with the single key that won the exclusive create.
func EnsureFile(path string) (created bool, err error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("checking KEK file %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("creating KEK directory for %s: %w", path, err)
	}

	kek := make([]byte, KEKSize)
	if _, err := io.ReadFull(rand.Reader, kek); err != nil {
		return false, fmt.Errorf("generating KEK: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("creating KEK file %s: %w", path, err)
	}
	if _, err := f.Write(kek); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return false, fmt.Errorf("writing KEK file %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return false, fmt.Errorf("closing KEK file %s: %w", path, err)
	}

	return true, nil
}

// Wrap encrypts plaintext under the KEK and returns nonce ‖ ciphertext ‖ tag.
func (p *Provider) Wrap(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}
	return p.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Unwrap decrypts a nonce ‖ ciphertext ‖ tag blob and returns the plaintext.
// Returns an error for inputs shorter than nonceSize and a wrapped AEAD error
// for any tampering, wrong-KEK, or malformed payload.
func (p *Provider) Unwrap(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < nonceSize {
		return nil, errCiphertextTooShort
	}
	nonce, payload := ciphertext[:nonceSize], ciphertext[nonceSize:]

	plaintext, err := p.aead.Open(nil, nonce, payload, nil)
	if err != nil {
		return nil, fmt.Errorf("AEAD open: %w", err)
	}
	return plaintext, nil
}
