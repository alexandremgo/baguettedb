//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// TestRun_MCP_DataDirReplaced checks that a running bdb mcp whose data directory is replaced, moved away and another
// one made at its path, refuses every tool call with an error saying so and how to reconnect, rather than answering
// from the files it still holds open, and writes nothing to the new directory.
func TestRun_MCP_DataDirReplaced(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")
	if code, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "create", "Old"); code != exitOK {
		t.Fatalf("creating Old = %d, stderr %q", code, stderr)
	}
	c, _, stop := startMCP(t, dataDir)
	defer stop()
	if bdbs := callToolJSON[[]map[string]any](t, c, "list_bdbs", nil); len(bdbs) != 1 || bdbs[0]["name"] != "Old" {
		t.Fatalf("list_bdbs before the replacement = %v, want Old", bdbs)
	}

	newFiles := replaceDataDir(t, dataDir)

	res, err := c.CallTool(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "list_bdbs"}})
	if err != nil {
		t.Fatalf("list_bdbs after the replacement: %v", err)
	}
	text := ""
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(mcp.TextContent); ok {
			text = tc.Text
		}
	}
	if !res.IsError || !strings.Contains(text, "was replaced") || !strings.Contains(text, "/mcp in Claude Code") {
		t.Errorf("list_bdbs after the replacement = %q (error %v), want a refusal saying how to reconnect", text,
			res.IsError)
	}
	checkUntouched(t, dataDir, newFiles)
}

// TestRun_ServeDataDirReplaced checks that a running bdb serve whose data directory is replaced refuses every
// request, with 503 and an error saying so and to restart the server, and writes nothing to the new directory.
func TestRun_ServeDataDirReplaced(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")
	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "tokens", "create", "laptop")
	if code != exitOK {
		t.Fatalf("creating a token = %d, stderr %q", code, stderr)
	}
	token := strings.TrimSpace(stdout)
	baseURL, _, stop := startServe(t, dataDir)
	defer stop()
	if status, body := withBearer(t, http.MethodGet, baseURL+"/api/auth/v1/me", token, nil); status != http.StatusOK {
		t.Fatalf("/me before the replacement = %d %s, want 200", status, body)
	}

	newFiles := replaceDataDir(t, dataDir)

	status, body := withBearer(t, http.MethodGet, baseURL+"/api/bdbs/v1/bdbs", token, nil)
	if status != http.StatusServiceUnavailable || !strings.Contains(string(body), "was replaced") ||
		!strings.Contains(string(body), "serve` and start it again") {
		t.Errorf("listing bdbs after the replacement = %d %s, want 503 saying to restart the server", status, body)
	}
	checkUntouched(t, dataDir, newFiles)
}

// replaceDataDir moves dataDir away and makes another data directory at its path, with a bdb of its own, as a reset
// or a restore would; it returns the files the new directory's databases directory holds then.
func replaceDataDir(t *testing.T, dataDir string) []string {
	t.Helper()
	if err := os.Rename(dataDir, dataDir+".old"); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs", "create", "Fresh"); code != exitOK {
		t.Fatalf("creating Fresh in the new directory = %d, stderr %q", code, stderr)
	}
	return databaseFiles(t, dataDir)
}

// checkUntouched fails the test unless the databases directory of dataDir still holds exactly the files want.
func checkUntouched(t *testing.T, dataDir string, want []string) {
	t.Helper()
	if got := databaseFiles(t, dataDir); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("database files of the new directory = %v, want %v untouched", got, want)
	}
}

// databaseFiles returns the names of the bdb files in dataDir's databases directory, sorted.
func databaseFiles(t *testing.T, dataDir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dataDir, "databases", "*.db"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}
