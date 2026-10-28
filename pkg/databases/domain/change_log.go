package domain

import (
	"iter"
	"time"
)

// ChangeLogTable is the unified platform-managed table that records both
// DML row touches and DDL statements after migration v4.
const ChangeLogTable = "_change_log"

// ChangeSnapshotsTable is the sidecar that holds heavy DDL pre-image
// blobs (DROP COLUMN row data, DROP TABLE full snapshots, dependent
// object SQL). Empty for DML entries and non-destructive DDL.
const ChangeSnapshotsTable = "_change_snapshots"

// ChangeKind discriminates a _change_log row as DML or DDL. The CHECK
// constraint on the column enforces these two values at the SQLite level.
type ChangeKind string

// ChangeKind values.
const (
	ChangeKindDML ChangeKind = "DML"
	ChangeKindDDL ChangeKind = "DDL"
)

// IsDML reports whether the change is a DML row touch.
func (k ChangeKind) IsDML() bool { return k == ChangeKindDML }

// IsDDL reports whether the change is a DDL statement.
func (k ChangeKind) IsDDL() bool { return k == ChangeKindDDL }

// Change operation values for _change_log.operation. DML kinds use
// INSERT/UPDATE/DELETE; DDL kinds use CREATE/DROP/ALTER. The DDL subtype
// (AddColumn, DropColumn, RenameColumn, RenameTo, ...) is stored
// separately in the ddl_subtype column.
const (
	ChangeOpInsert = "INSERT"
	ChangeOpUpdate = "UPDATE"
	ChangeOpDelete = "DELETE"
	ChangeOpCreate = "CREATE"
	ChangeOpDrop   = "DROP"
	ChangeOpAlter  = "ALTER"
)

// Object types stored in _change_log.object_type for DDL entries.
const (
	ChangeObjectTable   = "TABLE"
	ChangeObjectIndex   = "INDEX"
	ChangeObjectView    = "VIEW"
	ChangeObjectTrigger = "TRIGGER"
)

// DDL subtype values stored in _change_log.ddl_subtype. Populated for
// ALTER TABLE rows only; NULL for CREATE / DROP / non-ALTER DDL.
const (
	DDLSubtypeAddColumn    = "AddColumn"
	DDLSubtypeDropColumn   = "DropColumn"
	DDLSubtypeRenameColumn = "RenameColumn"
	DDLSubtypeRenameTo     = "RenameTo"
)

// DDLAlterMetadata carries subtype-specific parsed shape for an ALTER TABLE
// _change_log entry. Stored as JSON in _change_log.ddl_metadata. Field
// population by subtype:
//   - AddColumn:    ColumnName (the added column's name)
//   - DropColumn:   ColumnName (the dropped column's name; also in the snapshot's ColumnMetadata.Name)
//   - RenameColumn: ColumnName (old name) + NewColumnName (new name)
//   - RenameTo:     NewTableName (new name; the old name is in ChangeLogEntry.ObjectName)
type DDLAlterMetadata struct {
	ColumnName    string
	NewColumnName string
	NewTableName  string
}

// Origin values describe how a _change_log entry was created. Whatever its origin, an entry is a forward row: it
// records the change that ran and carries what a later reversal needs to invert it. The CHECK constraint on the
// column enforces this set.
const (
	ChangeOriginChange = "change" // Direct user write via execute_data or execute_schema.
	ChangeOriginUndo   = "undo"   // Written by undo_last_change: the inverse it ran.
	ChangeOriginRedo   = "redo"   // Written by redo_last_change: the change it re-applied.
	ChangeOriginRevert = "revert" // Written by revert_change: the inverse it ran.
)

// ChangePendingBatchID is the placeholder for batch_id inserted by DML
// triggers; the service layer rewrites it to the real batch UUID at
// end-of-transaction.
const ChangePendingBatchID = "__pending__"

// ChangePendingUser is the placeholder for changed_by inserted by DML
// triggers; the service layer rewrites it to the real user ID at
// end-of-transaction.
const ChangePendingUser = "__pending__"

// ChangeLogRetentionDays defines how many days of _change_log entries to
// keep. Entries older than this are deleted on each write; cascade-deletes
// the corresponding _change_snapshots rows via FK.
const ChangeLogRetentionDays = 7

// DefaultSnapshotMaxRows caps the number of rows captured per destructive
// DDL pre-image (DROP COLUMN values, DROP TABLE rows). Beyond this cap the
// snapshot is marked truncated and undo restores only the captured rows
// (the rest get NULL on column re-add). Resolved per-request via
// the quota resolver's SnapshotMaxRows.
const DefaultSnapshotMaxRows = 10000

// Platform-managed table names for the undo/redo stack pair. The stacks live alongside _change_log and are owned by
// the same migration set; defining them here keeps the unified change-log model self-contained.
const (
	UndoStackTable = "_undo_stack"
	RedoStackTable = "_redo_stack"
)

// DefaultUndoStackMaxSize caps the per-user undo / redo stack depth.
const DefaultUndoStackMaxSize = 50

// DefaultAffectedRowsMax caps the affected-row sample returned in a write response.
const DefaultAffectedRowsMax = 100

// ChangeLogEntry represents a row from the _change_log table. Kind
// discriminates which subset of fields is populated:
//   - Kind == ChangeKindDML: TableName, RowID, OldValues, NewValues set;
//     DDL fields (ObjectType, ObjectName, DDLSubtype, DDLStatement) zero.
//   - Kind == ChangeKindDDL: DDL fields set; DML fields zero.
//
// Origin signals provenance (direct change vs. undo/redo/revert reversal).
type ChangeLogEntry struct {
	ID        int64
	BatchID   string
	Kind      ChangeKind
	Operation string
	Origin    string
	ChangedBy string
	ChangedAt time.Time

	// DML fields — populated when Kind == ChangeKindDML.
	TableName string
	RowID     string
	OldValues map[string]any // parsed from JSON, nil for INSERT
	NewValues map[string]any // parsed from JSON, nil for DELETE

	// DDL fields — populated when Kind == ChangeKindDDL.
	ObjectType   string
	ObjectName   string
	DDLSubtype   string
	DDLStatement string
	DDLMetadata  *DDLAlterMetadata // populated for ALTER TABLE subtypes only; nil otherwise
}

// IsDML returns true if this entry records a DML row touch.
func (e *ChangeLogEntry) IsDML() bool { return e.Kind.IsDML() }

// IsDDL returns true if this entry records a DDL statement.
func (e *ChangeLogEntry) IsDDL() bool { return e.Kind.IsDDL() }

// ChangeBatch groups _change_log entries created by a single execute_data or execute_schema request
// (one shared batch_id).
// Kind is the newest entry's: a request is all-DML or all-DDL, except that a DDL statement whose foreign-key actions
// or user triggers wrote rows carries those DML entries alongside its own.
type ChangeBatch struct {
	BatchID   string
	Kind      ChangeKind
	Origin    string
	ChangedBy string
	ChangedAt time.Time
	Entries   []ChangeLogEntry
}

// KindRuns yields the maximal runs of consecutive entries sharing a Kind, in the order given, so a batch mixing DDL
// and DML — a DROP TABLE whose foreign-key actions deleted child rows — reverses run by run, newest first. Stops
// early when the consumer does.
func KindRuns(entries []*ChangeLogEntry) iter.Seq[[]*ChangeLogEntry] {
	return func(yield func([]*ChangeLogEntry) bool) {
		if len(entries) == 0 {
			return
		}

		lastRun := 0
		for i, e := range entries {
			if e.Kind != entries[lastRun].Kind {
				if !yield(entries[lastRun:i]) {
					return
				}
				lastRun = i
			}
		}
		yield(entries[lastRun:])
	}
}

// ListChangesResult holds the response for listing recent changes.
type ListChangesResult struct {
	Batches []ChangeBatch
}

// DependentObjectKind classifies a captured dependent of a DROP TABLE.
type DependentObjectKind string

// DependentObjectKind values.
const (
	DependentKindTable        DependentObjectKind = "TABLE"
	DependentKindVirtualTable DependentObjectKind = "VIRTUAL_TABLE"
	DependentKindIndex        DependentObjectKind = "INDEX"
	DependentKindTrigger      DependentObjectKind = "TRIGGER"
	DependentKindView         DependentObjectKind = "VIEW"
	DependentKindSkillTarget  DependentObjectKind = "SKILL_TARGET"
)

// ColumnSnapshot captures the metadata needed to recreate a single column
// (used by DROP COLUMN reversal). Type/NotNull/DfltValue come from
// pragma_table_info; Position is the column's index in the pre-DROP table
// (0-based, matching pragma_table_info.cid); ClauseSQL preserves the full
// column clause text from the table's CREATE TABLE statement so
// CHECK/UNIQUE/COLLATE/GENERATED constraints survive the rebuild path.
type ColumnSnapshot struct {
	Name    string
	Type    string
	NotNull bool
	// DfltValue is the default-value clause (name mirrors SQLite's
	// pragma_table_info.dflt_value); nil when the column has no DEFAULT.
	DfltValue  *string
	PrimaryKey bool
	Position   int    // 0-based column index in the pre-DROP table (pragma_table_info.cid)
	ClauseSQL  string // full column-definition text from sqlite_master.sql
}

// DependentObject captures one schema object or row payload that needs to
// be replayed when undoing a destructive DDL change. The SQL field's
// semantics depend on Kind:
//
//   - DependentKindTable:        body of the CREATE TABLE statement —
//     everything after `CREATE TABLE <name>`
//     (column list, constraints, options).
//     Replayed as fmt.Sprintf("CREATE TABLE %s %s", quoted-name, SQL).
//     Columns holds the column-name list in
//     declaration order; used by DROP COLUMN
//     undo to build the explicit-column populate
//     INSERT with the captured column at its
//     original position.
//   - DependentKindVirtualTable: full CREATE VIRTUAL TABLE statement text from sqlite_master. Replayed verbatim. Row
//     payload is not captured for virtual tables — undo restores an empty vtable; the index is rebuilt by the user's
//     sync triggers writing against the source table, or via the FTS5 'rebuild' command. Columns is left empty.
//   - DependentKindIndex/View/Trigger:
//     full CREATE statement text from sqlite_master.
//     Replayed verbatim.
//   - DependentKindSkillTarget:  JSON payload {"skill_id":..., "table_name":...}.
//     Parsed and re-INSERTed by the revert helper.
type DependentObject struct {
	Kind    DependentObjectKind
	Name    string
	SQL     string
	Columns []string // populated for DependentKindTable only; column names in declaration order
}

// SnapshotRow is one captured row used by destructive-DDL undo. For
// DROP COLUMN, Values has a single key (the dropped column's name). For
// DROP TABLE, Values has all of the row's columns.
type SnapshotRow struct {
	RowID  int64
	Values map[string]any
}

// ChangeSnapshot holds the pre-image data for a destructive DDL change.
// Empty for DML entries and non-destructive DDL — only DROP COLUMN /
// DROP TABLE / DROP INDEX / DROP VIEW / DROP TRIGGER populate it.
//
// Field population by subtype:
//   - DROP COLUMN: ColumnMetadata + RowPayload (RowID, Values{colName: value})
//   - DROP TABLE: DependentObjects (table SQL + indexes + triggers + views + skill targets) + RowPayload (full rows)
//   - DROP INDEX/VIEW/TRIGGER: DependentObjects (single entry with the CREATE text)
type ChangeSnapshot struct {
	ChangeLogID      int64
	ColumnMetadata   *ColumnSnapshot
	DependentObjects []DependentObject
	RowPayload       []SnapshotRow
	RowCountTotal    int  // true source row count before any truncation
	Truncated        bool // true if RowPayload was capped below RowCountTotal
}
