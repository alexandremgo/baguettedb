package sqlitelocal

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// classifierConn opens a fresh Conn with a few user tables seeded so
// the schema-aware classifier methods have something to classify
// against.
func classifierConn(t *testing.T) *Conn {
	t.Helper()
	c, err := Open(tmpDB(t), newKey(t), 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := c.WithInternal(func() error {
		stmts := []string{
			"CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, cat_id INTEGER)",
			"CREATE TABLE categories (id INTEGER PRIMARY KEY, name TEXT)",
			"CREATE TABLE temp_items (name TEXT)",
			"CREATE TABLE archive (name TEXT)",
			"CREATE TABLE recipes (id INTEGER PRIMARY KEY, name TEXT, tags TEXT)",
			"CREATE INDEX idx_name ON items (name)",
			"CREATE TRIGGER trg AFTER INSERT ON items BEGIN SELECT 1; END",
			"CREATE VIEW active_items AS SELECT * FROM items",
		}
		for _, s := range stmts {
			if _, e := c.Exec(context.Background(), s); e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		tClose(t, "conn", c)
		t.Fatalf("seed: %v", err)
	}
	return c
}

func TestClassify_DDL_Kinds(t *testing.T) {
	c := classifierConn(t)
	defer tClose(t, "conn", c)

	tests := []struct {
		name string
		sql  string
		want domain.DDLType
	}{
		{"create table", "CREATE TABLE foo (id INTEGER)", domain.DDLCreateTable},
		{"drop table", "DROP TABLE items", domain.DDLDropTable},
		{"alter table", "ALTER TABLE items ADD COLUMN x TEXT", domain.DDLAlterTable},
		{"create index", "CREATE INDEX idx2 ON items (cat_id)", domain.DDLCreateIndex},
		{"drop index", "DROP INDEX idx_name", domain.DDLDropIndex},
		{"create trigger", "CREATE TRIGGER trg2 AFTER INSERT ON items BEGIN SELECT 1; END", domain.DDLCreateTrigger},
		{"drop trigger", "DROP TRIGGER trg", domain.DDLDropTrigger},
		{"create view", "CREATE VIEW v AS SELECT id FROM items", domain.DDLCreateView},
		{"drop view", "DROP VIEW active_items", domain.DDLDropView},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewClassifier().Classify(c, tt.sql)
			if err != nil {
				t.Fatalf("Classify(%q): %v", tt.sql, err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d statements, want 1", len(got))
			}
			if got[0].DDLType != tt.want {
				t.Errorf("DDLType = %v, want %v", got[0].DDLType, tt.want)
			}
			if got[0].Type != domain.StatementDDL {
				t.Errorf("Type = %v, want StatementDDL", got[0].Type)
			}
		})
	}
}

func TestClassify_ResultColumns(t *testing.T) {
	c := classifierConn(t)
	defer tClose(t, "conn", c)

	tests := []struct {
		name string
		sql  string
		want []string
	}{
		{
			// A bare rowid on an INTEGER PRIMARY KEY table would come back named after the PK
			// column, hence the `rowid AS rowid` aliasing convention.
			name: "select with aliases",
			sql:  "SELECT rowid AS rowid, name, cat_id AS category FROM items",
			want: []string{"rowid", "name", "category"},
		},
		{
			name: "insert returns no columns",
			sql:  "INSERT INTO items (name) VALUES ('x')",
			want: nil,
		},
		{
			name: "ddl returns no columns",
			sql:  "CREATE TABLE foo (id INTEGER)",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewClassifier().Classify(c, tt.sql)
			if err != nil {
				t.Fatalf("Classify(%q): %v", tt.sql, err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d statements, want 1", len(got))
			}
			if !slices.Equal(got[0].ResultColumns, tt.want) {
				t.Errorf("ResultColumns = %v, want %v", got[0].ResultColumns, tt.want)
			}
		})
	}
}

func TestClassify_TablesRecorded(t *testing.T) {
	c := classifierConn(t)
	defer tClose(t, "conn", c)

	tests := []struct {
		name       string
		sql        string
		wantTables []string
	}{
		{"insert values", "INSERT INTO items (name) VALUES ('milk')", []string{"items"}},
		{"update", "UPDATE items SET name = 'bread' WHERE id = 1", []string{"items"}},
		{"delete", "DELETE FROM items WHERE id = 1", []string{"items"}},
		{"insert from select", "INSERT INTO items (name) SELECT name FROM temp_items", []string{"items", "temp_items"}},
		{"insert from select with join",
			"INSERT INTO items (name) SELECT t.name FROM temp_items t JOIN categories c ON t.name = c.name",
			[]string{"items", "temp_items", "categories"}},
		{"select", "SELECT * FROM items", []string{"items"}},
		{"select literal", "SELECT 1", nil},
		{"select join", "SELECT * FROM items JOIN categories ON items.cat_id = categories.id",
			[]string{"items", "categories"}},
		{"select subquery in where", "DELETE FROM items WHERE cat_id IN (SELECT id FROM categories)",
			[]string{"items", "categories"}},
		{"create table", "CREATE TABLE foo (id INTEGER PRIMARY KEY)", []string{"foo"}},
		{"drop table", "DROP TABLE items", []string{"items"}},
		{"alter table add column", "ALTER TABLE items ADD COLUMN extra TEXT", []string{"items"}},
		{"alter table rename", "ALTER TABLE items RENAME TO products", []string{"items"}},
		{"create index", "CREATE INDEX idx_cat ON items (cat_id)", []string{"items"}},
		{"drop index", "DROP INDEX idx_name", []string{"idx_name"}},
		{"create trigger", "CREATE TRIGGER trg2 AFTER INSERT ON items BEGIN SELECT 1; END", []string{"items"}},
		{"drop trigger", "DROP TRIGGER trg", []string{"trg"}},
		{"create view", "CREATE VIEW v AS SELECT name FROM items WHERE cat_id = 1", []string{"v"}},
		{"drop view", "DROP VIEW active_items", []string{"active_items"}},
		{"union", "SELECT name FROM items UNION SELECT name FROM archive", []string{"items", "archive"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewClassifier().Classify(c, tt.sql)
			if err != nil {
				t.Fatalf("Classify(%q) unexpected error: %v", tt.sql, err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d statements, want 1", len(got))
			}
			gotSorted := slices.Clone(got[0].Tables)
			slices.Sort(gotSorted)
			wantSorted := slices.Clone(tt.wantTables)
			slices.Sort(wantSorted)
			if !slices.Equal(gotSorted, wantSorted) {
				t.Errorf("Tables = %v, want %v", got[0].Tables, tt.wantTables)
			}
		})
	}
}

func TestClassify_TwoInserts_ReturnsTwo(t *testing.T) {
	c := classifierConn(t)
	defer tClose(t, "conn", c)

	got, err := NewClassifier().Classify(c,
		"INSERT INTO items (name) VALUES ('a'); INSERT INTO items (name) VALUES ('b');")
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d statements, want 2", len(got))
	}
	for i, s := range got {
		if s.Type != domain.StatementDML {
			t.Errorf("stmt %d: type = %v, want StatementDML", i, s.Type)
		}
		if !slices.Equal(s.Tables, []string{"items"}) {
			t.Errorf("stmt %d: tables = %v, want [items]", i, s.Tables)
		}
	}
}

func TestClassify_InsertThenSelect_ReturnsBoth(t *testing.T) {
	c := classifierConn(t)
	defer tClose(t, "conn", c)

	got, err := NewClassifier().Classify(c,
		"INSERT INTO items (name) VALUES ('a'); SELECT * FROM items")
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d statements, want 2", len(got))
	}
	if got[0].Type != domain.StatementDML {
		t.Errorf("stmt 0: type = %v, want StatementDML", got[0].Type)
	}
	if got[1].Type != domain.StatementDQL {
		t.Errorf("stmt 1: type = %v, want StatementDQL", got[1].Type)
	}
}

func TestClassify_TrailingWhitespace_Ignored(t *testing.T) {
	c := classifierConn(t)
	defer tClose(t, "conn", c)

	got, err := NewClassifier().Classify(c,
		"INSERT INTO items (name) VALUES ('a');   \n  ")
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d statements, want 1", len(got))
	}
	if got[0].Type != domain.StatementDML {
		t.Errorf("type = %v, want StatementDML", got[0].Type)
	}
}

func TestClassify_TrailingComment_Ignored(t *testing.T) {
	c := classifierConn(t)
	defer tClose(t, "conn", c)

	got, err := NewClassifier().Classify(c,
		"INSERT INTO items (name) VALUES ('a');\n-- trailing comment\n")
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d statements, want 1", len(got))
	}
}

func TestClassify_CommentsOnly_Rejected(t *testing.T) {
	c := classifierConn(t)
	defer tClose(t, "conn", c)

	_, err := NewClassifier().Classify(c, "-- nothing here\n/* nor here */")
	if err == nil {
		t.Fatal("expected error for comments-only input")
	}
	if !errors.Is(err, domain.ErrUnsupportedStatement) {
		t.Errorf("got %v, want wrapping ErrUnsupportedStatement", err)
	}
	if !strings.Contains(err.Error(), "empty SQL") {
		t.Errorf("error should mention empty SQL, got: %v", err)
	}
}

func TestClassify_Empty_Rejected(t *testing.T) {
	c := classifierConn(t)
	defer tClose(t, "conn", c)

	_, err := NewClassifier().Classify(c, "   \n  \t")
	if err == nil {
		t.Fatal("expected error for whitespace-only input")
	}
	if !errors.Is(err, domain.ErrUnsupportedStatement) {
		t.Errorf("got %v, want wrapping ErrUnsupportedStatement", err)
	}
}

func TestClassify_SyntaxErrorMidWalk_ReportsIndex(t *testing.T) {
	c := classifierConn(t)
	defer tClose(t, "conn", c)

	_, err := NewClassifier().Classify(c,
		"INSERT INTO items (name) VALUES ('a'); INSERT BROKEN SYNTAX")
	if err == nil {
		t.Fatal("expected error for syntax error in second statement")
	}
	if !strings.Contains(err.Error(), "statement 2") {
		t.Errorf("error should mention 'statement 2', got: %v", err)
	}
}

func TestClassify_CreateTriggerWithBeginEnd_OneStatement(t *testing.T) {
	c := classifierConn(t)
	defer tClose(t, "conn", c)

	got, err := NewClassifier().Classify(c,
		"CREATE TRIGGER trg_walk AFTER INSERT ON items BEGIN SELECT 1; SELECT 2; END")
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	// CREATE TRIGGER's BEGIN…END body contains internal `;`, but SQLite's
	// parser treats the whole CREATE TRIGGER as one statement.
	if len(got) != 1 {
		t.Fatalf("got %d statements, want 1 (CREATE TRIGGER must not split on inner ;)", len(got))
	}
	if got[0].Type != domain.StatementDDL {
		t.Errorf("type = %v, want StatementDDL", got[0].Type)
	}
	if got[0].DDLType != domain.DDLCreateTrigger {
		t.Errorf("ddl type = %v, want DDLCreateTrigger", got[0].DDLType)
	}
}

func TestClassify_DDL_RecordsKindAndTables(t *testing.T) {
	c := classifierConn(t)
	defer tClose(t, "conn", c)

	got, err := NewClassifier().Classify(c,
		"CREATE TABLE foo_walk (id INTEGER); ALTER TABLE items ADD COLUMN extra TEXT")
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d statements, want 2", len(got))
	}
	if got[0].Type != domain.StatementDDL || got[0].DDLType != domain.DDLCreateTable {
		t.Errorf("stmt 0: type=%v ddl=%v, want DDL/CreateTable", got[0].Type, got[0].DDLType)
	}
	if !slices.Equal(got[0].Tables, []string{"foo_walk"}) {
		t.Errorf("stmt 0: tables=%v, want [foo_walk]", got[0].Tables)
	}
	if got[1].DDLType != domain.DDLAlterTable {
		t.Errorf("stmt 1: ddl=%v, want DDLAlterTable", got[1].DDLType)
	}
	if !slices.Equal(got[1].Tables, []string{"items"}) {
		t.Errorf("stmt 1: tables=%v, want [items]", got[1].Tables)
	}
}

func TestClassify_SingleStatement_Works(t *testing.T) {
	c := classifierConn(t)
	defer tClose(t, "conn", c)

	got, err := NewClassifier().Classify(c, "SELECT 1")
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d statements, want 1", len(got))
	}
	if got[0].Type != domain.StatementDQL {
		t.Errorf("type = %v, want StatementDQL", got[0].Type)
	}
}

func TestClassify_TenantBegin_RejectedWithClearMessage(t *testing.T) {
	c := classifierConn(t)
	defer tClose(t, "conn", c)

	_, err := NewClassifier().Classify(c,
		"BEGIN; INSERT INTO items (name) VALUES ('a'); COMMIT")
	if err == nil {
		t.Fatal("expected error for tenant BEGIN, got nil")
	}
	if !errors.Is(err, domain.ErrUnsupportedStatement) {
		t.Errorf("got %v, want wrapping ErrUnsupportedStatement", err)
	}
	if !strings.Contains(err.Error(), "transaction control") {
		t.Errorf("error should mention transaction control, got: %v", err)
	}
	if !strings.Contains(err.Error(), "statement 1") {
		t.Errorf("error should mention 'statement 1', got: %v", err)
	}
}

func TestClassify_Reindex_RejectedAsUnsupported(t *testing.T) {
	c := classifierConn(t)
	defer tClose(t, "conn", c)

	// REINDEX is neither SELECT nor INSERT/UPDATE/DELETE nor DDL,
	// so the strict classifier must reject it.
	_, err := NewClassifier().Classify(c, "REINDEX items")
	if err == nil {
		t.Fatal("expected error for REINDEX, got nil")
	}
	if !errors.Is(err, domain.ErrUnsupportedStatement) {
		t.Errorf("got %v, want wrapping ErrUnsupportedStatement", err)
	}
}

func TestClassify_ReservedPrefixDeniedAtStmtIndex(t *testing.T) {
	c := classifierConn(t)
	defer tClose(t, "conn", c)

	// Seeds a reserved-prefix table so the authorizer fires for the
	// INSERT (without it, SQLite errors with "no such table" before
	// the authorizer is consulted).
	if err := c.WithInternal(func() error {
		_, e := c.Exec(context.Background(), "CREATE TABLE _platform_audit (id INTEGER, msg TEXT)")
		return e
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := NewClassifier().Classify(c,
		"INSERT INTO items (name) VALUES ('a'); INSERT INTO _platform_audit (id, msg) VALUES (1, 'x')")
	if err == nil {
		t.Fatal("expected error for reserved-prefix write in second statement")
	}
	if !errors.Is(err, domain.ErrReservedTable) {
		t.Errorf("got %v, want wrapping ErrReservedTable", err)
	}
	if !strings.Contains(err.Error(), "statement 2") {
		t.Errorf("error should mention 'statement 2', got: %v", err)
	}
}

func TestClassifyAlterTable(t *testing.T) {
	c := classifierConn(t)
	defer tClose(t, "conn", c)

	tests := []struct {
		name     string
		sql      string
		wantInfo domain.AlterTableInfo
		wantErr  bool
	}{
		{
			name: "add column",
			sql:  "ALTER TABLE items ADD COLUMN category TEXT",
			wantInfo: domain.AlterTableInfo{Subtype: domain.AlterTableAddColumn, TableName: "items",
				ColumnName: "category"},
		},
		{
			name: "add column without COLUMN keyword",
			sql:  "ALTER TABLE items ADD email TEXT NOT NULL",
			wantInfo: domain.AlterTableInfo{Subtype: domain.AlterTableAddColumn, TableName: "items",
				ColumnName: "email"},
		},
		{
			// A quoted column name must be extracted verbatim — not misparsed as the COLUMN keyword.
			name: "add column with quoted name",
			sql:  `ALTER TABLE items ADD COLUMN "unit (kg)" REAL`,
			wantInfo: domain.AlterTableInfo{
				Subtype: domain.AlterTableAddColumn, TableName: "items", ColumnName: "unit (kg)",
			},
		},
		{
			name: "drop column",
			sql:  "ALTER TABLE recipes DROP COLUMN tags",
			wantInfo: domain.AlterTableInfo{Subtype: domain.AlterTableDropColumn, TableName: "recipes",
				ColumnName: "tags"},
		},
		{
			name: "rename column",
			sql:  "ALTER TABLE recipes RENAME COLUMN name TO title",
			wantInfo: domain.AlterTableInfo{Subtype: domain.AlterTableRenameColumn, TableName: "recipes",
				ColumnName: "name", NewColumnName: "title"},
		},
		{
			name: "rename to",
			sql:  "ALTER TABLE recipes RENAME TO meals",
			wantInfo: domain.AlterTableInfo{Subtype: domain.AlterTableRenameTo, TableName: "recipes",
				NewTableName: "meals"},
		},
		{
			name:    "not an alter table",
			sql:     "CREATE TABLE foo (id INTEGER)",
			wantErr: true,
		},
		{
			name:    "garbage",
			sql:     "NOT VALID SQL",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewClassifier().ClassifyAlterTable(c, tt.sql)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ClassifyAlterTable(%q) expected error, got nil", tt.sql)
				}
				return
			}
			if err != nil {
				t.Fatalf("ClassifyAlterTable(%q) unexpected error: %v", tt.sql, err)
			}
			if got != tt.wantInfo {
				t.Errorf("ClassifyAlterTable(%q) = %+v, want %+v", tt.sql, got, tt.wantInfo)
			}
		})
	}
}

// TestClassify_CreatedObjectsRecorded verifies that Classify surfaces the created object's own name for
// every CREATE shape — including quoted names, recorded verbatim — and records nothing for DROP, ALTER,
// and DML statements.
func TestClassify_CreatedObjectsRecorded(t *testing.T) {
	c := classifierConn(t)
	defer tClose(t, "conn", c)

	tests := []struct {
		name string
		sql  string
		want []string
	}{
		{"create table", "CREATE TABLE foo (id INTEGER)", []string{"foo"}},
		{"create index", "CREATE INDEX idx2 ON items (cat_id)", []string{"idx2"}},
		{"create trigger", "CREATE TRIGGER trg2 AFTER INSERT ON items BEGIN SELECT 1; END", []string{"trg2"}},
		{"create view", "CREATE VIEW v AS SELECT id FROM items", []string{"v"}},
		{"create virtual table", "CREATE VIRTUAL TABLE ft USING fts5(content)", []string{"ft"}},
		{"quoted name with space", `CREATE TABLE "my table" (id INTEGER)`, []string{"my table"}},
		{"drop table", "DROP TABLE items", nil},
		{"alter add column", "ALTER TABLE items ADD COLUMN x TEXT", nil},
		{"select", "SELECT * FROM items", nil},
		{"insert", "INSERT INTO items (name) VALUES ('a')", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewClassifier().Classify(c, tt.sql)
			if err != nil {
				t.Fatalf("Classify(%q): %v", tt.sql, err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d statements, want 1", len(got))
			}
			if !slices.Equal(got[0].CreatedObjects, tt.want) {
				t.Errorf("CreatedObjects = %v, want %v", got[0].CreatedObjects, tt.want)
			}
		})
	}
}

// TestClassify_CreatedObjectsPerStatement verifies that multi-statement input attributes each created
// name to its own statement, not to the request as a whole.
func TestClassify_CreatedObjectsPerStatement(t *testing.T) {
	c := classifierConn(t)
	defer tClose(t, "conn", c)

	got, err := NewClassifier().Classify(c,
		"CREATE TABLE first_t (id INTEGER); CREATE INDEX second_i ON items (name)")
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d statements, want 2", len(got))
	}
	if !slices.Equal(got[0].CreatedObjects, []string{"first_t"}) {
		t.Errorf("stmt 0: CreatedObjects = %v, want [first_t]", got[0].CreatedObjects)
	}
	if !slices.Equal(got[1].CreatedObjects, []string{"second_i"}) {
		t.Errorf("stmt 1: CreatedObjects = %v, want [second_i]", got[1].CreatedObjects)
	}
}

// TestClassify_TempObject_RejectedAsUnsupported checks that a tenant CREATE TEMP statement surfaces as an unsupported
// statement rather than a bare authorization error.
func TestClassify_TempObject_RejectedAsUnsupported(t *testing.T) {
	c := classifierConn(t)
	defer tClose(t, "conn", c)

	_, err := NewClassifier().Classify(c, "CREATE TEMP TABLE scratch (x INTEGER)")
	if err == nil {
		t.Fatal("expected error for CREATE TEMP TABLE, got nil")
	}
	if !errors.Is(err, domain.ErrUnsupportedStatement) {
		t.Errorf("got %v, want wrapping ErrUnsupportedStatement", err)
	}
}

// TestTranslatePrepareError_KeepsTheCause checks that an authorizer denial becomes the domain error for its reason
// while the SQLite error stays in the chain, so a caller can still read its code.
func TestTranslatePrepareError_KeepsTheCause(t *testing.T) {
	tests := []struct {
		name   string
		reason domain.DenyReason
		want   error
	}{
		{"reserved prefix", domain.DenyReservedPrefix, domain.ErrReservedTable},
		{"pragma not allowed", domain.DenyPragmaNotAllowed, domain.ErrUnsupportedStatement},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			denial := &Error{Code: CodeAuth, Msg: "not authorized", Op: "prepare"}
			got := translatePrepareError(denial, tt.reason)
			if !errors.Is(got, tt.want) {
				t.Errorf("translatePrepareError() = %v, want it to match %v", got, tt.want)
			}
			if sqlErr, ok := errors.AsType[*Error](got); !ok || sqlErr != denial {
				t.Errorf("translatePrepareError() = %v, want the SQLite error %v reachable in its chain", got, denial)
			}
		})
	}
}
