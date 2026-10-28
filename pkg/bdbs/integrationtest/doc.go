//go:build integration

// Package integrationtest holds the bdbs app's integration tests, built with the integration tag. They run on the
// instance store BAGUETTEDB_TEST_STORE selects (a Postgres container by default, or an SQLite file) and exercise the
// REST routes, the internal endpoints and the MCP tools, driving the services directly for concurrency checks.
package integrationtest
