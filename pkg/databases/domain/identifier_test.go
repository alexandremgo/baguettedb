package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateIdentifier(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{"single letter", "a", nil},
		{"simple name", "groceries", nil},
		{"uppercase", "Groceries", nil},
		{"with digits and underscores", "audit_2026_q3", nil},
		{"reserved keyword", "order", nil},
		{"max length", strings.Repeat("a", IdentifierMaxLen), nil},
		{"over max length", strings.Repeat("a", IdentifierMaxLen+1), ErrUnsupportedStatement},
		{"empty", "", ErrUnsupportedStatement},
		{"leading digit", "1table", ErrUnsupportedStatement},
		{"leading underscore", "_private", ErrUnsupportedStatement},
		{"space", "my table", ErrUnsupportedStatement},
		{"accent", "café", ErrUnsupportedStatement},
		{"emoji", "notes🥖", ErrUnsupportedStatement},
		{"single quote", "it's", ErrUnsupportedStatement},
		{"double quote", `a"b`, ErrUnsupportedStatement},
		{"dash", "my-table", ErrUnsupportedStatement},
		{"dot", "my.table", ErrUnsupportedStatement},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateIdentifier(tt.input)
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("ValidateIdentifier(%q) returned error: %v, want nil", tt.input, err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidateIdentifier(%q) = %v, want error wrapping %v", tt.input, err, tt.wantErr)
				return
			}
			if !strings.Contains(err.Error(), "identifier") {
				t.Errorf("ValidateIdentifier(%q) error %q does not name the identifier rule", tt.input, err)
			}
		})
	}
}

func TestQuoteIdentifier(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple name", "notes", `"notes"`},
		{"reserved keyword", "order", `"order"`},
		{"embedded double quote", `a"b`, `"a""b"`},
		{"only a double quote", `"`, `""""`},
		{"multiple double quotes", `a"b"c`, `"a""b""c"`},
		{"single quotes pass through", "it's", `"it's"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := QuoteIdentifier(tt.input); got != tt.want {
				t.Errorf("QuoteIdentifier(%q) = %s, want %s", tt.input, got, tt.want)
			}
		})
	}
}
