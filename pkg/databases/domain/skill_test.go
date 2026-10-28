package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateSkillName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{"empty", "", ErrSkillNameInvalid},
		{"single letter", "a", nil},
		{"valid slug", "groceries-flow", nil},
		{"valid with digits", "audit-2026", nil},
		{"max length", strings.Repeat("a", SkillNameMaxLen), nil},
		{"over max length", strings.Repeat("a", SkillNameMaxLen+1), ErrSkillNameInvalid},
		{"starts with digit", "1-skill", ErrSkillNameInvalid},
		{"starts with hyphen", "-skill", ErrSkillNameInvalid},
		{"uppercase", "Skill", ErrSkillNameInvalid},
		{"contains space", "my skill", ErrSkillNameInvalid},
		{"contains underscore", "my_skill", ErrSkillNameInvalid},
		{"contains slash", "my/skill", ErrSkillNameInvalid},
		{"contains period", "my.skill", ErrSkillNameInvalid},
		{"unicode", "café", ErrSkillNameInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSkillName(tt.input)
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("ValidateSkillName(%q) returned error: %v, want nil", tt.input, err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidateSkillName(%q) error = %v, want %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestValidateSkillDescription(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{"empty", "", ErrSkillDescriptionInvalid},
		{"single char", "x", nil},
		{"normal description", "Helps the agent organize the weekly grocery list.", nil},
		{"max length", strings.Repeat("a", SkillDescriptionMaxLen), nil},
		{"over max length", strings.Repeat("a", SkillDescriptionMaxLen+1), ErrSkillDescriptionInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSkillDescription(tt.input)
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("ValidateSkillDescription(%q) returned error: %v, want nil", tt.input, err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidateSkillDescription error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateSkillBody(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{"empty", "", ErrSkillBodyInvalid},
		{"single char", "x", nil},
		{"normal body", "# Groceries\n\nWhen the user adds an item, ...", nil},
		{"exactly max bytes", strings.Repeat("a", SkillBodyMaxBytes), nil},
		{"one byte over max", strings.Repeat("a", SkillBodyMaxBytes+1), ErrSkillBodyTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSkillBody(tt.input)
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("ValidateSkillBody returned error: %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidateSkillBody error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
