// Package sqlite embeds the bdbs app's migrations for the SQLite instance store.
package sqlite

import "embed"

// FS contains the embedded SQL migration files for the bdbs app's SQLite store.
//
//go:embed *.sql
var FS embed.FS
