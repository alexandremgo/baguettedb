//go:build integration

// Package integrationtest holds the databases app's integration tests: they call its REST, internal and MCP endpoints,
// and the per-bdb SQLite engine directly, against the instance store the suite selects (Postgres or SQLite), real
// encrypted bdb files, and an S3-compatible snapshot store in a container. It builds only under the integration tag.
package integrationtest
