package service

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// detectChangeConflicts decides whether the given _change_log entries can
// safely be reversed (undo / redo / revert). It returns:
//
//   - (nil, nil)          — every entry can be reversed; caller proceeds.
//   - (*ChangeConflict, nil) — at least one entry is blocked; caller surfaces
//     the structured payload (with BlockedBy + ResolutionHints) to the API.
//   - (nil, err)          — I/O failure reading current state.
//
// entries is the full set of _change_log rows in a single batch (the unit
// of undo). N>1 is the norm: a bulk DML statement produces one entry per
// affected row, and a multi-statement DDL request produces one entry per
// statement. RevertChange (single-entry revert) passes a slice of one.
//
// The detector dispatches on each entry's Kind and bails on the first conflict; a batch may mix kinds when a DDL
// statement's foreign-key actions or user triggers wrote rows.
//
// Conflict semantics — DML (mirrors the legacy detectRevertConflictsBatch):
//   - INSERT undo: row must still exist (else "row missing").
//   - DELETE undo: row must not exist (else "row re-inserted").
//   - UPDATE undo: current row values must still match the entry's NewValues
//     column-by-column (else "column has changed").
//
// When a batch touches the same row more than once (a multi-statement request, or an FK action writing a row a
// sibling statement also wrote), only the row's newest entry is checked: earlier entries snapshot intermediate
// states that later statements of the same batch legitimately moved, so holding them against the live row would
// make the batch conflict with itself. The reverse replay revalidates the skipped entries implicitly — it applies
// inverses newest-first and each one restores the full row from its OldValues.
//
// Conflict semantics — DDL:
//   - CreateTable undo: rows written since the CREATE by a logged DML change block (they are the blockers); rows
//     without a logged writer (a recorded DML change since the CREATE) are captured by the DROP's own snapshot. A
//     non-empty virtual table always blocks, since its writes are never logged.
//   - DropTable undo:   no object with the same name may exist.
//   - AddColumn undo:   the column must still exist on the table.
//   - DropColumn undo:  the column must not exist (rebuild would collide).
//   - RenameColumn / RenameTo undo: the renamed target must still exist.
//   - CreateIndex / View / Trigger undo: the object must still exist.
//   - DropIndex / View / Trigger undo: no object with the same name may exist.
//
// On every conflict the detector tries to populate BlockedBy with the
// _change_log entries that explain *which later change* moved the state.
// Empty BlockedBy is a legal outcome (e.g. schema mutation that bypassed
// the change-log path, or pruning by retention).
func (s *QueryService) detectChangeConflicts(
	ctx context.Context,
	exec internalExecutor,
	databaseID string,
	entries []*domain.ChangeLogEntry,
) (*domain.ChangeConflict, error) {
	ctx, span := tracer.Start(ctx, "QueryService.detectChangeConflicts",
		trace.WithAttributes(
			attribute.String("database_id", databaseID),
			attribute.Int("entry.count", len(entries)),
		),
	)
	defer span.End()

	lastPerRow := lastDMLEntryPerRow(entries)
	for _, e := range entries {
		// A DML entry that is not its row's newest within the batch describes an intermediate state, not live
		// state — see the multi-touch paragraph in the doc comment.
		if e.IsDML() && lastPerRow[dmlRowKey(e)] != e.ID {
			continue
		}
		conflict, err := s.detectOneChangeConflict(ctx, exec, databaseID, e)
		if err != nil {
			return nil, err
		}
		if conflict == nil {
			continue
		}
		span.SetAttributes(
			attribute.Int64("conflict.change_log_id", conflict.ChangeLogID),
			attribute.String("conflict.kind", string(conflict.Kind)),
			attribute.String("conflict.subtype", conflict.Subtype),
			attribute.Int("conflict.blocked_by_count", len(conflict.BlockedBy)),
		)
		// Info, not Warn: a conflict is an expected outcome of the caller's request, not a fault of the instance.
		s.dbSvc.logger.Info().
			Str("database_id", databaseID).
			Int64("change_log_id", conflict.ChangeLogID).
			Str("kind", string(conflict.Kind)).
			Str("subtype", conflict.Subtype).
			Int("blocked_by_count", len(conflict.BlockedBy)).
			Msg("Change conflict raised; caller must resolve blockers before retrying")
		return conflict, nil
	}
	return nil, nil
}

// lastDMLEntryPerRow maps each (table, row) touched by the batch's DML entries to the ID of its newest entry — the
// only one whose recorded post-state describes the live row rather than an intermediate state internal to the batch.
func lastDMLEntryPerRow(entries []*domain.ChangeLogEntry) map[string]int64 {
	last := make(map[string]int64, len(entries))
	for _, e := range entries {
		if !e.IsDML() {
			continue
		}
		if k := dmlRowKey(e); e.ID > last[k] {
			last[k] = e.ID
		}
	}
	return last
}

// dmlRowKey identifies the row a DML entry touched. The NUL separator keeps distinct (table, row) pairs from
// colliding, since SQLite identifiers cannot contain NUL.
func dmlRowKey(e *domain.ChangeLogEntry) string {
	return e.TableName + "\x00" + e.RowID
}

// detectOneChangeConflict dispatches a single entry to its kind-specific
// check. Returns (nil, nil) when the entry can be reversed.
func (s *QueryService) detectOneChangeConflict(
	ctx context.Context,
	exec internalExecutor,
	databaseID string,
	e *domain.ChangeLogEntry,
) (*domain.ChangeConflict, error) {
	if e.IsDML() {
		return s.detectDMLChangeConflict(ctx, exec, databaseID, e)
	}
	return s.detectDDLChangeConflict(ctx, exec, databaseID, e)
}

// --- DML branch ---------------------------------------------------------

func (s *QueryService) detectDMLChangeConflict(
	ctx context.Context,
	exec internalExecutor,
	databaseID string,
	e *domain.ChangeLogEntry,
) (*domain.ChangeConflict, error) {
	switch e.Operation {
	case domain.ChangeOpInsert:
		current, found, err := s.fetchCurrentRow(ctx, exec, databaseID, e.TableName, e.RowID)
		if err != nil {
			return nil, err
		}
		if !found {
			return s.buildDMLConflict(ctx, exec, databaseID, e,
				fmt.Sprintf("row %s in %q no longer exists", e.RowID, e.TableName),
				"Revert the change that removed the row first, or leave this change in place.")
		}
		// Undoing the insert deletes the row, so the row must still be the one that was inserted.
		return s.rowStillAsRecorded(ctx, exec, databaseID, e, current,
			"The row changed after this insert. Revert the newer change to the row first, or leave this change in "+
				"place.")

	case domain.ChangeOpDelete:
		exists, err := s.rowExists(ctx, exec, databaseID, e.TableName, e.RowID)
		if err != nil {
			return nil, err
		}
		if exists {
			return s.buildDMLConflict(ctx, exec, databaseID, e,
				fmt.Sprintf("row %s in %q already exists (re-inserted since this delete)", e.RowID, e.TableName),
				"Revert the change that re-inserted the row first, or leave this change in place.")
		}
		return nil, nil

	case domain.ChangeOpUpdate:
		if len(e.NewValues) == 0 {
			return nil, nil
		}
		current, found, err := s.fetchCurrentRow(ctx, exec, databaseID, e.TableName, e.RowID)
		if err != nil {
			return nil, err
		}
		if !found {
			return s.buildDMLConflict(ctx, exec, databaseID, e,
				fmt.Sprintf("row %s in %q no longer exists", e.RowID, e.TableName),
				"The row was deleted after this update. Revert that delete first.")
		}
		return s.rowStillAsRecorded(ctx, exec, databaseID, e, current,
			"Revert the newer change to the row first.")
	}
	return nil, nil
}

// rowStillAsRecorded compares the live row against the values e recorded as the row's state after it ran. On the
// first column that diverges (in name order, so the reason is deterministic) it builds the conflict: a schema-drift
// one when the column no longer exists on the live row, otherwise one naming the changed value with hint as the way
// out. A nil conflict means the live row is exactly what e left behind.
func (s *QueryService) rowStillAsRecorded(
	ctx context.Context,
	exec internalExecutor,
	databaseID string,
	e *domain.ChangeLogEntry,
	current map[string]any,
	hint string,
) (*domain.ChangeConflict, error) {
	for _, k := range slices.Sorted(maps.Keys(e.NewValues)) {
		cur, ok := current[k]
		if !ok {
			return s.buildSchemaDriftConflict(ctx, exec, databaseID, e, k)
		}
		if !domain.ValuesEqual(cur, e.NewValues[k]) {
			reason := fmt.Sprintf("row %s in %q column %q has changed (expected %s, got %s)",
				e.RowID, e.TableName, k, conflictValue(e.NewValues[k]), conflictValue(cur))
			return s.buildDMLConflict(ctx, exec, databaseID, e, reason, hint)
		}
	}
	return nil, nil
}

// conflictValue spells a recorded or live column value for a conflict's reason: NULL for a missing value, text quoted,
// a number in its shortest form whatever its recorded spelling (a recorded 7.0 reads 7, as the live 7 does), and a
// blob by its size.
func conflictValue(v any) string {
	switch v := v.(type) {
	case nil:
		return "NULL"
	case string:
		return strconv.Quote(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return strconv.FormatInt(n, 10)
		}
		if f, err := v.Float64(); err == nil {
			return strconv.FormatFloat(f, 'g', -1, 64)
		}
		return v.String()
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case []byte:
		return fmt.Sprintf("<blob, %d bytes>", len(v))
	default:
		return fmt.Sprint(v)
	}
}

// buildSchemaDriftConflict builds the conflict for a recorded column the live row no longer has: the table's shape
// moved after e ran, so the blockers are the DDL entries on the table since then.
func (s *QueryService) buildSchemaDriftConflict(
	ctx context.Context,
	exec internalExecutor,
	databaseID string,
	e *domain.ChangeLogEntry,
	column string,
) (*domain.ChangeConflict, error) {
	blockers, err := s.loadDDLBlockersForTable(ctx, exec, databaseID, e.TableName, e.ID)
	if err != nil {
		return nil, err
	}
	return &domain.ChangeConflict{
		ChangeLogID: e.ID,
		Kind:        e.Kind,
		Reason:      fmt.Sprintf("row %s in %q no longer has column %q", e.RowID, e.TableName, column),
		BlockedBy:   blockers,
		ResolutionHints: []string{
			"The column was dropped or renamed after this change. Revert that schema change first, or leave this " +
				"change in place.",
		},
	}, nil
}

func (s *QueryService) buildDMLConflict(
	ctx context.Context,
	exec internalExecutor,
	databaseID string,
	e *domain.ChangeLogEntry,
	reason, hint string,
) (*domain.ChangeConflict, error) {
	blockers, err := s.loadDMLBlockers(ctx, exec, databaseID, e.TableName, e.RowID, e.ID)
	if err != nil {
		return nil, err
	}
	return &domain.ChangeConflict{
		ChangeLogID:     e.ID,
		Kind:            e.Kind,
		Subtype:         "",
		Reason:          reason,
		BlockedBy:       blockers,
		ResolutionHints: []string{hint},
	}, nil
}

// --- DDL branch ---------------------------------------------------------

func (s *QueryService) detectDDLChangeConflict(
	ctx context.Context,
	exec internalExecutor,
	databaseID string,
	e *domain.ChangeLogEntry,
) (*domain.ChangeConflict, error) {
	switch e.Operation {
	case domain.ChangeOpCreate:
		switch e.ObjectType {
		case domain.ChangeObjectTable:
			return s.detectCreateTableConflict(ctx, exec, databaseID, e)
		case domain.ChangeObjectIndex, domain.ChangeObjectView, domain.ChangeObjectTrigger:
			return s.detectCreateSchemaObjectConflict(ctx, exec, databaseID, e)
		}
	case domain.ChangeOpDrop:
		switch e.ObjectType {
		case domain.ChangeObjectTable:
			return s.detectDropTableConflict(ctx, exec, databaseID, e)
		case domain.ChangeObjectIndex, domain.ChangeObjectView, domain.ChangeObjectTrigger:
			return s.detectDropSchemaObjectConflict(ctx, exec, databaseID, e)
		}
	case domain.ChangeOpAlter:
		return s.detectAlterTableConflict(ctx, exec, databaseID, e)
	}
	return nil, nil
}

// CreateTable undo (= DROP TABLE) blocks when rows were written to the table since the CREATE by a logged DML change:
// dropping the table would discard work the change log attributes to someone. Rows with no logged writer (a recorded
// DML change since the CREATE) — restored by a reversal, or produced by the CREATE itself — are captured by the
// DROP's own snapshot and do not block. A non-empty virtual table always blocks, since writes into it bypass the
// change log.
func (s *QueryService) detectCreateTableConflict(
	ctx context.Context,
	exec internalExecutor,
	databaseID string,
	e *domain.ChangeLogEntry,
) (*domain.ChangeConflict, error) {
	rowCount, err := s.countTableRows(ctx, exec, databaseID, e.ObjectName)
	if err != nil {
		return nil, err
	}
	if rowCount == 0 {
		return nil, nil
	}
	blockers, err := s.loadDMLBlockersForTable(ctx, exec, databaseID, e.ObjectName, e.ID)
	if err != nil {
		return nil, err
	}
	if len(blockers) == 0 {
		virtual, err := isVirtualTable(ctx, exec, databaseID, e.ObjectName)
		if err != nil {
			return nil, err
		}
		if !virtual {
			return nil, nil
		}
	}
	return &domain.ChangeConflict{
		ChangeLogID: e.ID,
		Kind:        e.Kind,
		Reason: fmt.Sprintf("table %q is not empty (%d row(s)); undoing the CREATE would drop them",
			e.ObjectName, rowCount),
		BlockedBy: blockers,
		ResolutionHints: []string{
			"Delete the rows (revert each insert), or undo the later changes one at a time, whoever made them.",
			"To keep the rows, leave this change in place.",
		},
	}, nil
}

// DropTable undo (= recreate the table) blocks when an object already
// exists with the same name (another CREATE happened in the meantime).
func (s *QueryService) detectDropTableConflict(
	ctx context.Context,
	exec internalExecutor,
	databaseID string,
	e *domain.ChangeLogEntry,
) (*domain.ChangeConflict, error) {
	exists, err := s.objectExists(ctx, exec, databaseID, e.ObjectName)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	blockers, err := s.loadDDLBlockersForObjectName(ctx, exec, databaseID, e.ObjectName, e.ID)
	if err != nil {
		return nil, err
	}
	return &domain.ChangeConflict{
		ChangeLogID: e.ID,
		Kind:        e.Kind,
		Reason: fmt.Sprintf(
			"an object named %q already exists; cannot restore the dropped table over it",
			e.ObjectName),
		BlockedBy:       blockers,
		ResolutionHints: []string{"Drop or rename the colliding object first, then retry."},
	}, nil
}

// CreateIndex/View/Trigger undo blocks when the object isn't there to drop.
func (s *QueryService) detectCreateSchemaObjectConflict(
	ctx context.Context,
	exec internalExecutor,
	databaseID string,
	e *domain.ChangeLogEntry,
) (*domain.ChangeConflict, error) {
	exists, err := s.objectExists(ctx, exec, databaseID, e.ObjectName)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, nil
	}
	blockers, err := s.loadDDLBlockersForObjectName(ctx, exec, databaseID, e.ObjectName, e.ID)
	if err != nil {
		return nil, err
	}
	return &domain.ChangeConflict{
		ChangeLogID: e.ID,
		Kind:        e.Kind,
		Reason: fmt.Sprintf("%s %q no longer exists (already dropped elsewhere)",
			strings.ToLower(e.ObjectType), e.ObjectName),
		BlockedBy:       blockers,
		ResolutionHints: []string{"Nothing to undo — the object is already gone."},
	}, nil
}

// DropIndex/View/Trigger undo blocks when an object already exists with
// the same name.
func (s *QueryService) detectDropSchemaObjectConflict(
	ctx context.Context,
	exec internalExecutor,
	databaseID string,
	e *domain.ChangeLogEntry,
) (*domain.ChangeConflict, error) {
	exists, err := s.objectExists(ctx, exec, databaseID, e.ObjectName)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	blockers, err := s.loadDDLBlockersForObjectName(ctx, exec, databaseID, e.ObjectName, e.ID)
	if err != nil {
		return nil, err
	}
	return &domain.ChangeConflict{
		ChangeLogID: e.ID,
		Kind:        e.Kind,
		Reason: fmt.Sprintf(
			"an object named %q already exists; cannot restore the dropped %s over it",
			e.ObjectName, strings.ToLower(e.ObjectType)),
		BlockedBy:       blockers,
		ResolutionHints: []string{"Drop or rename the colliding object first, then retry."},
	}, nil
}

func (s *QueryService) detectAlterTableConflict(
	ctx context.Context,
	exec internalExecutor,
	databaseID string,
	e *domain.ChangeLogEntry,
) (*domain.ChangeConflict, error) {
	subtype := domain.ParseAlterTableSubtype(e.DDLSubtype)
	switch subtype {
	case domain.AlterTableAddColumn:
		colName := alterColumnName(e)
		if colName == "" {
			return nil, nil
		}
		present, err := s.columnExists(ctx, exec, databaseID, e.ObjectName, colName)
		if err != nil {
			return nil, err
		}
		if present {
			return nil, nil
		}
		blockers, err := s.loadDDLBlockersForTable(ctx, exec, databaseID, e.ObjectName, e.ID)
		if err != nil {
			return nil, err
		}
		return &domain.ChangeConflict{
			ChangeLogID: e.ID,
			Kind:        e.Kind,
			Subtype:     domain.DDLSubtypeAddColumn,
			Reason: fmt.Sprintf(
				"column %q on table %q no longer exists (already removed or renamed)",
				colName, e.ObjectName),
			BlockedBy:       blockers,
			ResolutionHints: []string{"Nothing to undo — the column is already gone."},
		}, nil

	case domain.AlterTableDropColumn:
		colName := alterColumnName(e)
		if colName == "" {
			return nil, nil
		}
		present, err := s.columnExists(ctx, exec, databaseID, e.ObjectName, colName)
		if err != nil {
			return nil, err
		}
		if !present {
			return nil, nil
		}
		blockers, err := s.loadDDLBlockersForTable(ctx, exec, databaseID, e.ObjectName, e.ID)
		if err != nil {
			return nil, err
		}
		return &domain.ChangeConflict{
			ChangeLogID: e.ID,
			Kind:        e.Kind,
			Subtype:     domain.DDLSubtypeDropColumn,
			Reason: fmt.Sprintf(
				"column %q already exists on table %q (re-added since this drop)",
				colName, e.ObjectName),
			BlockedBy:       blockers,
			ResolutionHints: []string{"Drop the re-added column first (revert the ADD COLUMN change), then retry."},
		}, nil

	case domain.AlterTableRenameColumn:
		newCol := ""
		if e.DDLMetadata != nil {
			newCol = e.DDLMetadata.NewColumnName
		}
		if newCol == "" {
			return nil, nil
		}
		present, err := s.columnExists(ctx, exec, databaseID, e.ObjectName, newCol)
		if err != nil {
			return nil, err
		}
		if present {
			return nil, nil
		}
		blockers, err := s.loadDDLBlockersForTable(ctx, exec, databaseID, e.ObjectName, e.ID)
		if err != nil {
			return nil, err
		}
		return &domain.ChangeConflict{
			ChangeLogID: e.ID,
			Kind:        e.Kind,
			Subtype:     domain.DDLSubtypeRenameColumn,
			Reason: fmt.Sprintf(
				"column %q on table %q no longer exists (renamed or dropped since)",
				newCol, e.ObjectName),
			BlockedBy:       blockers,
			ResolutionHints: []string{"Revert the newer ALTER on this column first."},
		}, nil

	case domain.AlterTableRenameTo:
		newName := ""
		if e.DDLMetadata != nil {
			newName = e.DDLMetadata.NewTableName
		}
		if newName == "" {
			return nil, nil
		}
		exists, err := s.objectExists(ctx, exec, databaseID, newName)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, nil
		}
		blockers, err := s.loadDDLBlockersForObjectName(ctx, exec, databaseID, newName, e.ID)
		if err != nil {
			return nil, err
		}
		return &domain.ChangeConflict{
			ChangeLogID:     e.ID,
			Kind:            e.Kind,
			Subtype:         domain.DDLSubtypeRenameTo,
			Reason:          fmt.Sprintf("table %q no longer exists (renamed or dropped since)", newName),
			BlockedBy:       blockers,
			ResolutionHints: []string{"Revert the newer ALTER on this table first."},
		}, nil
	}
	return nil, nil
}

func alterColumnName(e *domain.ChangeLogEntry) string {
	if e.DDLMetadata == nil {
		return ""
	}
	return e.DDLMetadata.ColumnName
}

// --- Schema state probes -----------------------------------------------

func (s *QueryService) rowExists(
	ctx context.Context, exec internalExecutor, databaseID, table, rowID string,
) (bool, error) {
	sql := fmt.Sprintf("SELECT 1 FROM %s WHERE rowid = ? LIMIT 1", domain.QuoteIdentifier(table))
	r, err := exec.ExecuteInternal(ctx, databaseID, sql, []domain.TypedValue{domain.TextValue(rowID)})
	if err != nil {
		return false, err
	}
	return len(r.Rows) > 0, nil
}

func (s *QueryService) fetchCurrentRow(
	ctx context.Context, exec internalExecutor, databaseID, table, rowID string,
) (map[string]any, bool, error) {
	sql := fmt.Sprintf("SELECT * FROM %s WHERE rowid = ?", domain.QuoteIdentifier(table))
	r, err := exec.ExecuteInternal(ctx, databaseID, sql, []domain.TypedValue{domain.TextValue(rowID)})
	if err != nil {
		return nil, false, err
	}
	if len(r.Rows) == 0 {
		return nil, false, nil
	}
	row := r.Rows[0]
	out := make(map[string]any, len(r.Columns))
	for i, col := range r.Columns {
		if i >= len(row) {
			break
		}
		out[col.Name] = row[i].Value
	}
	return out, true, nil
}

func (s *QueryService) countTableRows(
	ctx context.Context, exec internalExecutor, databaseID, table string,
) (int64, error) {
	sql := "SELECT COUNT(*) FROM " + domain.QuoteIdentifier(table)
	r, err := exec.ExecuteInternal(ctx, databaseID, sql, nil)
	if err != nil {
		return 0, err
	}
	if len(r.Rows) == 0 || len(r.Rows[0]) == 0 {
		return 0, nil
	}
	switch v := r.Rows[0][0].Value.(type) {
	case int64:
		return v, nil
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parsing row count of %q: %w", table, err)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("counting rows of %q: unexpected COUNT(*) value type %T", table, v)
	}
}

// objectExists checks sqlite_master for any object (table / index / view /
// trigger) with the given name. Used by the DDL conflict checks where the
// rule is "no name collision allowed" (DropTable undo, etc.).
func (s *QueryService) objectExists(ctx context.Context, exec internalExecutor, databaseID, name string) (bool, error) {
	sql := "SELECT 1 FROM sqlite_master WHERE name = ? LIMIT 1"
	r, err := exec.ExecuteInternal(ctx, databaseID, sql, []domain.TypedValue{domain.TextValue(name)})
	if err != nil {
		return false, err
	}
	return len(r.Rows) > 0, nil
}

func (s *QueryService) columnExists(
	ctx context.Context, exec internalExecutor, databaseID, table, column string,
) (bool, error) {
	sql := fmt.Sprintf("PRAGMA table_info(%s)", domain.QuoteIdentifier(table))
	r, err := exec.ExecuteInternal(ctx, databaseID, sql, nil)
	if err != nil {
		return false, err
	}
	for _, row := range r.Rows {
		if len(row) < 2 {
			continue
		}
		name, _ := row[1].Value.(string)
		if name == column {
			return true, nil
		}
	}
	return false, nil
}

// --- Blocker loaders ---------------------------------------------------

func (s *QueryService) loadDMLBlockers(
	ctx context.Context, exec internalExecutor, databaseID, table, rowID string, sinceID int64,
) ([]domain.ChangeLogEntry, error) {
	sql := "SELECT " + changeLogFullColumns + " FROM " + domain.ChangeLogTable +
		" WHERE id > ? AND kind = 'DML' AND table_name = ? AND row_id = ? ORDER BY id ASC LIMIT 50"
	r, err := exec.ExecuteInternal(ctx, databaseID, sql, []domain.TypedValue{
		domain.IntegerValue(sinceID),
		domain.TextValue(table),
		domain.TextValue(rowID),
	})
	if err != nil {
		return nil, err
	}
	return changeLogEntries(r.Rows), nil
}

func (s *QueryService) loadDMLBlockersForTable(
	ctx context.Context, exec internalExecutor, databaseID, table string, sinceID int64,
) ([]domain.ChangeLogEntry, error) {
	sql := "SELECT " + changeLogFullColumns + " FROM " + domain.ChangeLogTable +
		" WHERE id > ? AND kind = 'DML' AND table_name = ? ORDER BY id ASC LIMIT 50"
	r, err := exec.ExecuteInternal(ctx, databaseID, sql, []domain.TypedValue{
		domain.IntegerValue(sinceID),
		domain.TextValue(table),
	})
	if err != nil {
		return nil, err
	}
	return changeLogEntries(r.Rows), nil
}

func (s *QueryService) loadDDLBlockersForObjectName(
	ctx context.Context, exec internalExecutor, databaseID, name string, sinceID int64,
) ([]domain.ChangeLogEntry, error) {
	sql := "SELECT " + changeLogFullColumns + " FROM " + domain.ChangeLogTable +
		" WHERE id > ? AND kind = 'DDL' AND object_name = ? ORDER BY id ASC LIMIT 50"
	r, err := exec.ExecuteInternal(ctx, databaseID, sql, []domain.TypedValue{
		domain.IntegerValue(sinceID),
		domain.TextValue(name),
	})
	if err != nil {
		return nil, err
	}
	return changeLogEntries(r.Rows), nil
}

func (s *QueryService) loadDDLBlockersForTable(
	ctx context.Context, exec internalExecutor, databaseID, table string, sinceID int64,
) ([]domain.ChangeLogEntry, error) {
	return s.loadDDLBlockersForObjectName(ctx, exec, databaseID, table, sinceID)
}

// changeLogEntries decodes _change_log rows selected with changeLogFullColumns into entry values.
func changeLogEntries(rows [][]domain.TypedValue) []domain.ChangeLogEntry {
	out := make([]domain.ChangeLogEntry, len(rows))
	for i, row := range rows {
		out[i] = *parseChangeLogFullRow(row)
	}
	return out
}
