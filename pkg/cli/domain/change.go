package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Change kinds, as the change log records them.
const (
	ChangeKindData   = "data"
	ChangeKindSchema = "schema"
)

// ChangeBatch is one change someone made to a bdb: everything one statement or request touched, undone or reverted
// as a unit.
type ChangeBatch struct {
	ID string

	// Kind is ChangeKindData or ChangeKindSchema.
	Kind string

	// Origin says how the change came about: "change" for a write, "undo", "redo" or "revert" for one that reversed
	// or re-applied another.
	Origin string

	// ChangedBy is the ID of the account that made the change; Mine reports whether it is the caller's.
	ChangedBy string
	Mine      bool

	ChangedAt time.Time
	Entries   []ChangeEntry

	// Partial reports that Entries holds only part of the batch: the listing reached the most entries it reads.
	Partial bool
}

// ChangeEntry is one row or schema object a change touched.
type ChangeEntry struct {
	// ID identifies the entry in the change log, for reverting it alone.
	ID int64

	// Operation is INSERT, UPDATE or DELETE for a row, or the schema operation (CREATE, ALTER, DROP).
	Operation string

	// Table is the table a row change touched, or the schema object a schema change touched (ObjectType says
	// which kind of object).
	Table      string
	ObjectType string

	// RowID, OldValues and NewValues describe a row change: OldValues is empty for an insert, NewValues for a delete.
	RowID     string
	OldValues map[string]any
	NewValues map[string]any

	// Statement is the SQL of a schema change.
	Statement string
}

// ChangeFilter narrows a change listing: to one table, one kind of change, the caller's own changes, and at most
// Limit batches (0 for a default).
type ChangeFilter struct {
	Table string
	Kind  string
	Mine  bool
	Limit int
}

// ErrChangeNotFound reports a change reference that matches no change.
var ErrChangeNotFound = errors.New("no change")

// ErrNothingToUndo reports an undo with no change left to undo.
var ErrNothingToUndo = errors.New("nothing to undo")

// ErrNothingToRedo reports a redo with no undone change to redo.
var ErrNothingToRedo = errors.New("nothing to redo")

// ChangeConflictError reports a change that cannot be reversed because the data or schema has moved on since it was
// made: Reason says how, Hints how to get past it, and BlockedBy lists the IDs of the later change log entries in the
// way, as far as they are known.
type ChangeConflictError struct {
	Reason    string
	Hints     []string
	BlockedBy []int64
}

// maxBlockersNamed is how many blocking entries a conflict's message names before summing up the rest.
const maxBlockersNamed = 5

// Error returns the reason, followed by the hints and the blocking entries.
func (e *ChangeConflictError) Error() string {
	return e.Message(nil)
}

// Message returns the reason, followed by the hints and the blocking entries, at most maxBlockersNamed of them then
// how many more. revert, when not nil, words the command that reverts the entry id, and the message says it:
// revert("12") for a single entry, revert("<id>") for several.
func (e *ChangeConflictError) Message(revert func(id string) string) string {
	msg := "the change can't be reversed: " + e.Reason
	if len(e.Hints) > 0 {
		msg += ". " + strings.Join(e.Hints, " ")
	}
	if len(e.BlockedBy) == 0 {
		return msg
	}
	shown := e.BlockedBy[:min(len(e.BlockedBy), maxBlockersNamed)]
	ids := make([]string, len(shown))
	for i, id := range shown {
		ids[i] = strconv.FormatInt(id, 10)
	}
	if len(e.BlockedBy) == 1 {
		if revert == nil {
			return msg + " Blocked by entry " + ids[0] + "."
		}
		return fmt.Sprintf("%s Blocked by entry %s: %s reverts it.", msg, ids[0], revert(ids[0]))
	}
	msg += " Blocked by entries " + strings.Join(ids, ", ")
	if more := len(e.BlockedBy) - len(shown); more > 0 {
		msg += fmt.Sprintf(" and %d more", more)
	}
	if revert == nil {
		return msg + "."
	}
	return fmt.Sprintf("%s: %s reverts each, newest first.", msg, revert("<id>"))
}

// AmbiguousBatchError reports a batch reference that is a prefix of several batch IDs.
type AmbiguousBatchError struct {
	Ref     string
	Matches []string
}

// Error lists the matching batch IDs.
func (e *AmbiguousBatchError) Error() string {
	return fmt.Sprintf("%q matches several changes: %s; give more of the batch ID", e.Ref,
		strings.Join(e.Matches, ", "))
}

// ResolveBatchID returns the ID of the batch among batches that ref names: its full ID, or a prefix of it, in any
// case. Several matches are an *AmbiguousBatchError; none, or an empty ref, wraps ErrChangeNotFound.
func ResolveBatchID(ref string, batches []ChangeBatch) (string, error) {
	prefix := strings.ToLower(ref)
	var matches []string
	for _, b := range batches {
		if prefix != "" && strings.HasPrefix(b.ID, prefix) {
			matches = append(matches, b.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("%w with a batch ID starting with %q among the recent ones", ErrChangeNotFound, ref)
	case 1:
		return matches[0], nil
	default:
		return "", &AmbiguousBatchError{Ref: ref, Matches: matches}
	}
}
