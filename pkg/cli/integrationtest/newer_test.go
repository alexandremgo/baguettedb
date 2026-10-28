//go:build integration

package integrationtest

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alexandremgo/baguettedb/pkg/common/database"
	dbservice "github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// TestRun_RefusesNewerBdb checks that a bdb a newer version of bdb upgraded is refused, by a one-shot command and over
// `bdb mcp`, each time with the message telling how to run the newer version from there, while a bdb it did not
// upgrade still answers.
func TestRun_RefusesNewerBdb(t *testing.T) {
	dataDir, placesID := newBdbWithPlaces(t)
	if code, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "create", "Notes"); code != exitOK {
		t.Fatalf("bdbs create Notes = %d, stderr %q", code, stderr)
	}
	execInstanceStore(t, dataDir, "UPDATE databases SET platform_schema_version = ? WHERE bdb_id = ?",
		dbservice.CurrentPlatformSchemaVersion+1, placesID)

	code, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "q", "Places", "SELECT 1")
	if want := "Error: in Places: this bdb was upgraded by a newer version of bdb: update this binary\n"; code !=
		exitFailure || stderr != want {
		t.Errorf("q Places = %d, stderr %q; want %d and %q", code, stderr, exitFailure, want)
	}
	if code, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "q", "Notes", "SELECT 1"); code != exitOK {
		t.Errorf("q Notes = %d, stderr %q; want %d", code, stderr, exitOK)
	}

	c, _, stop := startMCP(t, dataDir)
	defer stop()
	res, err := c.CallTool(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: "execute_query", Arguments: map[string]any{"bdb_id": placesID, "sql": "SELECT 1"},
	}})
	if err != nil {
		t.Fatalf("calling execute_query: %v", err)
	}
	want := "this bdb was upgraded by a newer version of bdb than the one this server runs. Restart this server to " +
		"use the new version: reconnect it in your MCP client (in Claude Code, `/mcp`), or restart the session that " +
		"started it"
	if text, ok := res.Content[0].(mcp.TextContent); !res.IsError || !ok || !strings.Contains(text.Text, want) {
		t.Errorf("execute_query on Places = %+v; want an error with %q", res, want)
	}
}

// TestRun_RefusesNewerInstanceStore checks that an instance store a newer version of bdb migrated past this one's
// migrations is refused at startup with a message saying so, in place of the migrator's.
func TestRun_RefusesNewerInstanceStore(t *testing.T) {
	dataDir, _ := newBdbWithPlaces(t)
	execInstanceStore(t, dataDir, "UPDATE schema_migrations_auth SET version = version + 1")

	code, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs")
	if want := "Error: this data directory was upgraded by a newer version of bdb: update this binary\n"; code !=
		exitFailure || stderr != want {
		t.Errorf("bdbs = %d, stderr %q; want %d and %q", code, stderr, exitFailure, want)
	}
}

// execInstanceStore runs query with args on the instance store of dataDir.
func execInstanceStore(t *testing.T, dataDir, query string, args ...any) {
	t.Helper()
	db, err := database.OpenSQLite(context.Background(), filepath.Join(dataDir, "instance.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}
