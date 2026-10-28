package domain

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = 12
const minPasswordLength = 8

// HashPassword hashes a plaintext password using bcrypt.
func HashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hashing password: %w", err)
	}
	return string(hash), nil
}

// CheckPassword compares a plaintext password against a bcrypt hash.
// Returns nil on match, ErrInvalidCredentials on mismatch.
func CheckPassword(hash, plain string) error {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain))
	if err != nil {
		return ErrInvalidCredentials
	}
	return nil
}

// ValidatePasswordStrength checks that the password meets minimum requirements.
func ValidatePasswordStrength(plain string) error {
	if len(plain) < minPasswordLength {
		return fmt.Errorf("%w: must be at least %d characters", ErrPasswordTooWeak, minPasswordLength)
	}
	return nil
}
