package intraprocessbdbs

import (
	"errors"
	"fmt"
	"testing"

	bdbsdomain "github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
)

// TestStartersError checks that each bdb-creation failure of the bdbs app becomes the starters domain's error for
// it, whether returned as is or wrapped further down the chain, with the limit's maximum kept, and that any other
// error comes back unchanged.
func TestStartersError(t *testing.T) {
	wrap := func(err error) error { return fmt.Errorf("creating bdb: %w", err) }
	errOther := errors.New("database down")

	tests := []struct {
		name string
		err  error
		want error
	}{
		{"name empty", bdbsdomain.ErrBdbNameEmpty, domain.ErrBdbNameEmpty},
		{"name empty, wrapped", wrap(bdbsdomain.ErrBdbNameEmpty), domain.ErrBdbNameEmpty},
		{"name too long, wrapped", wrap(bdbsdomain.ErrBdbNameTooLong), domain.ErrBdbNameTooLong},
		{"name taken, wrapped", wrap(bdbsdomain.ErrBdbNameTaken), domain.ErrBdbNameTaken},
		{"limit reached", bdbsdomain.BdbLimitReachedError{Max: 3}, domain.BdbLimitReachedError{Max: 3}},
		{"limit reached, wrapped", wrap(bdbsdomain.BdbLimitReachedError{Max: 3}), domain.BdbLimitReachedError{Max: 3}},
		{"other error", errOther, errOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := startersError(tt.err); !errors.Is(got, tt.want) {
				t.Errorf("startersError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
