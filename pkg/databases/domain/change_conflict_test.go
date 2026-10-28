package domain

import "testing"

// TestChangeConflict_BlockersHint checks the hint naming the blocking entries: none for no blockers, "entry" for one,
// "entries" for several, at most five IDs followed by how many more, and, given a way to revert one, that way with
// the entry's ID, or with "<id>" and "newest first" for several.
func TestChangeConflict_BlockersHint(t *testing.T) {
	blockedBy := func(ids ...int64) *ChangeConflict {
		c := &ChangeConflict{BlockedBy: make([]ChangeLogEntry, len(ids))}
		for i, id := range ids {
			c.BlockedBy[i].ID = id
		}
		return c
	}
	revert := func(id string) string { return "`revert " + id + "`" }
	for _, tt := range []struct {
		conflict *ChangeConflict
		revert   func(string) string
		want     string
	}{
		{blockedBy(), revert, ""},
		{blockedBy(7), nil, "Blocked by entry 7."},
		{blockedBy(7, 9), nil, "Blocked by entries 7, 9."},
		{blockedBy(1, 2, 3, 4, 5, 6, 7), nil, "Blocked by entries 1, 2, 3, 4, 5 and 2 more."},
		{blockedBy(7), revert, "Blocked by entry 7: `revert 7` reverts it."},
		{blockedBy(7, 9), revert, "Blocked by entries 7, 9: `revert <id>` reverts each, newest first."},
	} {
		if got := tt.conflict.BlockersHint(tt.revert); got != tt.want {
			t.Errorf("BlockersHint(%d blockers, revert %v) = %q, want %q", len(tt.conflict.BlockedBy),
				tt.revert != nil, got, tt.want)
		}
	}
}
