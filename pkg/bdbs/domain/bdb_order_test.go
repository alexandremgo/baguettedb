package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestValidateBdbOrder(t *testing.T) {
	a := uuid.New()
	b := uuid.New()
	c := uuid.New()
	stranger := uuid.New()

	tests := []struct {
		name      string
		current   []uuid.UUID
		requested []uuid.UUID
		wantErr   bool
	}{
		{"same set rearranged", []uuid.UUID{a, b, c}, []uuid.UUID{c, a, b}, false},
		{"same set same order", []uuid.UUID{a, b, c}, []uuid.UUID{a, b, c}, false},
		{"single bdb", []uuid.UUID{a}, []uuid.UUID{a}, false},
		{"no bdbs at all", nil, nil, false},
		{"missing one", []uuid.UUID{a, b, c}, []uuid.UUID{a, b}, true},
		{"extra id the user is not a member of", []uuid.UUID{a, b}, []uuid.UUID{a, b, stranger}, true},
		{"foreign id swapped in", []uuid.UUID{a, b}, []uuid.UUID{a, stranger}, true},
		{"duplicate instead of a distinct id", []uuid.UUID{a, b}, []uuid.UUID{a, a}, true},
		{"duplicate on top of the full set", []uuid.UUID{a, b}, []uuid.UUID{a, b, b}, true},
		{"empty while the user has bdbs", []uuid.UUID{a, b}, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBdbOrder(tt.current, tt.requested)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidBdbOrder) {
					t.Errorf("err = %v, want ErrInvalidBdbOrder", err)
				}
				return
			}
			if err != nil {
				t.Errorf("err = %v, want nil", err)
			}
		})
	}
}
