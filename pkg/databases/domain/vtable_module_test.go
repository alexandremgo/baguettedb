package domain

import "testing"

func TestModuleFromCreateSQL(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want string
	}{
		{"fts5", "CREATE VIRTUAL TABLE notes_fts USING fts5(title, body)", "fts5"},
		{"mixed case with if-not-exists", "create virtual table if not exists idx using FTS5(body)", "fts5"},
		{"fts5vocab", "CREATE VIRTUAL TABLE notes_vocab USING fts5vocab('notes_fts', 'row')", "fts5vocab"},
		{"external content fts5", "CREATE VIRTUAL TABLE notes_fts USING fts5(title, content='notes')", "fts5"},
		{"no using clause", "CREATE TABLE notes (id INTEGER PRIMARY KEY)", ""},
		{"empty sql", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ModuleFromCreateSQL(tt.sql); got != tt.want {
				t.Errorf("ModuleFromCreateSQL(%q) = %q, want %q", tt.sql, got, tt.want)
			}
		})
	}
}
