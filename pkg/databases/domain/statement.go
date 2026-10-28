package domain

// StatementType represents the category of a SQL statement.
type StatementType int

// The statement categories.
const (
	StatementDQL         StatementType = iota // SELECT
	StatementDML                              // INSERT, UPDATE, DELETE
	StatementDDL                              // CREATE, DROP, ALTER
	StatementUnsupported                      // ATTACH, DETACH, etc.
)

// String returns a human-readable name suitable for error messages.
func (t StatementType) String() string {
	switch t {
	case StatementDQL:
		return "SELECT"
	case StatementDML:
		return "DML"
	case StatementDDL:
		return "DDL"
	default:
		return "unsupported"
	}
}

// Description returns the statement kind as a phrase for messages, e.g. "a schema change".
func (t StatementType) Description() string {
	switch t {
	case StatementDQL:
		return "a read"
	case StatementDML:
		return "a row change"
	case StatementDDL:
		return "a schema change"
	default:
		return "an unsupported statement"
	}
}

// StatementVerb identifies what a statement does at the verb level: the read verb (select) or the
// distinct top-level write verbs the authorizer reported during prepare (insert / update / delete).
type StatementVerb string

// The statement verbs.
const (
	StatementVerbSelect StatementVerb = "select"
	StatementVerbInsert StatementVerb = "insert"
	StatementVerbUpdate StatementVerb = "update"
	StatementVerbDelete StatementVerb = "delete"
)

// DDLType represents the specific type of a DDL statement.
type DDLType int

// The DDL statement types.
const (
	DDLUnknown       DDLType = iota
	DDLCreateTable           // CREATE TABLE
	DDLDropTable             // DROP TABLE
	DDLAlterTable            // ALTER TABLE
	DDLCreateIndex           // CREATE INDEX
	DDLDropIndex             // DROP INDEX
	DDLCreateView            // CREATE VIEW
	DDLDropView              // DROP VIEW
	DDLCreateTrigger         // CREATE TRIGGER
	DDLDropTrigger           // DROP TRIGGER
)

// AlterTableSubtype represents the kind of ALTER TABLE operation.
type AlterTableSubtype int

// The ALTER TABLE operations.
const (
	AlterTableUnknown      AlterTableSubtype = iota
	AlterTableAddColumn                      // ALTER TABLE x ADD [COLUMN] <coldef>
	AlterTableDropColumn                     // ALTER TABLE x DROP COLUMN c
	AlterTableRenameColumn                   // ALTER TABLE x RENAME COLUMN a TO b
	AlterTableRenameTo                       // ALTER TABLE x RENAME TO y
)

// String returns the stable string label used as _change_log.ddl_subtype.
// Persistence-safe: callers can renumber the int constants without invalidating
// existing rows because storage round-trips through these names, not the int.
func (s AlterTableSubtype) String() string {
	switch s {
	case AlterTableAddColumn:
		return DDLSubtypeAddColumn
	case AlterTableDropColumn:
		return DDLSubtypeDropColumn
	case AlterTableRenameColumn:
		return DDLSubtypeRenameColumn
	case AlterTableRenameTo:
		return DDLSubtypeRenameTo
	default:
		return ""
	}
}

// ParseAlterTableSubtype returns the AlterTableSubtype for the given stored
// _change_log.ddl_subtype string. Returns AlterTableUnknown for empty or
// unrecognized inputs.
func ParseAlterTableSubtype(s string) AlterTableSubtype {
	switch s {
	case DDLSubtypeAddColumn:
		return AlterTableAddColumn
	case DDLSubtypeDropColumn:
		return AlterTableDropColumn
	case DDLSubtypeRenameColumn:
		return AlterTableRenameColumn
	case DDLSubtypeRenameTo:
		return AlterTableRenameTo
	default:
		return AlterTableUnknown
	}
}

// AlterTableInfo describes the parsed shape of an ALTER TABLE statement.
// Fields are populated based on Subtype:
//   - AddColumn:    TableName + ColumnName (the new column's name)
//   - DropColumn:   TableName + ColumnName (the dropped column's name)
//   - RenameColumn: TableName + ColumnName (old name) + NewColumnName (new name)
//   - RenameTo:     TableName (old) + NewTableName (new)
type AlterTableInfo struct {
	Subtype       AlterTableSubtype
	TableName     string
	NewTableName  string
	ColumnName    string
	NewColumnName string
}

// SQLStatement represents a SQL statement with optional arguments for transactional execution.
type SQLStatement struct {
	SQL  string
	Args []TypedValue
}

// ClassifiedStatement is one entry produced by the classifier when walking
// multi-statement input. Tables, DDLType, and DMLVerbs reflect what the SQLite
// authorizer recorded during prepare; DDLType is DDLUnknown and DMLVerbs empty
// for non-DDL / non-DML statements respectively.
type ClassifiedStatement struct {
	SQL      string
	Type     StatementType
	DDLType  DDLType
	Tables   []string
	DMLVerbs []StatementVerb

	// CreatedObjects lists the names of schema objects the statement creates (table, index, trigger,
	// view, or virtual table names on CREATE); empty for non-CREATE statements.
	CreatedObjects []string

	// ResultColumns lists the statement's result column names in positional order; empty for
	// statements that return no rows.
	ResultColumns []string

	// ParamNames lists the statement's parameter placeholders in binding order, without their sigil ("x" for ":x");
	// "" for a bare "?".
	ParamNames []string
}

// StatementsGrowStorage reports whether any statement in the batch can increase on-disk size. Pure
// deletions free space — DML whose verbs are only delete (and select, from subqueries), and DROP
// DDL — so a batch of those passes even over quota, letting an over-budget owner recover. Inserts,
// updates, non-DROP DDL, and any shape the classifier could not pin down count as growing.
func StatementsGrowStorage(stmts []ClassifiedStatement) bool {
	for _, cs := range stmts {
		switch cs.Type {
		case StatementDML:
			// No recorded verbs means the authorizer could not tell what this DML does — fail closed:
			// wrongly blocking a delete is visible and recoverable, wrongly admitting growth is not.
			if len(cs.DMLVerbs) == 0 {
				return true
			}
			for _, v := range cs.DMLVerbs {
				if v != StatementVerbDelete && v != StatementVerbSelect {
					return true
				}
			}
		case StatementDDL:
			switch cs.DDLType {
			case DDLDropTable, DDLDropIndex, DDLDropView, DDLDropTrigger:
			default:
				return true
			}
		}
	}
	return false
}

// OperationIntent represents the caller's intended operation mode.
type OperationIntent int

// The operation intents a caller declares.
const (
	IntentQuery   OperationIntent = iota // read-only queries (DQL)
	IntentExecute                        // data mutations (DML)
	IntentSchema                         // schema changes (DDL)
)

// Description returns the kinds of statement the intent runs, as a phrase for messages, e.g. "row changes".
func (i OperationIntent) Description() string {
	switch i {
	case IntentQuery:
		return "reads"
	case IntentExecute:
		return "row changes"
	case IntentSchema:
		return "schema changes"
	default:
		return "no statements"
	}
}
