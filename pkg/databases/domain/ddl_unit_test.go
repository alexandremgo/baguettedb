package domain

import "testing"

// TestUserDDLUnit pins that a user statement becomes a unit that records itself and executes itself, keeping the
// parsed ALTER shape alongside.
func TestUserDDLUnit(t *testing.T) {
	stmt := ClassifiedStatement{
		SQL: `ALTER TABLE "items" ADD COLUMN "qty" INTEGER`, Type: StatementDDL, DDLType: DDLAlterTable,
		Tables: []string{"items"},
	}
	alter := AlterTableInfo{Subtype: AlterTableAddColumn, TableName: "items", ColumnName: "qty"}

	unit := UserDDLUnit(stmt, alter)

	if unit.Shape.SQL != stmt.SQL || unit.Shape.DDLType != DDLAlterTable || unit.Shape.Tables[0] != "items" {
		t.Errorf("Shape = %+v, want the classified statement itself", unit.Shape)
	}
	if unit.Alter != alter {
		t.Errorf("Alter = %+v, want %+v", unit.Alter, alter)
	}
	if len(unit.Exec) != 1 || unit.Exec[0].SQL != stmt.SQL || unit.Exec[0].Args != nil {
		t.Errorf("Exec = %+v, want the statement itself with no args", unit.Exec)
	}
}

// TestUserDDLUnit_NamesTheCreatedObject pins that a CREATE INDEX / TRIGGER unit targets the object it creates rather
// than the table the classifier filed it under, while a CREATE TABLE keeps the table it names.
func TestUserDDLUnit_NamesTheCreatedObject(t *testing.T) {
	for _, tc := range []struct {
		name string
		stmt ClassifiedStatement
		want string
	}{
		{"index", ClassifiedStatement{
			SQL: "CREATE INDEX idx ON items(name)", DDLType: DDLCreateIndex, Tables: []string{"items"},
			CreatedObjects: []string{"idx"},
		}, "idx"},
		{"trigger", ClassifiedStatement{
			SQL: "CREATE TRIGGER trg AFTER INSERT ON items BEGIN SELECT 1; END", DDLType: DDLCreateTrigger,
			Tables: []string{"items"}, CreatedObjects: []string{"trg"},
		}, "trg"},
		{"table", ClassifiedStatement{
			SQL: "CREATE TABLE items (id INTEGER)", DDLType: DDLCreateTable, Tables: []string{"items"},
			CreatedObjects: []string{"items"},
		}, "items"},
		{"index without a recorded created object", ClassifiedStatement{
			SQL: "CREATE INDEX idx ON items(name)", DDLType: DDLCreateIndex, Tables: []string{"idx"},
		}, "idx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unit := UserDDLUnit(tc.stmt, AlterTableInfo{})
			if len(unit.Shape.Tables) != 1 || unit.Shape.Tables[0] != tc.want {
				t.Errorf("Shape.Tables = %v, want [%s]", unit.Shape.Tables, tc.want)
			}
		})
	}
}
