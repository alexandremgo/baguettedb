package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

// TestResolveBdb checks each level of bdb resolution, that the first level with a match decides, and that several
// matches at that level, no match and an empty reference are errors.
func TestResolveBdb(t *testing.T) {
	groceries := Bdb{ID: uuid.MustParse("1a2b3c4d-0000-4000-8000-000000000001"), Name: "Groceries"}
	lowercase := Bdb{ID: uuid.MustParse("1a2b9999-0000-4000-8000-000000000002"), Name: "groceries"}
	sport := Bdb{ID: uuid.MustParse("5e6f7a8b-0000-4000-8000-000000000003"), Name: "Sport"}
	numeric := Bdb{ID: uuid.MustParse("9c9c9c9c-0000-4000-8000-000000000004"), Name: "5e6f"}
	twinA := Bdb{ID: uuid.MustParse("7d7d7d7d-0000-4000-8000-000000000005"), Name: "Notes"}
	twinB := Bdb{ID: uuid.MustParse("8e8e8e8e-0000-4000-8000-000000000006"), Name: "Notes"}
	all := []Bdb{groceries, lowercase, sport, numeric, twinA, twinB}

	for _, tt := range []struct {
		name string
		ref  string
		want Bdb
	}{
		{"exact name wins over the same name in another case", "groceries", lowercase},
		{"name ignoring case", "SPORT", sport},
		{"name wins over an ID prefix", "5e6f", numeric},
		{"ID prefix", "5e6f7", sport},
		{"ID prefix in upper case", "1A2B3C", groceries},
		{"short ID", groceries.ShortID(), groceries},
		{"full ID", sport.ID.String(), sport},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveBdb(tt.ref, all)
			if err != nil || got != tt.want {
				t.Errorf("ResolveBdb(%q) = %v, %v; want %v", tt.ref, got, err, tt.want)
			}
		})
	}

	t.Run("several matches at the deciding level", func(t *testing.T) {
		for _, ref := range []string{"Notes", "notes", "1a2b"} {
			_, err := ResolveBdb(ref, all)
			if amb, ok := errors.AsType[AmbiguousBdbError](err); !ok || len(amb.Matches) != 2 {
				t.Errorf("ResolveBdb(%q) error = %v, want an AmbiguousBdbError with 2 matches", ref, err)
			}
		}
	})

	t.Run("no match", func(t *testing.T) {
		for _, ref := range []string{"Work", "ffff", ""} {
			if _, err := ResolveBdb(ref, all); !errors.Is(err, ErrBdbNotFound) {
				t.Errorf("ResolveBdb(%q) error = %v, want ErrBdbNotFound", ref, err)
			}
		}
	})
}
