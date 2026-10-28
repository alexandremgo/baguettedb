package domain

import (
	"reflect"
	"testing"
)

// TestCheckConstraintsFromCreateSQL pins the extractor against the scanner traps: column-level and
// named table-level constraints, declaration order, nested parens, a CHECK inside a string literal or
// a quoted identifier, escaped quotes keeping the scan aligned, and an unclosed paren degrading to the
// constraints completed before it.
func TestCheckConstraintsFromCreateSQL(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []string
	}{
		{
			name: "no checks",
			sql:  "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)",
			want: nil,
		},
		{
			name: "column-level check",
			sql:  "CREATE TABLE t (data TEXT CHECK (json_valid(data)))",
			want: []string{"json_valid(data)"},
		},
		{
			name: "named table-level check",
			sql:  "CREATE TABLE t (qty INTEGER, CONSTRAINT positive_qty CHECK (qty > 0))",
			want: []string{"qty > 0"},
		},
		{
			name: "multiple checks in declaration order",
			sql:  "CREATE TABLE t (data TEXT CHECK (json_valid(data)), qty INTEGER, CHECK (qty >= 0))",
			want: []string{"json_valid(data)", "qty >= 0"},
		},
		{
			name: "nested parens and lowercase keyword",
			sql:  "CREATE TABLE t (data TEXT check (json_valid(data) AND length(data) < 100))",
			want: []string{"json_valid(data) AND length(data) < 100"},
		},
		{
			name: "check inside a string literal is not a constraint",
			sql:  "CREATE TABLE t (kind TEXT DEFAULT 'CHECK (nope)', qty INTEGER CHECK (qty > 0))",
			want: []string{"qty > 0"},
		},
		{
			name: "quoted identifier named check is not a constraint",
			sql:  `CREATE TABLE t ("check" TEXT, qty INTEGER CHECK (qty > 0))`,
			want: []string{"qty > 0"},
		},
		{
			name: "unquoted column named check without parens is skipped",
			sql:  "CREATE TABLE t (check_state TEXT, qty INTEGER CHECK (qty > 0))",
			want: []string{"qty > 0"},
		},
		{
			name: "escaped quote inside a literal keeps the scan aligned",
			sql:  "CREATE TABLE t (label TEXT DEFAULT 'it''s', qty INTEGER CHECK (qty > 0))",
			want: []string{"qty > 0"},
		},
		{
			name: "paren inside a literal does not end the balancing",
			sql:  "CREATE TABLE t (kind TEXT CHECK (kind IN ('a)', 'b')))",
			want: []string{"kind IN ('a)', 'b')"},
		},
		{
			name: "unclosed paren returns what was completed",
			sql:  "CREATE TABLE t (qty INTEGER CHECK (qty > 0), data TEXT CHECK (json_valid(data)",
			want: []string{"qty > 0"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckConstraintsFromCreateSQL(tt.sql)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CheckConstraintsFromCreateSQL(%q) = %#v, want %#v", tt.sql, got, tt.want)
			}
		})
	}
}
