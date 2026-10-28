// Package migrations embeds the bdbs app's SQL migrations for the Postgres instance store; those for the SQLite
// instance store live in its sqlite subpackage.
package migrations

import "embed"

// FS contains the embedded SQL migration files for the bdbs module.
//
//go:embed *.sql
var FS embed.FS
