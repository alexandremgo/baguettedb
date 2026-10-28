//go:build integration

// Package integrationtest holds the starters app's integration tests, built with the integration tag. They run on the
// instance store BAGUETTEDB_TEST_STORE selects (a Postgres container by default, or an SQLite file) with real
// encrypted bdb files, create bdbs from starters through the REST routes, the MCP tools and the service over both its
// in-process and HTTP adapters, and check every built-in starter, running its formulas to pin its behavior.
package integrationtest
