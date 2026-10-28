package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// RegenChangeLogTriggersForAllUserTablesForTesting regenerates the per-table
// _change_log triggers on every user table, for tests that create tables
// through the internal executor instead of the DDL flow that installs them.
func RegenChangeLogTriggersForAllUserTablesForTesting(
	ctx context.Context, executor internalExecutor, databaseID string,
) error {
	return regenChangeLogTriggersForAllUserTables(ctx, executor, databaseID)
}

// postMigrationHook runs Go-side work after a specific platform migration's
// SQL has executed but before the version bump. No current version registers
// a hook; the seam stays for a future migration that needs Go-side work — for
// example regenerating per-table triggers after a trigger-shape change. A
// hook must be idempotent: a database left in partial state re-runs its
// version's SQL and hook on the next attempt.
func postMigrationHook(context.Context, internalExecutor, string, int) error {
	return nil
}

// regenChangeLogTriggersForAllUserTables enumerates non-platform tables
// (those not prefixed with "_") and reinstalls the _change_log triggers
// on each.
func regenChangeLogTriggersForAllUserTables(ctx context.Context, executor internalExecutor, databaseID string) error {
	tables, err := enumerateUserTables(ctx, executor, databaseID)
	if err != nil {
		return fmt.Errorf("enumerating user tables: %w", err)
	}
	for _, table := range tables {
		if err := regenChangeLogTriggersForTable(ctx, executor, databaseID, table); err != nil {
			return fmt.Errorf("regenerating triggers for %q: %w", table, err)
		}
	}
	return nil
}

// regenChangeLogTriggersForTable drops the given user table's _change_log
// trigger names, reads the column list via pragma_table_info, and creates
// fresh _change_log triggers. Idempotent.
//
// Virtual tables are exempt: SQLite forbids CREATE TRIGGER on them and they don't participate in the change-log
// (writes flow through the module's xUpdate, which bypasses triggers).
func regenChangeLogTriggersForTable(ctx context.Context, executor internalExecutor, databaseID, table string) error {
	virtual, err := isVirtualTable(ctx, executor, databaseID, table)
	if err != nil {
		return fmt.Errorf("classify table kind: %w", err)
	}
	if virtual {
		return nil
	}
	drop1, drop2, drop3 := domain.GenerateDropChangeLogTriggers(table)
	for _, drop := range []string{drop1, drop2, drop3} {
		if _, err := executor.ExecuteInternal(ctx, databaseID, drop, nil); err != nil {
			return fmt.Errorf("drop trigger: %w", err)
		}
	}

	cols, err := readTableColumns(ctx, executor, databaseID, table)
	if err != nil {
		return fmt.Errorf("reading columns: %w", err)
	}
	if len(cols) == 0 {
		return nil
	}

	insert, update, del := domain.GenerateChangeLogTriggers(table, cols)
	for _, sql := range []string{insert, update, del} {
		if _, err := executor.ExecuteInternal(ctx, databaseID, sql, nil); err != nil {
			return fmt.Errorf("create trigger: %w", err)
		}
	}
	return nil
}

// enumerateUserTables returns the names of all non-platform tables in the
// given database. Platform tables are those whose names begin with "_";
// SQLite system tables (sqlite_sequence, sqlite_stat*, etc.) are excluded
// as well — they cannot have user triggers attached. The ESCAPE clause
// treats the leading underscore literally rather than as the LIKE
// single-character wildcard.
func enumerateUserTables(ctx context.Context, executor internalExecutor, databaseID string) ([]string, error) {
	const sql = `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE '\_%' ESCAPE '\' ` +
		`AND name NOT LIKE 'sqlite_%' ORDER BY name`
	result, err := executor.ExecuteInternal(ctx, databaseID, sql, nil)
	if err != nil {
		return nil, err
	}
	tables := make([]string, 0, len(result.Rows))
	for _, row := range result.Rows {
		if len(row) == 0 {
			continue
		}
		name, ok := row[0].Value.(string)
		if !ok {
			continue
		}
		tables = append(tables, name)
	}
	return tables, nil
}

// isVirtualTable reports whether the named object in sqlite_master is a virtual table by inspecting the stored CREATE
// statement. A missing row returns false.
func isVirtualTable(ctx context.Context, executor internalExecutor, databaseID, table string) (bool, error) {
	const sql = "SELECT sql FROM sqlite_master WHERE name = ? LIMIT 1"
	r, err := executor.ExecuteInternal(ctx, databaseID, sql, []domain.TypedValue{domain.TextValue(table)})
	if err != nil {
		return false, err
	}
	if r == nil || len(r.Rows) == 0 || len(r.Rows[0]) == 0 {
		return false, nil
	}
	createSQL, _ := r.Rows[0][0].Value.(string)
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(createSQL)), "CREATE VIRTUAL TABLE"), nil
}

// readTableColumns reads the column names for the given table via
// PRAGMA table_info. Returns columns in the order SQLite returns them
// (matches the table's declaration order).
func readTableColumns(
	ctx context.Context, executor internalExecutor, databaseID, table string,
) ([]domain.TriggerColumn, error) {
	sql := fmt.Sprintf("PRAGMA table_info(%s)", domain.QuoteIdentifier(table))
	result, err := executor.ExecuteInternal(ctx, databaseID, sql, nil)
	if err != nil {
		return nil, err
	}
	cols := make([]domain.TriggerColumn, 0, len(result.Rows))
	for _, row := range result.Rows {
		if len(row) < 2 {
			continue
		}
		name, ok := row[1].Value.(string)
		if !ok {
			continue
		}
		cols = append(cols, domain.TriggerColumn{Name: name})
	}
	return cols, nil
}
