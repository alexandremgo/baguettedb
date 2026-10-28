package apptypes

// TableInfo is the lightweight table listing entry. Module is the SQLite virtual-table module name (e.g.
// "fts5") when Type == "virtual", and empty otherwise.
type TableInfo struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Module string `json:"module,omitempty"`
}

// TableColumn describes a single column in a table.
type TableColumn struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	NotNull    bool   `json:"not_null"`
	Default    string `json:"default,omitempty"`
	PrimaryKey bool   `json:"primary_key"`
}

// ForeignKey describes a foreign key constraint.
type ForeignKey struct {
	From     string `json:"from"`
	Table    string `json:"table"`
	To       string `json:"to"`
	OnUpdate string `json:"on_update"`
	OnDelete string `json:"on_delete"`
}

// TableDescription holds the full schema of a single table. Kind is "table", "view", "virtual", or "shadow" (a
// virtual table's internal storage table); Module is the SQLite virtual-table module name (e.g. "fts5") when
// Kind == "virtual". Checks holds the table's CHECK constraint expressions in declaration order, and is empty
// for views and virtual tables.
type TableDescription struct {
	Columns     []TableColumn `json:"columns"`
	ForeignKeys []ForeignKey  `json:"foreign_keys"`
	Kind        string        `json:"kind,omitempty"`
	Module      string        `json:"module,omitempty"`
	Checks      []string      `json:"checks,omitempty"`
}

// TableSchema pairs a table's name and type with its full description. Module is the SQLite virtual-table
// module name when Type == "virtual". DescribeError marks a degraded entry whose schema introspection failed
// (Schema is then empty), so an unreadable table is distinguishable from one with no columns.
type TableSchema struct {
	Name          string           `json:"name"`
	Type          string           `json:"type"`
	Module        string           `json:"module,omitempty"`
	Schema        TableDescription `json:"schema"`
	DescribeError string           `json:"describe_error,omitempty"`
}

// Skill is a skill with its full body and the tables it targets (empty for a bdb-level skill).
type Skill struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Body         string   `json:"body"`
	TargetTables []string `json:"target_tables"`
}

// Formula is the lightweight orientation view of a formula: enough to list and route to it, without the SQL or
// parameter schema. Kind is "view" or "action". Label is the optional human name rendered instead of Name, and
// Hidden marks an assistant-only formula that member-facing listings omit.
type Formula struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description"`
	Kind        string `json:"kind"`
	Hidden      bool   `json:"hidden,omitempty"`
}
