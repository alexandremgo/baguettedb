package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// columnTypePattern matches declared column types that are safe to splice into generated DDL: letters,
// digits, underscores, spaces, parens, and commas — e.g. "TEXT", "VARCHAR(30)", "DECIMAL(10,5)".
var columnTypePattern = regexp.MustCompile(`^[a-zA-Z0-9_ (),]*$`)

// sanitizeColumnType returns declType when it is safe to splice into generated DDL, and "" otherwise.
// SQLite declared types are free-form text, so a hostile or exotic type read back from the schema degrades
// to an untyped column — the restored data is unchanged, only insert-time affinity is lost.
func sanitizeColumnType(declType string) string {
	if columnTypePattern.MatchString(declType) {
		return declType
	}
	return ""
}

// ErrInvalidRevertEntry indicates that an entry passed to GenerateRevertDDL
// is not a valid DDL change-log entry (wrong kind, missing required fields,
// or an unrecognized subtype).
var ErrInvalidRevertEntry = errors.New("invalid revert entry")

// ErrMissingSnapshot indicates that the inverse of a destructive DDL change
// requires snapshot data that wasn't provided.
var ErrMissingSnapshot = errors.New("missing snapshot for destructive DDL undo")

// ErrMissingDDLMetadata indicates that an ALTER TABLE inverse needs
// DDLMetadata fields (column name, new table name) that weren't set on
// the entry.
var ErrMissingDDLMetadata = errors.New("missing ALTER TABLE metadata")

// GenerateRevertDDL returns the unit that reverses a DDL change: the shape the change log records for the reversal
// and the statements that perform it. For destructive subtypes (DROP TABLE, DROP COLUMN, DROP INDEX / VIEW /
// TRIGGER), snapshot must be non-nil and populated; for non-destructive subtypes (CREATE *, ADD COLUMN, RENAME *),
// snapshot may be nil.
//
// The unit's statements run in order inside the caller's reversal transaction. Domain stays pure — execution is the
// caller's job.
func GenerateRevertDDL(entry *ChangeLogEntry, snapshot *ChangeSnapshot) (DDLUnit, error) {
	if entry == nil {
		return DDLUnit{}, fmt.Errorf("%w: nil entry", ErrInvalidRevertEntry)
	}
	if !entry.IsDDL() {
		return DDLUnit{}, fmt.Errorf("%w: kind=%s (expected DDL)", ErrInvalidRevertEntry, entry.Kind)
	}
	switch entry.Operation {
	case ChangeOpCreate:
		return revertCreate(entry)
	case ChangeOpDrop:
		if snapshot == nil {
			return DDLUnit{}, fmt.Errorf("%w: DROP %s requires snapshot", ErrMissingSnapshot, entry.ObjectType)
		}
		return revertDrop(entry, snapshot)
	case ChangeOpAlter:
		return revertAlter(entry, snapshot)
	}
	return DDLUnit{}, fmt.Errorf("%w: unrecognized operation %q", ErrInvalidRevertEntry, entry.Operation)
}

// inverseUnit builds the unit that records a computed inverse: objectName is the object the recorded statement
// targets, sql the recorded statement, and exec the statements that perform it — sql itself when exec is empty.
func inverseUnit(ddlType DDLType, objectName, sql string, exec ...SQLStatement) DDLUnit {
	if len(exec) == 0 {
		exec = []SQLStatement{{SQL: sql}}
	}
	return DDLUnit{
		Shape: ClassifiedStatement{SQL: sql, Type: StatementDDL, DDLType: ddlType, Tables: []string{objectName}},
		Exec:  exec,
	}
}

// --- CREATE inverse ---

func revertCreate(entry *ChangeLogEntry) (DDLUnit, error) {
	if entry.ObjectName == "" {
		return DDLUnit{}, fmt.Errorf("%w: CREATE %s missing object name", ErrInvalidRevertEntry, entry.ObjectType)
	}
	name := QuoteIdentifier(entry.ObjectName)
	switch entry.ObjectType {
	case ChangeObjectTable:
		return inverseUnit(DDLDropTable, entry.ObjectName, "DROP TABLE "+name), nil
	case ChangeObjectIndex:
		return inverseUnit(DDLDropIndex, entry.ObjectName, "DROP INDEX "+name), nil
	case ChangeObjectView:
		return inverseUnit(DDLDropView, entry.ObjectName, "DROP VIEW "+name), nil
	case ChangeObjectTrigger:
		return inverseUnit(DDLDropTrigger, entry.ObjectName, "DROP TRIGGER "+name), nil
	}
	return DDLUnit{}, fmt.Errorf("%w: unrecognized object_type %q for CREATE", ErrInvalidRevertEntry, entry.ObjectType)
}

// --- DROP inverse ---

func revertDrop(entry *ChangeLogEntry, snapshot *ChangeSnapshot) (DDLUnit, error) {
	if entry.ObjectName == "" {
		return DDLUnit{}, fmt.Errorf("%w: DROP %s missing object name", ErrInvalidRevertEntry, entry.ObjectType)
	}
	switch entry.ObjectType {
	case ChangeObjectTable:
		return revertDropTable(entry, snapshot)
	case ChangeObjectIndex:
		return revertDropSchemaObject(entry, snapshot, DependentKindIndex, DDLCreateIndex, "INDEX")
	case ChangeObjectView:
		return revertDropSchemaObject(entry, snapshot, DependentKindView, DDLCreateView, "VIEW")
	case ChangeObjectTrigger:
		return revertDropSchemaObject(entry, snapshot, DependentKindTrigger, DDLCreateTrigger, "TRIGGER")
	}
	return DDLUnit{}, fmt.Errorf("%w: unrecognized object_type %q for DROP", ErrInvalidRevertEntry, entry.ObjectType)
}

// revertDropSchemaObject replays the captured CREATE statement for a dropped INDEX / VIEW / TRIGGER, recorded under
// ddlType. The snapshot's first DependentObject of the matching kind carries the CREATE text from sqlite_master.
func revertDropSchemaObject(
	entry *ChangeLogEntry, snapshot *ChangeSnapshot, kind DependentObjectKind, ddlType DDLType, label string,
) (DDLUnit, error) {
	for _, dep := range snapshot.DependentObjects {
		if dep.Kind == kind {
			return inverseUnit(ddlType, entry.ObjectName, dep.SQL), nil
		}
	}
	return DDLUnit{}, fmt.Errorf("%w: DROP %s snapshot missing %s entry", ErrMissingSnapshot, label, kind)
}

// revertDropTable replays the full pre-image of a dropped table: the
// CREATE TABLE statement, bulk INSERT of the captured rows, dependent
// CREATE INDEX / VIEW / TRIGGER, and INSERTs into _skill_targets for
// any skills that targeted the table.
//
// Statement ordering: CREATE TABLE → bulk INSERT rows → CREATE INDEX → CREATE
// VIEW → CREATE TRIGGER → INSERT _skill_targets. The platform's per-table
// _change_log triggers regenerate via the service-layer post-DDL pass and
// are NOT included in DependentObjects.
//
// Virtual tables (e.g. FTS5) take a different path: the snapshot carries a
// DependentKindVirtualTable entry whose SQL is the verbatim CREATE VIRTUAL
// TABLE statement. Replay emits the CREATE verbatim, skips the row-payload
// INSERT (none was captured — the index is derived from a source table or
// rebuildable via FTS5's 'rebuild' command), and skips index/view/trigger
// replay (SQLite forbids those on virtual tables). _skill_targets restoration
// still runs.
func revertDropTable(entry *ChangeLogEntry, snapshot *ChangeSnapshot) (DDLUnit, error) {
	var tableDep, vtableDep *DependentObject
	var indexes, views, triggers, skillTargets []DependentObject
	for i, dep := range snapshot.DependentObjects {
		switch dep.Kind {
		case DependentKindTable:
			tableDep = &snapshot.DependentObjects[i]
		case DependentKindVirtualTable:
			vtableDep = &snapshot.DependentObjects[i]
		case DependentKindIndex:
			indexes = append(indexes, dep)
		case DependentKindView:
			views = append(views, dep)
		case DependentKindTrigger:
			triggers = append(triggers, dep)
		case DependentKindSkillTarget:
			skillTargets = append(skillTargets, dep)
		}
	}

	if vtableDep != nil {
		stmts := []SQLStatement{{SQL: vtableDep.SQL}}
		for _, dep := range skillTargets {
			stmt, err := buildSkillTargetInsert(dep.SQL)
			if err != nil {
				return DDLUnit{}, fmt.Errorf("skill_targets restore: %w", err)
			}
			stmts = append(stmts, stmt)
		}
		return inverseUnit(DDLCreateTable, entry.ObjectName, vtableDep.SQL, stmts...), nil
	}

	if tableDep == nil {
		return DDLUnit{}, fmt.Errorf("%w: DROP TABLE snapshot missing TABLE entry", ErrMissingSnapshot)
	}

	createSQL := fmt.Sprintf("CREATE TABLE %s %s", QuoteIdentifier(entry.ObjectName), tableDep.SQL)
	stmts := []SQLStatement{{SQL: createSQL}}

	if len(snapshot.RowPayload) > 0 {
		insert, err := buildBulkInsert(entry.ObjectName, snapshot.RowPayload)
		if err != nil {
			return DDLUnit{}, fmt.Errorf("bulk insert: %w", err)
		}
		stmts = append(stmts, insert)
	}

	for _, dep := range indexes {
		stmts = append(stmts, SQLStatement{SQL: dep.SQL})
	}
	for _, dep := range views {
		stmts = append(stmts, SQLStatement{SQL: dep.SQL})
	}
	for _, dep := range triggers {
		stmts = append(stmts, SQLStatement{SQL: dep.SQL})
	}

	for _, dep := range skillTargets {
		stmt, err := buildSkillTargetInsert(dep.SQL)
		if err != nil {
			return DDLUnit{}, fmt.Errorf("skill_targets restore: %w", err)
		}
		stmts = append(stmts, stmt)
	}

	return inverseUnit(DDLCreateTable, entry.ObjectName, createSQL, stmts...), nil
}

// --- ALTER inverse ---

func revertAlter(entry *ChangeLogEntry, snapshot *ChangeSnapshot) (DDLUnit, error) {
	if entry.ObjectName == "" {
		return DDLUnit{}, fmt.Errorf("%w: ALTER missing table name", ErrInvalidRevertEntry)
	}
	switch entry.DDLSubtype {
	case DDLSubtypeAddColumn:
		return revertAddColumn(entry)
	case DDLSubtypeDropColumn:
		if snapshot == nil {
			return DDLUnit{}, fmt.Errorf("%w: DROP COLUMN requires snapshot", ErrMissingSnapshot)
		}
		return revertDropColumn(entry, snapshot)
	case DDLSubtypeRenameColumn:
		return revertRenameColumn(entry)
	case DDLSubtypeRenameTo:
		return revertRenameTo(entry)
	}
	return DDLUnit{}, fmt.Errorf("%w: unrecognized ddl_subtype %q", ErrInvalidRevertEntry, entry.DDLSubtype)
}

// revertAddColumn inverts ALTER TABLE t ADD COLUMN c → ALTER TABLE t DROP COLUMN c.
func revertAddColumn(entry *ChangeLogEntry) (DDLUnit, error) {
	if entry.DDLMetadata == nil || entry.DDLMetadata.ColumnName == "" {
		return DDLUnit{}, fmt.Errorf("%w: ADD COLUMN missing column name", ErrMissingDDLMetadata)
	}
	column := entry.DDLMetadata.ColumnName
	unit := inverseUnit(DDLAlterTable, entry.ObjectName, fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s",
		QuoteIdentifier(entry.ObjectName), QuoteIdentifier(column)))
	unit.Alter = AlterTableInfo{Subtype: AlterTableDropColumn, TableName: entry.ObjectName, ColumnName: column}
	return unit, nil
}

// revertRenameColumn inverts ALTER TABLE t RENAME COLUMN a TO b → rename b TO a.
func revertRenameColumn(entry *ChangeLogEntry) (DDLUnit, error) {
	if entry.DDLMetadata == nil || entry.DDLMetadata.ColumnName == "" || entry.DDLMetadata.NewColumnName == "" {
		return DDLUnit{}, fmt.Errorf("%w: RENAME COLUMN missing old/new column name", ErrMissingDDLMetadata)
	}
	oldName := entry.DDLMetadata.ColumnName
	newName := entry.DDLMetadata.NewColumnName
	unit := inverseUnit(DDLAlterTable, entry.ObjectName, fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s",
		QuoteIdentifier(entry.ObjectName), QuoteIdentifier(newName), QuoteIdentifier(oldName)))
	unit.Alter = AlterTableInfo{
		Subtype: AlterTableRenameColumn, TableName: entry.ObjectName, ColumnName: newName, NewColumnName: oldName,
	}
	return unit, nil
}

// revertRenameTo inverts ALTER TABLE t RENAME TO u → rename u back to t. The unit targets u, the name the table has
// when it runs; the _skill_targets cascade is the pipeline's, as for any rename.
func revertRenameTo(entry *ChangeLogEntry) (DDLUnit, error) {
	if entry.DDLMetadata == nil || entry.DDLMetadata.NewTableName == "" {
		return DDLUnit{}, fmt.Errorf("%w: RENAME TO missing new table name", ErrMissingDDLMetadata)
	}
	oldName := entry.ObjectName
	newName := entry.DDLMetadata.NewTableName
	rename := fmt.Sprintf("ALTER TABLE %s RENAME TO %s", QuoteIdentifier(newName), QuoteIdentifier(oldName))
	unit := inverseUnit(DDLAlterTable, newName, rename)
	unit.Alter = AlterTableInfo{Subtype: AlterTableRenameTo, TableName: newName, NewTableName: oldName}
	return unit, nil
}

// revertDropColumn inverts ALTER TABLE t DROP COLUMN c. Two paths:
//
//   - Full table-rebuild (default): drops the table and recreates it from
//     the captured pre-DDL CREATE TABLE SQL, joining the post-drop table
//     to a temp table of captured (rowid, value) pairs so the column is
//     restored with the original constraints (NOT NULL, CHECK, UNIQUE,
//     COLLATE, GENERATED). Dependent indexes / triggers / views replay
//     after the rename.
//
//   - Truncated-snapshot fallback (snapshot.Truncated == true): the
//     rebuild can't safely write NULL into a NOT NULL column for the rows
//     beyond the cap, so this path falls back to ALTER TABLE ADD COLUMN
//     nullable + bulk UPDATE keyed by rowid. The column's constraints are
//     lost; the rows beyond the cap come back NULL.
//
// Either way the unit is recorded as the ADD COLUMN that declares the restored column: with its constraints on the
// rebuild path, as the plain nullable column the fallback actually adds.
func revertDropColumn(entry *ChangeLogEntry, snapshot *ChangeSnapshot) (DDLUnit, error) {
	if snapshot.ColumnMetadata == nil {
		return DDLUnit{}, fmt.Errorf("%w: DROP COLUMN snapshot missing ColumnMetadata", ErrMissingSnapshot)
	}
	if snapshot.Truncated {
		return revertDropColumnNullableFallback(entry, snapshot)
	}
	return revertDropColumnRebuild(entry, snapshot)
}

// revertDropColumnRebuild emits the full table-rebuild sequence.
// Statement ordering:
//  1. CREATE TEMP TABLE _captured_<col> (rowid INTEGER PRIMARY KEY, value)
//  2. INSERT INTO _captured_<col> VALUES ...  (multi-VALUES from snapshot)
//  3. CREATE TABLE _revert_rebuild_<table> <captured-body>
//  4. INSERT INTO _revert_rebuild_<table> (cols...)
//     SELECT t.col1, captured.value, t.col3 FROM t LEFT JOIN _captured_<col>
//  5. DROP TABLE t
//  6. ALTER TABLE _revert_rebuild_<table> RENAME TO t
//  7. Replay dependent INDEX / VIEW / TRIGGER CREATE statements
//  8. DROP TABLE _captured_<col>
//
// The captured CREATE TABLE body (everything after `CREATE TABLE <name>`)
// is taken verbatim from DependentKindTable.SQL — at capture time the
// table name is parsed off and stored separately as DependentObject.Name,
// so we never re-parse user SQL here.
//
// The populate SQL (step 4) enumerates columns explicitly using the
// captured column order in DependentKindTable.Columns, with the dropped
// column's slot filled from _captured_<col>.value at its original
// position. This correctly handles columns dropped from the middle of a
// table, not just trailing columns.
func revertDropColumnRebuild(entry *ChangeLogEntry, snapshot *ChangeSnapshot) (DDLUnit, error) {
	col := snapshot.ColumnMetadata
	table := entry.ObjectName
	if table == "" {
		return DDLUnit{}, fmt.Errorf("%w: DROP COLUMN missing table name", ErrInvalidRevertEntry)
	}
	if col.Name == "" {
		return DDLUnit{}, fmt.Errorf("%w: DROP COLUMN ColumnMetadata.Name empty", ErrMissingSnapshot)
	}

	tempTable := "_captured_" + col.Name
	shadowTable := "_revert_rebuild_" + table

	// Locate the captured table body + column list.
	var tableDep *DependentObject
	var dependents []DependentObject
	for i, dep := range snapshot.DependentObjects {
		if dep.Kind == DependentKindTable {
			tableDep = &snapshot.DependentObjects[i]
		} else {
			dependents = append(dependents, dep)
		}
	}
	if tableDep == nil {
		return DDLUnit{}, fmt.Errorf("%w: DROP COLUMN rebuild needs DependentKindTable entry", ErrMissingSnapshot)
	}
	if len(tableDep.Columns) == 0 {
		return DDLUnit{}, fmt.Errorf("%w: DROP COLUMN rebuild needs DependentKindTable.Columns populated",
			ErrMissingSnapshot)
	}
	shadowCreateSQL := fmt.Sprintf("CREATE TABLE %s %s", QuoteIdentifier(shadowTable), tableDep.SQL)

	stmts := []SQLStatement{
		{SQL: fmt.Sprintf("CREATE TEMP TABLE %s (rowid INTEGER PRIMARY KEY, value %s)",
			QuoteIdentifier(tempTable), sanitizeColumnType(col.Type))},
	}

	if len(snapshot.RowPayload) > 0 {
		captured, err := buildCapturedValuesInsert(tempTable, col.Name, snapshot.RowPayload)
		if err != nil {
			return DDLUnit{}, fmt.Errorf("captured values insert: %w", err)
		}
		stmts = append(stmts, captured)
	}

	// CREATE the shadow table.
	stmts = append(stmts, SQLStatement{SQL: shadowCreateSQL})

	// Populate the shadow by joining the live (post-drop) table to the
	// temp table on rowid. Captured column lands at its original position.
	stmts = append(stmts, SQLStatement{
		SQL: buildShadowPopulateSQL(table, shadowTable, tempTable, col.Name, tableDep.Columns),
	})

	stmts = append(stmts,
		SQLStatement{SQL: "DROP TABLE " + QuoteIdentifier(table)},
		SQLStatement{SQL: fmt.Sprintf("ALTER TABLE %s RENAME TO %s",
			QuoteIdentifier(shadowTable), QuoteIdentifier(table))},
	)

	for _, dep := range dependents {
		stmts = append(stmts, SQLStatement{SQL: dep.SQL})
	}

	stmts = append(stmts, SQLStatement{SQL: "DROP TABLE " + QuoteIdentifier(tempTable)})

	return addColumnUnit(table, *col, stmts), nil
}

// addColumnUnit records a restored column as the ADD COLUMN declaring col on table, performed by exec.
func addColumnUnit(table string, col ColumnSnapshot, exec []SQLStatement) DDLUnit {
	unit := inverseUnit(DDLAlterTable, table, addColumnStatement(table, col), exec...)
	unit.Alter = AlterTableInfo{Subtype: AlterTableAddColumn, TableName: table, ColumnName: col.Name}
	return unit
}

// addColumnStatement renders the ADD COLUMN statement that declares col on table: the declared type when it is safe
// to splice, then PRIMARY KEY, NOT NULL, and DEFAULT as recorded, each omitted when absent.
func addColumnStatement(table string, col ColumnSnapshot) string {
	parts := []string{fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", QuoteIdentifier(table), QuoteIdentifier(col.Name))}

	if typ := sanitizeColumnType(col.Type); typ != "" {
		parts = append(parts, typ)
	}
	if col.PrimaryKey {
		parts = append(parts, "PRIMARY KEY")
	}
	if col.NotNull {
		parts = append(parts, "NOT NULL")
	}
	if col.DfltValue != nil {
		parts = append(parts, "DEFAULT "+*col.DfltValue)
	}
	return strings.Join(parts, " ")
}

// revertDropColumnNullableFallback emits the simple ADD COLUMN + UPDATE
// path used when the snapshot was truncated. Drops NOT NULL since the
// rows beyond the cap would violate it.
func revertDropColumnNullableFallback(entry *ChangeLogEntry, snapshot *ChangeSnapshot) (DDLUnit, error) {
	nullable := ColumnSnapshot{Name: snapshot.ColumnMetadata.Name, Type: snapshot.ColumnMetadata.Type}
	stmts := []SQLStatement{{SQL: addColumnStatement(entry.ObjectName, nullable)}}
	if len(snapshot.RowPayload) == 0 {
		return addColumnUnit(entry.ObjectName, nullable, stmts), nil
	}
	// One UPDATE per row keyed by rowid — collapsing into a single SQL with
	// a CASE expression is possible but the per-row form is simpler and
	// the row count is bounded by the snapshot cap.
	for _, row := range snapshot.RowPayload {
		val, ok := row.Values[nullable.Name]
		if !ok {
			continue
		}
		stmts = append(stmts, SQLStatement{
			SQL: fmt.Sprintf("UPDATE %s SET %s = ? WHERE rowid = ?",
				QuoteIdentifier(entry.ObjectName),
				QuoteIdentifier(nullable.Name)),
			Args: []TypedValue{toTypedValue(val), IntegerValue(row.RowID)},
		})
	}
	return addColumnUnit(entry.ObjectName, nullable, stmts), nil
}

// buildShadowPopulateSQL builds the INSERT … SELECT that copies live rows
// from the post-drop table into the shadow, joining on rowid to recover
// the captured column values. preDropColumns is the table's column-name
// list in declaration order (including the dropped column at its
// original position); capturedCol is the dropped column's name. The
// SELECT list pulls each non-dropped column from the live table aliased
// as `t`, and the dropped column from the temp `<tempTable>.value`,
// preserving the original column order.
func buildShadowPopulateSQL(table, shadowTable, tempTable, capturedCol string, preDropColumns []string) string {
	insertCols := make([]string, 0, len(preDropColumns)+1)
	selectExprs := make([]string, 0, len(preDropColumns)+1)
	// Rows keep their rowid across the rebuild so change-log entries recorded against them stay addressable.
	if !shadowsRowID(preDropColumns) {
		insertCols = append(insertCols, "rowid")
		selectExprs = append(selectExprs, "t.rowid")
	}
	for _, col := range preDropColumns {
		insertCols = append(insertCols, QuoteIdentifier(col))
		if col == capturedCol {
			selectExprs = append(selectExprs, QuoteIdentifier(tempTable)+".value")
		} else {
			selectExprs = append(selectExprs, "t."+QuoteIdentifier(col))
		}
	}
	return fmt.Sprintf(
		"INSERT INTO %s (%s) SELECT %s FROM %s t LEFT JOIN %s ON t.rowid = %s.rowid",
		QuoteIdentifier(shadowTable),
		strings.Join(insertCols, ", "),
		strings.Join(selectExprs, ", "),
		QuoteIdentifier(table),
		QuoteIdentifier(tempTable),
		QuoteIdentifier(tempTable),
	)
}

// --- helpers ---

// buildBulkInsert emits a single multi-VALUES INSERT that replays a uniform set of captured rows. Each row keeps its
// captured rowid, so change-log entries recorded against it stay addressable, unless a row carries no rowid or a
// column claims a rowid alias; the remaining column order is the sorted union of value-map keys, applied to every row.
func buildBulkInsert(table string, rows []SnapshotRow) (SQLStatement, error) {
	if len(rows) == 0 {
		return SQLStatement{}, errors.New("buildBulkInsert: empty rows")
	}
	cols := sortedKeys(rows[0].Values)
	restoreRowID := !shadowsRowID(cols)
	for i, r := range rows {
		// SQLite assigns rowids from 1, so 0 means the capture recorded no rowid for this row; the column is then
		// omitted for the whole payload rather than mixing rows with and without an explicit identity.
		if r.RowID == 0 {
			restoreRowID = false
		}
		if i == 0 {
			continue
		}
		if len(r.Values) != len(cols) {
			return SQLStatement{}, fmt.Errorf("row %d has %d cols, expected %d", i, len(r.Values), len(cols))
		}
		for _, c := range cols {
			if _, ok := r.Values[c]; !ok {
				return SQLStatement{}, fmt.Errorf("row %d missing column %q", i, c)
			}
		}
	}

	var b strings.Builder
	b.WriteString("INSERT INTO ")
	b.WriteString(QuoteIdentifier(table))
	b.WriteString(" (")
	if restoreRowID {
		b.WriteString("rowid, ")
	}
	for i, c := range cols {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(QuoteIdentifier(c))
	}
	b.WriteString(") VALUES ")

	args := make([]TypedValue, 0, len(rows)*(len(cols)+1))
	for i, r := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("(")
		if restoreRowID {
			b.WriteString("?, ")
			args = append(args, IntegerValue(r.RowID))
		}
		for j, c := range cols {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString("?")
			args = append(args, toTypedValue(r.Values[c]))
		}
		b.WriteString(")")
	}

	return SQLStatement{SQL: b.String(), Args: args}, nil
}

// buildCapturedValuesInsert emits the multi-VALUES INSERT that populates
// the _captured_<col> temp table from a DROP COLUMN snapshot's RowPayload.
func buildCapturedValuesInsert(tempTable, colName string, rows []SnapshotRow) (SQLStatement, error) {
	if len(rows) == 0 {
		return SQLStatement{}, errors.New("buildCapturedValuesInsert: empty rows")
	}
	var b strings.Builder
	b.WriteString("INSERT INTO ")
	b.WriteString(QuoteIdentifier(tempTable))
	b.WriteString(" (rowid, value) VALUES ")

	args := make([]TypedValue, 0, len(rows)*2)
	for i, r := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("(?, ?)")
		args = append(args, IntegerValue(r.RowID))
		val, ok := r.Values[colName]
		if !ok {
			args = append(args, NullValue())
			continue
		}
		args = append(args, toTypedValue(val))
	}

	return SQLStatement{SQL: b.String(), Args: args}, nil
}

// buildSkillTargetInsert parses a captured DependentKindSkillTarget payload
// (JSON {"skill_id":"...","table_name":"..."}) into an INSERT statement.
func buildSkillTargetInsert(payload string) (SQLStatement, error) {
	var row struct {
		SkillID   string `json:"skill_id"`
		TableName string `json:"table_name"`
	}
	if err := json.Unmarshal([]byte(payload), &row); err != nil {
		return SQLStatement{}, fmt.Errorf("parse skill_target snapshot: %w", err)
	}
	if row.SkillID == "" || row.TableName == "" {
		return SQLStatement{}, fmt.Errorf("skill_target snapshot incomplete: skill_id=%q table_name=%q",
			row.SkillID, row.TableName)
	}
	return SQLStatement{
		SQL:  fmt.Sprintf("INSERT INTO %s (skill_id, table_name) VALUES (?, ?)", SkillTargetsTable),
		Args: []TypedValue{TextValue(row.SkillID), TextValue(row.TableName)},
	}, nil
}
