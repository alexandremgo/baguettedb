package domain

import (
	"regexp"
	"strings"
)

// vtableUsingPattern extracts the module name from a `CREATE VIRTUAL TABLE` statement. SQLite's grammar is
// `CREATE VIRTUAL TABLE [IF NOT EXISTS] [schema.]name USING module-name [(module-args)]`. The regex tolerates
// optional schema prefix, whitespace, and any quoting style SQLite emits in sqlite_master.sql.
var vtableUsingPattern = regexp.MustCompile(`(?i)\bUSING\s+(\w+)`)

// ModuleFromCreateSQL extracts the lowercased virtual-table module name (e.g. "fts5") from a
// `CREATE VIRTUAL TABLE` statement, or "" when none is found. Callers pass SQL already known to define a
// virtual table (e.g. classified by `pragma_table_list`); a wrong module can only mislabel the cosmetic
// module hint, never the table's kind.
func ModuleFromCreateSQL(createSQL string) string {
	if m := vtableUsingPattern.FindStringSubmatch(createSQL); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}
