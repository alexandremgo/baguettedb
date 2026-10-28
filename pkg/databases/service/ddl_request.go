package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// SnapshotWarning reports a truncated pre-image capture: a destructive DDL statement touched more rows than the
// snapshot cap, so a reversal restores only the captured rows. Carries the raw counts (captured / total / lost) and
// the 1-based index of the statement whose capture was truncated, which tells the statements of a multi-statement
// request apart.
type SnapshotWarning struct {
	Truncated bool
	Captured  int
	Total     int
	Lost      int
	Statement int
}

// executeDDLRequest runs a request of N>=1 user DDL statements through the DDL pipeline inside one transaction —
// pre-image capture, execution, _change_log and _change_snapshots writes, trigger regeneration, and the batch close
// that pushes the batch onto the caller's undo stack and clears the caller's redo stack — then the post-commit
// housekeeping.
//
// The transaction opens in tenant mode: the user's statements are prepared under the authorizer a second time, at
// execution, so one whose meaning changed since classification is still refused; the pipeline's own reads and
// writes on platform tables go through ExecInternal.
//
// On success the returned snapshotWarnings slice has one entry per truncated statement (empty when nothing was
// truncated).
func (s *QueryService) executeDDLRequest(
	ctx context.Context,
	databaseID string,
	userID uuid.UUID,
	stmts []domain.ClassifiedStatement,
) (*domain.QueryResult, []SnapshotWarning, error) {
	ctx, span := tracer.Start(ctx, "QueryService.executeDDLRequest",
		trace.WithAttributes(
			attribute.String("database_id", databaseID),
			attribute.String("user.id", userID.String()),
			attribute.Int("stmt.count", len(stmts)),
		),
	)
	defer span.End()

	batchID := uuid.NewString()
	var result *domain.QueryResult
	var warnings []SnapshotWarning
	err := s.executor.WithTx(ctx, databaseID, false, func(ctx context.Context, tx Tx) error {
		units, err := s.userDDLUnits(tx, stmts)
		if err != nil {
			return err
		}
		result, warnings, err = s.executeDDLRequestInTx(ctx, tx, databaseID, userID, units, batchID,
			domain.ChangeOriginChange, undoStackPushAndRedoClearStmts(batchID, userID))
		return err
	})
	if err != nil {
		return nil, nil, err
	}

	// The transaction has committed: a request budget firing right after it must not drop the retention sweep or the
	// size refresh.
	postCommitCtx := context.WithoutCancel(ctx)
	s.cleanupStack(postCommitCtx, databaseID, domain.UndoStackTable, userID)
	s.dbSvc.RefreshStoredSize(postCommitCtx, databaseID)
	return result, warnings, nil
}

// executeDDLRequestInTx runs a DDL batch inside the transaction tx: the units in order, then the batch close. The
// batch is recorded under batchID with origin; stackStmts are the caller's stack moves and run last, so a failure
// anywhere rolls the whole batch back.
//
// ChangeOriginChange marks fresh user DDL, the only origin the admission checks gate — the identifier rules on the
// names an ALTER introduces and on the columns of a fresh CREATE. Computed statements may legitimately carry names
// admitted before those rules existed and must keep working.
func (s *QueryService) executeDDLRequestInTx(
	ctx context.Context,
	tx Tx,
	databaseID string,
	userID uuid.UUID,
	units []domain.DDLUnit,
	batchID string,
	origin string,
	stackStmts []domain.SQLStatement,
) (*domain.QueryResult, []SnapshotWarning, error) {
	result, warnings, err := s.runDDLUnitsInTx(ctx, tx, databaseID, userID, units, batchID, origin)
	if err != nil {
		return nil, nil, err
	}
	if err := closeChangeBatchInTx(ctx, tx, userID, batchID, origin, stackStmts); err != nil {
		return nil, nil, err
	}
	return result, warnings, nil
}

// runDDLUnitsInTx performs units in order inside tx and records each one under batchID with origin: the pre-image is
// captured (snapshot + metadata), the unit's statements run, the _change_log DDL row is written, and the optional
// _change_snapshots row is written for destructive shapes. Per-table change-log triggers are regenerated once at the
// end for every table whose shape changed. The result is the last unit's, carrying batchID; snapshotWarnings has one
// entry per truncated capture.
func (s *QueryService) runDDLUnitsInTx(
	ctx context.Context,
	tx Tx,
	databaseID string,
	userID uuid.UUID,
	units []domain.DDLUnit,
	batchID string,
	origin string,
) (*domain.QueryResult, []SnapshotWarning, error) {
	if err := rejectRenameToInMultiStmt(units); err != nil {
		return nil, nil, err
	}

	fresh := origin == domain.ChangeOriginChange
	if fresh {
		for i, unit := range units {
			if unit.Shape.DDLType != domain.DDLAlterTable {
				continue
			}
			if err := validateAlterAdmission(unit.Alter); err != nil {
				if len(units) > 1 {
					return nil, nil, fmt.Errorf("statement %d: %w", i+1, err)
				}
				return nil, nil, err
			}
		}
	}

	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.String("batch_id", batchID))

	tablesToRegen := map[string]struct{}{}
	dropped := map[string]struct{}{}
	var lastResult *domain.QueryResult
	var warnings []SnapshotWarning

	for i, unit := range units {
		// 1. Capture pre-image (snapshot + metadata) before mutating state. Domain capture builders are pure;
		//    service code issues the SQL reads.
		snap, alterMeta, err := s.captureDDLPreImage(ctx, tx, unit.Shape, unit.Alter)
		if err != nil {
			return nil, nil, fmt.Errorf("statement %d: capture: %w", i+1, err)
		}

		// 2. Perform the unit with the support work its recorded shape calls for (pre-drop triggers for ALTER,
		//    skill cascades for DROP / RENAME, regen tracking for CREATE).
		r, err := s.runDDLUnit(ctx, tx, i, unit, tablesToRegen, dropped)
		if err != nil {
			return nil, nil, err
		}
		lastResult = r

		// Column names aren't visible at prepare time, so a fresh CREATE's columns are validated from the engine's
		// post-execute schema; a violation rolls back the whole request and the table never becomes visible.
		if fresh && unit.Shape.DDLType == domain.DDLCreateTable {
			if err := validateCreatedColumnsInTx(ctx, tx, unit.Shape); err != nil {
				if len(units) > 1 {
					return nil, nil, fmt.Errorf("statement %d: %w", i+1, err)
				}
				return nil, nil, err
			}
		}

		// 3. Write the _change_log DDL row with the shared batch_id.
		changeLogID, err := insertChangeLogDDL(ctx, tx, batchID, userID, unit.Shape, unit.Alter, alterMeta, origin)
		if err != nil {
			return nil, nil, fmt.Errorf("statement %d: %w", i+1, err)
		}

		// 4. Write the _change_snapshots sidecar row for destructive subtypes that captured pre-image data.
		if snap != nil {
			if err := insertChangeSnapshot(ctx, tx, changeLogID, *snap); err != nil {
				return nil, nil, fmt.Errorf("statement %d: %w", i+1, err)
			}
			if snap.Truncated {
				captured := len(snap.RowPayload)
				lost := snap.RowCountTotal - captured
				warnings = append(warnings, SnapshotWarning{
					Truncated: true,
					Captured:  captured,
					Total:     snap.RowCountTotal,
					Lost:      lost,
					Statement: i + 1,
				})
				s.dbSvc.logger.Warn().
					Str("database_id", databaseID).
					Str("batch_id", batchID).
					Int("stmt.index", i+1).
					Int("snapshot.captured_rows", captured).
					Int("snapshot.total_rows", snap.RowCountTotal).
					Int("snapshot.lost_rows", lost).
					Msg("DDL pre-image snapshot truncated at cap; lost rows cannot be restored by undo")
			}
		}
	}

	// 5. Deferred trigger regeneration: replace per-table _change_log triggers from the post-DDL pragma_table_info
	//    column list.
	for table := range tablesToRegen {
		if _, isDropped := dropped[table]; isDropped {
			continue
		}
		if err := regenChangeLogTriggersInTx(ctx, tx, table); err != nil {
			return nil, nil, fmt.Errorf("regenerating triggers for %q: %w", table, err)
		}
	}

	if lastResult == nil {
		lastResult = &domain.QueryResult{}
	}
	lastResult.BatchID = batchID
	span.SetAttributes(attribute.Int("snapshot.truncated_count", len(warnings)))
	return lastResult, warnings, nil
}

// closeChangeBatchInTx ends a change batch inside tx: it stamps batchID and origin on every __pending__ DML row the
// batch's triggers wrote — rows a foreign-key action or a user trigger produced while the batch ran — then applies
// the caller's stack statements. Both are atomic with the batch: a failure rolls the whole batch back.
func closeChangeBatchInTx(
	ctx context.Context, tx Tx, userID uuid.UUID, batchID, origin string, stackStmts []domain.SQLStatement,
) error {
	correction := changeLogCorrectionStmt(userID, batchID, origin)
	if _, err := tx.ExecInternal(ctx, correction.SQL, correction.Args); err != nil {
		return fmt.Errorf("change log correction: %w", err)
	}
	for _, st := range stackStmts {
		if _, err := tx.ExecInternal(ctx, st.SQL, st.Args); err != nil {
			return fmt.Errorf("stack update: %w", err)
		}
	}
	return nil
}

// userDDLUnits wraps classified user statements as units that execute themselves, parsing each ALTER TABLE against
// the schema h resolves.
func (s *QueryService) userDDLUnits(h ParseHandle, stmts []domain.ClassifiedStatement) ([]domain.DDLUnit, error) {
	units := make([]domain.DDLUnit, 0, len(stmts))
	for i, stmt := range stmts {
		var alter domain.AlterTableInfo
		if stmt.DDLType == domain.DDLAlterTable {
			parsed, err := s.classifier.ClassifyAlterTable(h, stmt.SQL)
			if err != nil {
				return nil, fmt.Errorf("%w: statement %d: %w", domain.ErrUnsupportedStatement, i+1, err)
			}
			alter = parsed
		}
		units = append(units, domain.UserDDLUnit(stmt, alter))
	}
	return units, nil
}

// validateAlterAdmission checks the identifier rule on the names an ALTER TABLE statement introduces: the
// new table name on RENAME TO, and the new column name on ADD COLUMN / RENAME COLUMN.
//
// Existing names are deliberately not validated, so a pre-existing non-conforming column can still be
// renamed to a conforming name or dropped.
func validateAlterAdmission(info domain.AlterTableInfo) error {
	switch info.Subtype {
	case domain.AlterTableRenameTo:
		return domain.ValidateIdentifier(info.NewTableName)
	case domain.AlterTableAddColumn:
		return domain.ValidateIdentifier(info.ColumnName)
	case domain.AlterTableRenameColumn:
		return domain.ValidateIdentifier(info.NewColumnName)
	}
	return nil
}

// validateCreatedColumnsInTx checks the identifier rule on every column of a just-created table (or virtual
// table) by reading the engine's post-execute schema inside the open transaction.
func validateCreatedColumnsInTx(ctx context.Context, tx Tx, stmt domain.ClassifiedStatement) error {
	if len(stmt.Tables) == 0 {
		return nil
	}
	args := []domain.TypedValue{domain.TextValue(stmt.Tables[0])}
	r, err := tx.ExecInternal(ctx, "SELECT name FROM pragma_table_info(?)", args)
	if err != nil {
		return fmt.Errorf("read table_info: %w", err)
	}
	for _, row := range r.Rows {
		if len(row) == 0 {
			continue
		}
		name, _ := row[0].Value.(string)
		if err := domain.ValidateIdentifier(name); err != nil {
			return fmt.Errorf("invalid column name: %w", err)
		}
	}
	return nil
}

// rejectRenameToInMultiStmt enforces the documented limitation that ALTER TABLE RENAME TO must be a single-statement
// request: tracking the new name across multiple statements would complicate the regen set.
func rejectRenameToInMultiStmt(units []domain.DDLUnit) error {
	if len(units) <= 1 {
		return nil
	}
	for i, unit := range units {
		if unit.Shape.DDLType == domain.DDLAlterTable && unit.Alter.Subtype == domain.AlterTableRenameTo {
			return fmt.Errorf("%w: ALTER TABLE RENAME TO is not supported in multi-statement DDL (statement %d); "+
				"submit it as a single-statement request", domain.ErrUnsupportedStatement, i+1)
		}
	}
	return nil
}

// runDDLUnit performs one unit inside the open transaction with the support work its recorded shape calls for: the
// per-table _change_log triggers are dropped before an ALTER, _skill_targets cascade on DROP TABLE / RENAME TO, and
// the tables whose shape changed are tracked in tablesToRegen / dropped. Returns the last statement's result so the
// caller can preserve AffectedRowCount / LastInsertRowID parity.
func (s *QueryService) runDDLUnit(
	ctx context.Context,
	tx Tx,
	stmtIdx int,
	unit domain.DDLUnit,
	tablesToRegen map[string]struct{},
	dropped map[string]struct{},
) (*domain.QueryResult, error) {
	objectName := ""
	if len(unit.Shape.Tables) > 0 {
		objectName = unit.Shape.Tables[0]
	}
	// The unit's own statements run on Exec: a user's DDL stays under the authorizer, and a computed inverse unit
	// runs inside an internal transaction where Exec already bypasses it.
	execAll := func() (*domain.QueryResult, error) {
		var last *domain.QueryResult
		for _, st := range unit.Exec {
			r, e := tx.Exec(ctx, st.SQL, st.Args)
			if e != nil {
				return nil, fmt.Errorf("statement %d: %w", stmtIdx+1, explainExecError(e))
			}
			last = r
		}
		return last, nil
	}

	switch unit.Shape.DDLType {
	case domain.DDLAlterTable:
		if objectName == "" {
			return nil, fmt.Errorf("statement %d: ALTER TABLE missing target", stmtIdx+1)
		}
		// Pre-drop the per-table _change_log triggers so the ALTER
		// doesn't fail trigger validation against a stale NEW/OLD
		// column reference.
		for _, drop := range dropTriggerSet(objectName) {
			if _, e := tx.ExecInternal(ctx, drop, nil); e != nil {
				return nil, fmt.Errorf("statement %d: dropping pre-ALTER triggers: %w", stmtIdx+1, e)
			}
		}
		r, e := execAll()
		if e != nil {
			return nil, e
		}
		if unit.Alter.Subtype == domain.AlterTableRenameTo && unit.Alter.NewTableName != "" {
			cascade := CascadeStatementOnTableRenamed(objectName, unit.Alter.NewTableName)
			if _, ce := tx.ExecInternal(ctx, cascade.SQL, cascade.Args); ce != nil {
				return nil, fmt.Errorf("statement %d: _skill_targets rename cascade: %w", stmtIdx+1, ce)
			}
			delete(tablesToRegen, objectName)
			tablesToRegen[unit.Alter.NewTableName] = struct{}{}
		} else {
			tablesToRegen[objectName] = struct{}{}
		}
		return r, nil

	case domain.DDLDropTable:
		r, e := execAll()
		if e != nil {
			return nil, e
		}
		if objectName != "" {
			cascade := CascadeStatementOnTableDropped(objectName)
			if _, ce := tx.ExecInternal(ctx, cascade.SQL, cascade.Args); ce != nil {
				return nil, fmt.Errorf("statement %d: _skill_targets cascade: %w", stmtIdx+1, ce)
			}
			dropped[objectName] = struct{}{}
			delete(tablesToRegen, objectName)
		}
		return r, nil

	case domain.DDLCreateTable:
		r, e := execAll()
		if e != nil {
			return nil, e
		}
		if objectName != "" {
			tablesToRegen[objectName] = struct{}{}
		}
		return r, nil

	default:
		// CREATE/DROP INDEX, CREATE/DROP TRIGGER, CREATE/DROP VIEW.
		return execAll()
	}
}

// dropTriggerSet returns the DROP TRIGGER IF EXISTS statements for the
// per-table _change_log triggers.
func dropTriggerSet(tableName string) []string {
	drop1, drop2, drop3 := domain.GenerateDropChangeLogTriggers(tableName)
	return []string{drop1, drop2, drop3}
}

// regenChangeLogTriggersInTx drops and recreates the per-table
// _change_log triggers using the current pragma_table_info column list.
// In-transaction counterpart to regenChangeLogTriggersForTable in the
// post-migration hook (which uses the database-level executor).
//
// Virtual tables are exempt: SQLite forbids CREATE TRIGGER on a virtual table, and writes via the module's xUpdate
// bypass the trigger mechanism anyway.
func regenChangeLogTriggersInTx(ctx context.Context, tx Tx, tableName string) error {
	virtual, err := isVirtualTableInTx(ctx, tx, tableName)
	if err != nil {
		return fmt.Errorf("classify table kind: %w", err)
	}
	if virtual {
		return nil
	}
	for _, drop := range dropTriggerSet(tableName) {
		if _, e := tx.ExecInternal(ctx, drop, nil); e != nil {
			return fmt.Errorf("drop trigger: %w", e)
		}
	}
	pragmaSQL := fmt.Sprintf("PRAGMA table_info(%s)", domain.QuoteIdentifier(tableName))
	r, err := tx.ExecInternal(ctx, pragmaSQL, nil)
	if err != nil {
		return fmt.Errorf("read table_info: %w", err)
	}
	cols := pragmaResultToTriggerColumns(r)
	if len(cols) == 0 {
		// Table doesn't exist (dropped earlier in the same request) or
		// has no columns; nothing to regenerate.
		return nil
	}
	insertTrigger, updateTrigger, deleteTrigger := domain.GenerateChangeLogTriggers(tableName, cols)
	for _, triggerSQL := range []string{insertTrigger, updateTrigger, deleteTrigger} {
		if _, e := tx.ExecInternal(ctx, triggerSQL, nil); e != nil {
			return fmt.Errorf("create trigger: %w", e)
		}
	}
	return nil
}

// isVirtualTableInTx is isVirtualTable through the open transaction.
func isVirtualTableInTx(ctx context.Context, tx Tx, tableName string) (bool, error) {
	return isVirtualTable(ctx, txQueryExecutor{tx}, "", tableName)
}

// ExecuteDDLRequestForTesting exposes executeDDLRequest for integration
// tests that need direct access to the snapshot-warning return value.
func (s *QueryService) ExecuteDDLRequestForTesting(
	ctx context.Context,
	databaseID string,
	userID uuid.UUID,
	stmts []domain.ClassifiedStatement,
) (*domain.QueryResult, []SnapshotWarning, error) {
	return s.executeDDLRequest(ctx, databaseID, userID, stmts)
}
