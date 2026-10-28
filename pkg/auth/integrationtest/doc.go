//go:build integration

// Package integrationtest holds the auth app's integration tests, built with the integration tag: its HTTP routes,
// services and repositories run together on a real instance store — a Postgres container, or an SQLite file when
// BAGUETTEDB_TEST_STORE=sqlite — with test-signed JWTs and a stub authorization server standing in for Hydra.
package integrationtest
