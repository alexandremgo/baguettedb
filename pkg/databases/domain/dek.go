package domain

import (
	"crypto/rand"
	"fmt"
)

// DEKSize is the byte length of a per-bdb data-encryption key.
// SQLCipher consumes the raw 32 bytes via sqlite3_key (no PBKDF2 derivation).
const DEKSize = 32

// GenerateDEK returns a fresh DEKSize-byte data-encryption key sourced from crypto/rand.
func GenerateDEK() ([]byte, error) {
	dek := make([]byte, DEKSize)
	if _, err := rand.Read(dek); err != nil {
		return nil, fmt.Errorf("generating DEK: %w", err)
	}
	return dek, nil
}
