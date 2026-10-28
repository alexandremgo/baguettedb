package service

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// changeLogRowFor renders e as the row the change-log readers decode, in changeLogFullColumns order.
func changeLogRowFor(e *domain.ChangeLogEntry) *domain.QueryResult {
	oldJSON, _ := json.Marshal(e.OldValues)
	newJSON, _ := json.Marshal(e.NewValues)
	row := []domain.TypedValue{
		domain.IntegerValue(e.ID), domain.TextValue(e.BatchID), domain.TextValue(string(e.Kind)),
		domain.TextValue(e.Operation), domain.TextValue(e.Origin), domain.TextValue(e.ChangedBy),
		domain.TextValue("2026-09-04T10:00:00.000Z"), domain.TextValue(e.TableName), domain.TextValue(e.RowID),
		domain.TextValue(string(oldJSON)), domain.TextValue(string(newJSON)),
		domain.TextValue(e.ObjectType), domain.TextValue(e.ObjectName), domain.TextValue(e.DDLSubtype),
		domain.TextValue(e.DDLStatement), domain.NullValue(),
	}
	return &domain.QueryResult{Rows: [][]domain.TypedValue{row}}
}

// TestUndo_ReadsAndAppliesInsideOneTransaction pins that everything deciding whether an undo may run — the stack
// peek, the batch load, and the conflict probes — executes inside the same transaction as the inverse, so no write
// can land between the check and the apply. Only the post-commit stack cleanup runs outside.
func TestUndo_ReadsAndAppliesInsideOneTransaction(t *testing.T) {
	userID := uuid.New()
	entry := &domain.ChangeLogEntry{
		ID: 7, BatchID: "b1", Kind: domain.ChangeKindDML, Operation: domain.ChangeOpUpdate,
		Origin: domain.ChangeOriginChange, ChangedBy: userID.String(), TableName: "t", RowID: "1",
		OldValues: map[string]any{"name": "milk"}, NewValues: map[string]any{"name": "bread"},
	}
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`FROM _undo_stack WHERE user_id = ?`: {
			Rows: [][]domain.TypedValue{{domain.IntegerValue(3), domain.TextValue("b1")}},
		},
		`FROM _change_log WHERE batch_id = ?`: changeLogRowFor(entry),
		`SELECT * FROM "t"`: {
			Columns: []domain.Column{{Name: "name"}},
			Rows:    [][]domain.TypedValue{{domain.TextValue("bread")}},
		},
	})
	s := newTestQueryService(exec)
	s.quotaResolver = quota.Fixed(quota.Limits{UndoStackMaxSize: 50})

	result, err := s.undo(context.Background(), "ns", userID, s.userStackPeek(userID), "stale stack")
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if result.BatchID == "" || result.AffectedRowCount != 1 {
		t.Errorf("result = %+v, want a batch id and one reversed entry", result)
	}

	// The recorded depths must be a run of 1s (the reversal) followed by a run of 0s (the post-commit housekeeping),
	// both non-empty: the first call back at depth 0 marks where the transaction ended.
	committedAt := slices.Index(exec.txDepths, 0)
	if committedAt <= 0 || committedAt == len(exec.txDepths) {
		t.Fatalf("depths = %v, want statements inside the transaction, then housekeeping outside it", exec.txDepths)
	}
	for i, depth := range exec.txDepths {
		want := 0
		if i < committedAt {
			want = 1
		}
		if depth != want {
			t.Errorf("call %d %q ran at transaction depth %d, want %d", i, exec.calls[i], depth, want)
		}
	}
	inside := exec.calls[:committedAt]
	for _, needle := range []string{
		"FROM _undo_stack", "FROM _change_log WHERE batch_id = ?", `SELECT * FROM "t"`,
		`UPDATE "t" SET "name" = ? WHERE rowid = ?`, "DELETE FROM _undo_stack", "INSERT INTO _redo_stack",
	} {
		if !slices.ContainsFunc(inside, func(sql string) bool { return strings.Contains(sql, needle) }) {
			t.Errorf("no statement containing %q ran inside the transaction; got %v", needle, inside)
		}
	}
}

// TestRevert_PrunesRetentionAndCapsTheUndoStackAfterCommit pins the housekeeping a revert runs once its transaction
// has committed: the change-log retention prune, since the revert wrote rows of its own, and the cap on the undo
// stack it pushed onto.
func TestRevert_PrunesRetentionAndCapsTheUndoStackAfterCommit(t *testing.T) {
	userID := uuid.New()
	entry := &domain.ChangeLogEntry{
		ID: 7, BatchID: "b1", Kind: domain.ChangeKindDML, Operation: domain.ChangeOpUpdate,
		Origin: domain.ChangeOriginChange, ChangedBy: userID.String(), TableName: "t", RowID: "1",
		OldValues: map[string]any{"name": "milk"}, NewValues: map[string]any{"name": "bread"},
	}
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`SELECT * FROM "t"`: {
			Columns: []domain.Column{{Name: "name"}},
			Rows:    [][]domain.TypedValue{{domain.TextValue("bread")}},
		},
	})
	s := newTestQueryService(exec)
	s.quotaResolver = quota.Fixed(quota.Limits{UndoStackMaxSize: 50})
	load := func(context.Context, internalExecutor, string) ([]*domain.ChangeLogEntry, error) {
		return []*domain.ChangeLogEntry{entry}, nil
	}

	if _, err := s.revert(context.Background(), "ns", userID, load); err != nil {
		t.Fatalf("revert: %v", err)
	}

	committedAt := slices.Index(exec.txDepths, 0)
	if committedAt <= 0 {
		t.Fatalf("depths = %v, want the reversal inside a transaction, then housekeeping outside it", exec.txDepths)
	}
	after := exec.calls[committedAt:]
	for _, needle := range []string{
		"DELETE FROM _change_log WHERE changed_at <",
		"DELETE FROM _undo_stack WHERE user_id = ?",
	} {
		if !slices.ContainsFunc(after, func(sql string) bool { return strings.Contains(sql, needle) }) {
			t.Errorf("no statement containing %q ran after the commit; got %v", needle, after)
		}
	}
}
