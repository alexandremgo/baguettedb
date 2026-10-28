package domain

// DDLUnit is one recorded step of a DDL request: the shape the change log records and the statements that perform it.
// For a user statement the two coincide. For a computed inverse, Shape describes the schema change as the statement
// a user would have written — a restored column is recorded as its ADD COLUMN — while Exec holds the work
// that actually performs it, which may be a table rebuild.
type DDLUnit struct {
	// Shape supplies DDLType, Tables[0] as the object name, and SQL as the recorded ddl_statement.
	// The SQL is informational: nothing parses it, so it may describe a column shape SQLite cannot ADD COLUMN directly.
	Shape ClassifiedStatement
	// Alter carries the parsed ALTER TABLE shape when Shape.DDLType is DDLAlterTable.
	// TableName is the name the table has when the unit runs, so a rename back is recorded against the live name.
	Alter AlterTableInfo
	// Exec runs in order in place of Shape.SQL, one statement per entry.
	Exec []SQLStatement
}

// UserDDLUnit wraps a classified user statement as a unit that executes itself.
// The classifier files a CREATE INDEX or CREATE TRIGGER under the table it attaches to;
// the unit names the created object instead, since that is what the change log records and what a later DROP inverts.
func UserDDLUnit(stmt ClassifiedStatement, alter AlterTableInfo) DDLUnit {
	shape := stmt
	switch stmt.DDLType {
	case DDLCreateIndex, DDLCreateView, DDLCreateTrigger:
		if len(stmt.CreatedObjects) > 0 {
			shape.Tables = []string{stmt.CreatedObjects[0]}
		}
	}
	return DDLUnit{Shape: shape, Alter: alter, Exec: []SQLStatement{{SQL: stmt.SQL}}}
}
