// Package migrations embeds the databases app's SQL migrations: those of its Postgres instance store, and under
// platform/ the forward-only migrations of the platform tables every bdb's SQLite file carries. The SQLite instance
// store's migrations live in the sqlite subpackage.
package migrations

import "embed"

// FS contains the embedded SQL migration files for the databases Postgres tables.
//
//go:embed *.sql
var FS embed.FS

// PlatformFS contains the embedded SQL migration files for the SQLite platform
// tables (_change_log, _change_snapshots, _undo_stack, _redo_stack, _skills, _skill_targets, _formulas).
//
//go:embed platform/*.sql
var PlatformFS embed.FS
