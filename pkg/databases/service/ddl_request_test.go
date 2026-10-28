package service

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// assertCallOrder checks that every needle matches a distinct statement of calls, in the order given.
func assertCallOrder(t *testing.T, calls []string, needles ...string) {
	t.Helper()
	from := 0
	for _, needle := range needles {
		at := slices.IndexFunc(calls[from:], func(sql string) bool { return strings.Contains(sql, needle) })
		if at < 0 {
			t.Errorf("no statement containing %q after position %d; calls: %v", needle, from, calls)
			return
		}
		from += at + 1
	}
}

// TestExecuteDDLRequest_RecordsAndClosesTheBatchInOneTransaction pins the order of a fresh DDL request and that all
// of it runs inside one transaction: the statement runs, its columns are validated, the _change_log row is written,
// the table's change-log triggers are regenerated, then the batch close stamps any __pending__ DML rows and pushes the
// batch onto the caller's undo stack while clearing the caller's redo stack. Only the stack cap runs after the commit.
func TestExecuteDDLRequest_RecordsAndClosesTheBatchInOneTransaction(t *testing.T) {
	userID := uuid.New()
	rowID := int64(41)
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		"INSERT INTO _change_log": {LastInsertRowID: &rowID},
		"SELECT name FROM pragma_table_info": {
			Rows: [][]domain.TypedValue{{domain.TextValue("id")}, {domain.TextValue("name")}},
		},
		"PRAGMA table_info": {
			Rows: [][]domain.TypedValue{
				{domain.IntegerValue(0), domain.TextValue("id")},
				{domain.IntegerValue(1), domain.TextValue("name")},
			},
		},
	})
	s := newTestQueryService(exec)
	s.quotaResolver = quota.Fixed(quota.Limits{UndoStackMaxSize: 50})
	stmt := domain.ClassifiedStatement{
		SQL: "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)", Type: domain.StatementDDL,
		DDLType: domain.DDLCreateTable, Tables: []string{"items"},
	}

	result, _, err := s.executeDDLRequest(context.Background(), "ns", userID, []domain.ClassifiedStatement{stmt})
	if err != nil {
		t.Fatalf("executeDDLRequest: %v", err)
	}
	if result.BatchID == "" {
		t.Errorf("result = %+v, want a batch id", result)
	}

	committedAt := slices.Index(exec.txDepths, 0)
	if committedAt <= 0 || committedAt == len(exec.txDepths) {
		t.Fatalf("depths = %v, want statements inside the transaction, then housekeeping outside it", exec.txDepths)
	}
	inside := exec.calls[:committedAt]
	assertCallOrder(t, inside,
		"CREATE TABLE items",
		"SELECT name FROM pragma_table_info",
		"INSERT INTO _change_log",
		"CREATE TRIGGER",
		"UPDATE _change_log SET changed_by = ?, batch_id = ?, origin = ?",
		"INSERT INTO _undo_stack",
		"DELETE FROM _redo_stack",
	)
	for i, depth := range exec.txDepths[:committedAt] {
		if depth != 1 {
			t.Errorf("call %d %q ran at transaction depth %d, want 1", i, exec.calls[i], depth)
		}
	}
	correction := slices.IndexFunc(exec.calls, func(sql string) bool {
		return strings.HasPrefix(sql, "UPDATE _change_log SET changed_by")
	})
	if args := exec.args[correction]; len(args) != 3 || args[1] != domain.TextValue(result.BatchID) ||
		args[2] != domain.TextValue(domain.ChangeOriginChange) {
		t.Errorf("correction args = %v, want (user, %q, %q)", args, result.BatchID, domain.ChangeOriginChange)
	}
}
