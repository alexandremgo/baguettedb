package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// captureDDLPreImage gathers the pre-image data needed to reverse a DDL
// statement. Returns (nil, nil, nil) for non-destructive subtypes
// (CREATE *, ADD COLUMN, RENAME COLUMN, RENAME TO) that don't need
// snapshot rows. Always returns a non-nil DDLAlterMetadata for ALTER
// subtypes so the revert generator has the column/table names it needs;
// returns nil DDLAlterMetadata for non-ALTER DDL.
func (s *QueryService) captureDDLPreImage(
	ctx context.Context,
	tx Tx,
	stmt domain.ClassifiedStatement,
	alterInfo domain.AlterTableInfo,
) (snap *domain.ChangeSnapshot, alterMeta *domain.DDLAlterMetadata, retErr error) {
	objectName := ""
	if len(stmt.Tables) > 0 {
		objectName = stmt.Tables[0]
	}
	ddlSubtype := ""
	if stmt.DDLType == domain.DDLAlterTable {
		ddlSubtype = alterInfo.Subtype.String()
	}
	operation, objectType := ddlTypeToChangeLog(stmt.DDLType)
	ctx, span := tracer.Start(ctx, "QueryService.captureDDLPreImage",
		trace.WithAttributes(
			attribute.String("ddl.operation", operation),
			attribute.String("ddl.object_type", objectType),
			attribute.String("ddl.subtype", ddlSubtype),
			attribute.String("object.name", objectName),
		),
	)
	defer func() {
		if snap != nil {
			span.SetAttributes(
				attribute.Int("snapshot.captured_rows", len(snap.RowPayload)),
				attribute.Int("snapshot.total_rows", snap.RowCountTotal),
				attribute.Bool("snapshot.truncated", snap.Truncated),
			)
		}
		span.End()
	}()

	switch stmt.DDLType {
	case domain.DDLAlterTable:
		return s.captureAlterTable(ctx, tx, alterInfo)
	case domain.DDLDropTable:
		if len(stmt.Tables) == 0 {
			return nil, nil, errors.New("DROP TABLE missing target")
		}
		snap, err := s.captureDropTable(ctx, tx, stmt.Tables[0])
		return snap, nil, err
	case domain.DDLDropIndex:
		if len(stmt.Tables) == 0 {
			return nil, nil, errors.New("DROP INDEX missing target")
		}
		snap, err := captureSchemaObject(ctx, tx, "index", stmt.Tables[0], domain.DependentKindIndex)
		return snap, nil, err
	case domain.DDLDropView:
		if len(stmt.Tables) == 0 {
			return nil, nil, errors.New("DROP VIEW missing target")
		}
		snap, err := captureSchemaObject(ctx, tx, "view", stmt.Tables[0], domain.DependentKindView)
		return snap, nil, err
	case domain.DDLDropTrigger:
		if len(stmt.Tables) == 0 {
			return nil, nil, errors.New("DROP TRIGGER missing target")
		}
		snap, err := captureSchemaObject(ctx, tx, "trigger", stmt.Tables[0], domain.DependentKindTrigger)
		return snap, nil, err
	default:
		// CREATE TABLE / INDEX / VIEW / TRIGGER: no capture needed (inverse
		// is just DROP <object>).
		return nil, nil, nil
	}
}

// captureAlterTable populates DDLAlterMetadata for every ALTER subtype and
// snapshots pre-image data for DROP COLUMN only.
func (s *QueryService) captureAlterTable(
	ctx context.Context,
	tx Tx,
	info domain.AlterTableInfo,
) (*domain.ChangeSnapshot, *domain.DDLAlterMetadata, error) {
	meta := &domain.DDLAlterMetadata{}
	switch info.Subtype {
	case domain.AlterTableAddColumn:
		meta.ColumnName = info.ColumnName
		return nil, meta, nil
	case domain.AlterTableRenameColumn:
		meta.ColumnName = info.ColumnName
		meta.NewColumnName = info.NewColumnName
		return nil, meta, nil
	case domain.AlterTableRenameTo:
		meta.NewTableName = info.NewTableName
		return nil, meta, nil
	case domain.AlterTableDropColumn:
		meta.ColumnName = info.ColumnName
		snap, err := s.captureDropColumn(ctx, tx, info.TableName, info.ColumnName)
		return snap, meta, err
	}
	return nil, nil, fmt.Errorf("unsupported ALTER TABLE subtype: %v", info.Subtype)
}

// captureDropColumn reads the column metadata (pragma_table_info), the
// pre-DROP CREATE TABLE body + column list, and the column's
// (rowid, value) pairs (up to the snapshot cap).
func (s *QueryService) captureDropColumn(
	ctx context.Context,
	tx Tx,
	tableName, columnName string,
) (*domain.ChangeSnapshot, error) {
	colMeta, preDropCols, err := readColumnsForCapture(ctx, tx, tableName, columnName)
	if err != nil {
		return nil, fmt.Errorf("reading column metadata: %w", err)
	}

	tableBody, err := readTableBody(ctx, tx, tableName)
	if err != nil {
		return nil, fmt.Errorf("reading table body: %w", err)
	}

	policy, err := s.evaluateSnapshotPolicy(ctx, tx, tableName)
	if err != nil {
		return nil, fmt.Errorf("evaluating snapshot policy: %w", err)
	}

	rows, err := readDroppedColumnRows(ctx, tx, tableName, columnName, policy.Cap)
	if err != nil {
		return nil, fmt.Errorf("reading column rows: %w", err)
	}

	snap := domain.BuildDropColumnSnapshot(tableName, colMeta, tableBody, preDropCols, rows, policy.Total)
	return &snap, nil
}

// captureDropTable reads the table's full CREATE statement, all dependent
// schema objects (indexes/views/user-triggers), _skill_targets rows, and
// the row payload (up to the snapshot cap).
//
// Virtual tables (e.g. FTS5) take a different shape: only the verbatim
// CREATE VIRTUAL TABLE statement and any _skill_targets rows are captured.
// Row payload is skipped (the source table holds the authoritative data
// for external-content FTS5, and contentless / standalone vtables can't
// be re-INSERTed through a generic path). Dependent indexes/views/triggers
// are also skipped because SQLite forbids those on virtual tables.
func (s *QueryService) captureDropTable(
	ctx context.Context,
	tx Tx,
	tableName string,
) (*domain.ChangeSnapshot, error) {
	createSQL, err := readSqliteMasterSQL(ctx, tx, tableName)
	if err != nil {
		return nil, fmt.Errorf("reading table body: %w", err)
	}
	if isCreateVirtualTable(createSQL) {
		return captureDropVirtualTable(ctx, tx, tableName, createSQL)
	}

	tableBody, preDropCols, err := readTableBodyAndColumns(ctx, tx, tableName, createSQL)
	if err != nil {
		return nil, fmt.Errorf("reading table body: %w", err)
	}

	deps := []domain.DependentObject{
		{
			Kind:    domain.DependentKindTable,
			Name:    tableName,
			SQL:     tableBody,
			Columns: preDropCols,
		},
	}

	schemaDeps, err := readSchemaDependents(ctx, tx, tableName)
	if err != nil {
		return nil, fmt.Errorf("reading dependents: %w", err)
	}
	deps = append(deps, schemaDeps...)

	skillTargets, err := readSkillTargets(ctx, tx, tableName)
	if err != nil {
		return nil, fmt.Errorf("reading skill_targets: %w", err)
	}
	deps = append(deps, skillTargets...)

	policy, err := s.evaluateSnapshotPolicy(ctx, tx, tableName)
	if err != nil {
		return nil, fmt.Errorf("evaluating snapshot policy: %w", err)
	}

	rows, err := readFullTableRows(ctx, tx, tableName, preDropCols, policy.Cap)
	if err != nil {
		return nil, fmt.Errorf("reading rows: %w", err)
	}

	snap := domain.BuildDropTableSnapshot(deps, rows, policy.Total)
	return &snap, nil
}

// captureDropVirtualTable builds a ChangeSnapshot for a DROP TABLE on a
// virtual table. Records the verbatim CREATE VIRTUAL TABLE statement and
// any _skill_targets rows pointing at the vtable. No row payload, no
// dependent indexes/views/triggers, no column metadata.
func captureDropVirtualTable(
	ctx context.Context,
	tx Tx,
	tableName, createSQL string,
) (*domain.ChangeSnapshot, error) {
	deps := []domain.DependentObject{
		{
			Kind: domain.DependentKindVirtualTable,
			Name: tableName,
			SQL:  createSQL,
		},
	}

	skillTargets, err := readSkillTargets(ctx, tx, tableName)
	if err != nil {
		return nil, fmt.Errorf("reading skill_targets: %w", err)
	}
	deps = append(deps, skillTargets...)

	snap := domain.BuildDropTableSnapshot(deps, nil, 0)
	return &snap, nil
}

// captureSchemaObject reads the CREATE statement for a DROP INDEX / VIEW /
// TRIGGER target from sqlite_master. The returned snapshot is replayed
// verbatim at undo time.
func captureSchemaObject(
	ctx context.Context,
	tx Tx,
	masterType, objectName string,
	kind domain.DependentObjectKind,
) (*domain.ChangeSnapshot, error) {
	sql := "SELECT sql FROM sqlite_master WHERE type = ? AND name = ?"
	r, err := tx.ExecInternal(ctx, sql, []domain.TypedValue{
		domain.TextValue(masterType),
		domain.TextValue(objectName),
	})
	if err != nil {
		return nil, fmt.Errorf("read sqlite_master for %s %q: %w", masterType, objectName, err)
	}
	if len(r.Rows) == 0 {
		return nil, fmt.Errorf("%s %q not found in sqlite_master", masterType, objectName)
	}
	createSQL, _ := r.Rows[0][0].Value.(string)
	if createSQL == "" {
		return nil, fmt.Errorf("%s %q has empty sqlite_master.sql", masterType, objectName)
	}
	snap := domain.BuildDropSchemaObjectSnapshot(kind, objectName, createSQL)
	return &snap, nil
}

// SnapshotPolicy describes how a destructive-DDL pre-image capture
// should be bounded. v1 always uses SnapshotModeTruncate; a future
// SnapshotModeRejectWithConfirmation can be added without changing the
// call site or response envelope.
type SnapshotPolicy struct {
	Cap   int
	Total int
	Mode  SnapshotMode
}

// SnapshotMode enumerates the policy responses when row count > cap.
type SnapshotMode int

// SnapshotMode values.
const (
	SnapshotModeTruncate SnapshotMode = iota
	// SnapshotModeRejectWithConfirmation is reserved for future use; v1
	// always returns SnapshotModeTruncate.
)

// evaluateSnapshotPolicy reads the source table's row count and the
// per-role snapshot cap, and decides how to bound the capture. The
// decision lives in this dedicated function so a future "reject with
// confirmation token" mode is a one-branch swap — no call-site or
// response-envelope changes required.
func (s *QueryService) evaluateSnapshotPolicy(
	ctx context.Context,
	tx Tx,
	tableName string,
) (SnapshotPolicy, error) {
	roles := authz.RolesFromContext(ctx)
	maxRows := s.quotaResolver(roles).SnapshotMaxRows
	if maxRows <= 0 {
		maxRows = domain.DefaultSnapshotMaxRows
	}

	r, err := tx.ExecInternal(ctx, domain.FormatCaptureRowCountSQL(tableName), nil)
	if err != nil {
		return SnapshotPolicy{}, fmt.Errorf("count rows: %w", err)
	}
	var total int
	if len(r.Rows) > 0 && len(r.Rows[0]) > 0 {
		if n, ok := r.Rows[0][0].Value.(int64); ok {
			total = int(n)
		}
	}
	return SnapshotPolicy{
		Cap:   maxRows,
		Total: total,
		Mode:  SnapshotModeTruncate,
	}, nil
}

// --- internal helpers ---

// readColumnsForCapture returns the captured ColumnSnapshot for the named
// column (with Position from pragma_table_info.cid) plus the full
// pre-DROP column-name list in declaration order.
func readColumnsForCapture(
	ctx context.Context,
	tx Tx,
	tableName, columnName string,
) (domain.ColumnSnapshot, []string, error) {
	r, err := tx.ExecInternal(ctx, domain.PragmaTableInfoSQL(tableName), nil)
	if err != nil {
		return domain.ColumnSnapshot{}, nil, err
	}
	preDropCols := make([]string, 0, len(r.Rows))
	var target *domain.ColumnSnapshot
	for i, row := range r.Rows {
		if len(row) < 6 {
			continue
		}
		name, _ := row[1].Value.(string)
		preDropCols = append(preDropCols, name)
		if name == columnName {
			colType, _ := row[2].Value.(string)
			var notNull bool
			if n, ok := row[3].Value.(int64); ok {
				notNull = n != 0
			}
			var dflt *string
			if dv, ok := row[4].Value.(string); ok {
				dflt = &dv
			}
			var pk bool
			if p, ok := row[5].Value.(int64); ok {
				pk = p != 0
			}
			target = &domain.ColumnSnapshot{
				Name:       name,
				Type:       colType,
				NotNull:    notNull,
				DfltValue:  dflt,
				PrimaryKey: pk,
				Position:   i,
			}
		}
	}
	if target == nil {
		return domain.ColumnSnapshot{}, nil, fmt.Errorf("column %q not found in %q", columnName, tableName)
	}
	return *target, preDropCols, nil
}

// readTableBody parses the table's CREATE statement from sqlite_master
// and returns the body — everything after `CREATE TABLE <name>`. The
// name is dropped (we already know it) so the body can be re-attached
// to any name at undo time.
func readTableBody(ctx context.Context, tx Tx, tableName string) (string, error) {
	r, err := tx.ExecInternal(ctx, domain.CaptureTableCreateSQLQuery, []domain.TypedValue{
		domain.TextValue(tableName),
	})
	if err != nil {
		return "", fmt.Errorf("read sqlite_master.sql: %w", err)
	}
	if len(r.Rows) == 0 {
		return "", fmt.Errorf("table %q not found in sqlite_master", tableName)
	}
	createSQL, _ := r.Rows[0][0].Value.(string)
	if createSQL == "" {
		return "", fmt.Errorf("table %q has empty sqlite_master.sql", tableName)
	}
	_, body, err := domain.ParseCreateTableNameAndBody(createSQL)
	if err != nil {
		return "", fmt.Errorf("parse CREATE TABLE: %w", err)
	}
	return body, nil
}

// readSqliteMasterSQL returns the raw sqlite_master.sql for the named table.
// Empty result and empty SQL strings are both surfaced as errors so callers
// don't proceed with an empty body.
func readSqliteMasterSQL(ctx context.Context, tx Tx, tableName string) (string, error) {
	r, err := tx.ExecInternal(ctx, domain.CaptureTableCreateSQLQuery, []domain.TypedValue{
		domain.TextValue(tableName),
	})
	if err != nil {
		return "", fmt.Errorf("read sqlite_master.sql: %w", err)
	}
	if len(r.Rows) == 0 {
		return "", fmt.Errorf("table %q not found in sqlite_master", tableName)
	}
	createSQL, _ := r.Rows[0][0].Value.(string)
	if createSQL == "" {
		return "", fmt.Errorf("table %q has empty sqlite_master.sql", tableName)
	}
	return createSQL, nil
}

// isCreateVirtualTable reports whether the given sqlite_master.sql text is
// a CREATE VIRTUAL TABLE statement (case-insensitive, whitespace-tolerant).
func isCreateVirtualTable(createSQL string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(createSQL)), "CREATE VIRTUAL TABLE")
}

// readTableBodyAndColumns parses the CREATE TABLE body out of an already-read
// sqlite_master.sql and reads the column-name list via pragma_table_info. The
// caller passes the pre-fetched createSQL to avoid a second round-trip.
func readTableBodyAndColumns(
	ctx context.Context, tx Tx, tableName, createSQL string,
) (string, []string, error) {
	_, body, err := domain.ParseCreateTableNameAndBody(createSQL)
	if err != nil {
		return "", nil, fmt.Errorf("parse CREATE TABLE: %w", err)
	}
	r, err := tx.ExecInternal(ctx, domain.PragmaTableInfoSQL(tableName), nil)
	if err != nil {
		return "", nil, fmt.Errorf("read pragma_table_info: %w", err)
	}
	cols := make([]string, 0, len(r.Rows))
	for _, row := range r.Rows {
		if len(row) < 2 {
			continue
		}
		if name, ok := row[1].Value.(string); ok {
			cols = append(cols, name)
		}
	}
	return body, cols, nil
}

// readSchemaDependents returns CREATE INDEX / TRIGGER / VIEW statements
// that reference the given table, excluding SQLite's auto-indexes and
// the platform's _change_log_<table>_* triggers.
func readSchemaDependents(ctx context.Context, tx Tx, tableName string) ([]domain.DependentObject, error) {
	r, err := tx.ExecInternal(ctx, domain.CaptureDependentObjectsQuery, []domain.TypedValue{
		domain.TextValue(tableName),
	})
	if err != nil {
		return nil, err
	}
	deps := make([]domain.DependentObject, 0, len(r.Rows))
	for _, row := range r.Rows {
		if len(row) < 3 {
			continue
		}
		typ, _ := row[0].Value.(string)
		name, _ := row[1].Value.(string)
		sql, _ := row[2].Value.(string)
		if sql == "" {
			continue
		}
		var kind domain.DependentObjectKind
		switch typ {
		case "index":
			kind = domain.DependentKindIndex
		case "trigger":
			kind = domain.DependentKindTrigger
		case "view":
			kind = domain.DependentKindView
		default:
			continue
		}
		deps = append(deps, domain.DependentObject{Kind: kind, Name: name, SQL: sql})
	}
	return deps, nil
}

// readSkillTargets returns _skill_targets rows pointing at the given
// table, JSON-encoded into DependentObject.SQL so the revert generator
// can replay them as INSERT statements.
func readSkillTargets(ctx context.Context, tx Tx, tableName string) ([]domain.DependentObject, error) {
	r, err := tx.ExecInternal(ctx, domain.CaptureSkillTargetsQuery, []domain.TypedValue{
		domain.TextValue(tableName),
	})
	if err != nil {
		return nil, err
	}
	out := make([]domain.DependentObject, 0, len(r.Rows))
	for _, row := range r.Rows {
		if len(row) < 2 {
			continue
		}
		skillID, _ := row[0].Value.(string)
		tname, _ := row[1].Value.(string)
		payload, err := json.Marshal(map[string]string{
			"skill_id":   skillID,
			"table_name": tname,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal skill_target: %w", err)
		}
		out = append(out, domain.DependentObject{
			Kind: domain.DependentKindSkillTarget,
			Name: skillID + ":" + tname,
			SQL:  string(payload),
		})
	}
	return out, nil
}

// readDroppedColumnRows reads (rowid, value) pairs for a single column,
// capped by limit (0 means no limit). Used by DROP COLUMN capture.
func readDroppedColumnRows(
	ctx context.Context,
	tx Tx,
	tableName, columnName string,
	limit int,
) ([]domain.SnapshotRow, error) {
	sql := domain.FormatCaptureColumnRowsSQL(tableName, columnName)
	r, err := tx.ExecInternal(ctx, sql, []domain.TypedValue{domain.IntegerValue(int64(limit))})
	if err != nil {
		return nil, err
	}
	out := make([]domain.SnapshotRow, 0, len(r.Rows))
	for _, row := range r.Rows {
		if len(row) < 2 {
			continue
		}
		rowID, _ := row[0].Value.(int64)
		out = append(out, domain.SnapshotRow{
			RowID:  rowID,
			Values: map[string]any{columnName: typedValueToAny(row[1])},
		})
	}
	return out, nil
}

// readFullTableRows reads up to limit rows from the table using columns
// from preDropCols (ensures predictable column ordering in the captured
// row map, regardless of how the storage engine returns SELECT *).
func readFullTableRows(
	ctx context.Context,
	tx Tx,
	tableName string,
	preDropCols []string,
	limit int,
) ([]domain.SnapshotRow, error) {
	if len(preDropCols) == 0 {
		return nil, nil
	}
	sql := fmt.Sprintf("SELECT rowid, * FROM %s LIMIT ?", domain.QuoteIdentifier(tableName))
	r, err := tx.ExecInternal(ctx, sql, []domain.TypedValue{domain.IntegerValue(int64(limit))})
	if err != nil {
		return nil, err
	}
	out := make([]domain.SnapshotRow, 0, len(r.Rows))
	for _, row := range r.Rows {
		if len(row) < 1+len(preDropCols) {
			continue
		}
		rowID, _ := row[0].Value.(int64)
		values := make(map[string]any, len(preDropCols))
		for i, col := range preDropCols {
			values[col] = typedValueToAny(row[1+i])
		}
		out = append(out, domain.SnapshotRow{RowID: rowID, Values: values})
	}
	return out, nil
}

// typedValueToAny unwraps a TypedValue into the bare Go value used inside
// snapshot maps (so JSON marshaling produces plain ints / floats / strings).
func typedValueToAny(v domain.TypedValue) any {
	return v.Value
}
