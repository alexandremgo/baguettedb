package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrChangeConflict signals that an undo / redo / revert against a
// _change_log entry can't proceed because the row or schema state has
// changed since the entry was recorded (e.g. another user inserted
// rows, a later ALTER replaced a column, an object with the same name
// was recreated). Wrapped at the service-layer error site so callers
// can use errors.Is. The structured payload (BlockedBy / ResolutionHints)
// travels in ChangeConflict.
var ErrChangeConflict = errors.New("change conflict: state has moved since this change was recorded")

// ChangeConflict is the structured payload describing a refused
// undo / redo / revert. The detector returns it (or nil when clear) so
// callers can resolve the blockers programmatically instead of parsing
// an error message.
//
// Fields:
//   - ChangeLogID: the _change_log row the caller tried to reverse.
//   - Kind:        the kind of the refused entry (DML or DDL).
//   - Subtype:     for DDL, one of the DDLSubtype* constants; empty for DML.
//   - Reason:      short, human-readable explanation of the conflict.
//   - BlockedBy:   change_log entries that prevent the reversal (e.g. Bob's
//     INSERTs after Alice's CREATE TABLE). Reverting them by ID clears the
//     obstruction.
//   - ResolutionHints: advisory text aimed at agents/owners, naming no tool or
//     endpoint so every surface can show it ("Revert the newer change to the row
//     first."). The hint naming the blockers is BlockersHint's, worded per surface.
type ChangeConflict struct {
	ChangeLogID     int64
	Kind            ChangeKind
	Subtype         string
	Reason          string
	BlockedBy       []ChangeLogEntry
	ResolutionHints []string
}

// maxHintBlockers is how many blocking entries BlockersHint names before summing up the rest.
const maxHintBlockers = 5

// BlockersHint returns the hint naming the change log entries in BlockedBy, at most maxHintBlockers of them followed by
// how many more, e.g. "Blocked by entries 12, 15."; "" when there are none.
//
// revert, when not nil, words how a caller reverts the entry id, and the hint says it: revert("12") for a single
// entry ("Blocked by entry 12: <revert> reverts it."), revert("<id>") for several ("…: <revert> reverts each, newest
// first.").
func (c *ChangeConflict) BlockersHint(revert func(id string) string) string {
	if len(c.BlockedBy) == 0 {
		return ""
	}
	shown := c.BlockedBy[:min(len(c.BlockedBy), maxHintBlockers)]
	ids := make([]string, len(shown))
	for i, b := range shown {
		ids[i] = strconv.FormatInt(b.ID, 10)
	}
	if len(c.BlockedBy) == 1 {
		if revert == nil {
			return "Blocked by entry " + ids[0] + "."
		}
		return fmt.Sprintf("Blocked by entry %s: %s reverts it.", ids[0], revert(ids[0]))
	}
	hint := "Blocked by entries " + strings.Join(ids, ", ")
	if more := len(c.BlockedBy) - len(shown); more > 0 {
		hint += fmt.Sprintf(" and %d more", more)
	}
	if revert == nil {
		return hint + "."
	}
	return fmt.Sprintf("%s: %s reverts each, newest first.", hint, revert("<id>"))
}
