package domain

import (
	"errors"
	"testing"
)

func TestHashPassword(t *testing.T) {
	hash, err := HashPassword("validpassword")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	if hash == "" {
		t.Fatal("HashPassword returned empty hash")
	}
	if hash == "validpassword" {
		t.Fatal("HashPassword returned plaintext password")
	}
}

func TestCheckPassword(t *testing.T) {
	password := "testpassword123"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}

	tests := []struct {
		name    string
		plain   string
		wantErr error
	}{
		{"correct password", password, nil},
		{"wrong password", "wrongpassword", ErrInvalidCredentials},
		{"empty password", "", ErrInvalidCredentials},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckPassword(hash, tt.plain)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("CheckPassword() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateEmail(t *testing.T) {
	tests := []struct {
		name    string
		email   string
		wantErr bool
	}{
		{"valid simple", "user@example.com", false},
		{"valid with subdomain", "user@mail.example.com", false},
		{"valid with plus", "user+tag@example.com", false},
		{"valid with dots", "first.last@example.com", false},
		{"missing @", "userexample.com", true},
		{"missing domain", "user@", true},
		{"missing local part", "@example.com", true},
		{"empty string", "", true},
		{"spaces only", "   ", true},
		{"double @", "user@@example.com", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEmail(tt.email)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateEmail(%q) error = %v, wantErr = %v", tt.email, err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidEmail) {
				t.Errorf("ValidateEmail(%q) error should be ErrInvalidEmail, got: %v", tt.email, err)
			}
		})
	}
}

func TestValidatePasswordStrength(t *testing.T) {
	tests := []struct {
		name    string
		plain   string
		wantErr bool
	}{
		{"valid 8 chars", "12345678", false},
		{"valid long", "averylongpassword", false},
		{"too short 7 chars", "1234567", true},
		{"empty", "", true},
		{"one char", "a", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePasswordStrength(tt.plain)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePasswordStrength() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, ErrPasswordTooWeak) {
				t.Errorf("ValidatePasswordStrength() error should wrap ErrPasswordTooWeak, got: %v", err)
			}
		})
	}
}
