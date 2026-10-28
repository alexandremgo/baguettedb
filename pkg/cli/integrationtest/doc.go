//go:build integration

// Package integrationtest holds the bdb command line's integration tests, built with the integration tag. They run
// cli.Run in-process on temporary data directories, checking what each command prints and its exit status, and drive
// bdb serve over HTTP and bdb mcp over its standard input, as a client would.
package integrationtest
