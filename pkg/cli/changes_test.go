package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// TestWriteChangesTable_Authors checks who changes list says made each change: "you" for the caller's own, another
// account's email, and "someone else" for an author the instance has no account for.
func TestWriteChangesTable_Authors(t *testing.T) {
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	batches := []domain.ChangeBatch{
		{ID: "b1", Kind: domain.ChangeKindData, ChangedBy: "me", Mine: true, ChangedAt: at},
		{ID: "b2", Kind: domain.ChangeKindData, ChangedBy: "bob-id", ChangedAt: at},
		{ID: "b3", Kind: domain.ChangeKindData, ChangedBy: "from-elsewhere", ChangedAt: at},
	}
	var out strings.Builder
	if err := writeChangesTable(&out, batches, map[string]string{"bob-id": "bob@example.com"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	for i, want := range []string{"you", "bob@example.com", "someone else"} {
		if !strings.Contains(lines[i+1], "  "+want+"  ") {
			t.Errorf("row %d = %q, want it made by %s", i+1, lines[i+1], want)
		}
	}
}
