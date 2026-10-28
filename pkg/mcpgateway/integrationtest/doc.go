//go:build integration

// Package integrationtest holds the MCP gateway's integration tests, built with the integration tag. They put the real
// bdbs and databases MCP servers behind the gateway, or compose a whole instance, on the instance store
// BAGUETTEDB_TEST_STORE selects (Postgres by default, or SQLite), and drive the tools with an MCP client over
// Streamable HTTP or stdio, authenticated by test-signed JWTs, personal access tokens or the implicit local owner.
package integrationtest
