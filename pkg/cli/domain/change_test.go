package domain

import (
	"errors"
	"testing"
)

// TestResolveBatchID checks that a batch is named by its full ID or any prefix of it, in any case, that a prefix
// shared by several batches is ambiguous, and that no match or an empty reference is not found.
func TestResolveBatchID(t *testing.T) {
	batches := []ChangeBatch{
		{ID: "1a2b3c4d-0000-4000-8000-000000000001"},
		{ID: "1a2b9999-0000-4000-8000-000000000002"},
		{ID: "5e6f7a8b-0000-4000-8000-000000000003"},
	}
	for ref, want := range map[string]string{
		"5e6f":                                 batches[2].ID,
		"1A2B3C":                               batches[0].ID,
		"1a2b9999-0000-4000-8000-000000000002": batches[1].ID,
	} {
		if got, err := ResolveBatchID(ref, batches); err != nil || got != want {
			t.Errorf("ResolveBatchID(%q) = %q, %v; want %q", ref, got, err, want)
		}
	}
	if _, err := ResolveBatchID("1a2b", batches); !isAmbiguousBatch(err) {
		t.Errorf("ResolveBatchID(1a2b) error = %v, want an *AmbiguousBatchError", err)
	}
	for _, ref := range []string{"ffff", ""} {
		if _, err := ResolveBatchID(ref, batches); !errors.Is(err, ErrChangeNotFound) {
			t.Errorf("ResolveBatchID(%q) error = %v, want ErrChangeNotFound", ref, err)
		}
	}
}

// isAmbiguousBatch reports whether err is an *AmbiguousBatchError.
func isAmbiguousBatch(err error) bool {
	_, ok := errors.AsType[*AmbiguousBatchError](err)
	return ok
}
