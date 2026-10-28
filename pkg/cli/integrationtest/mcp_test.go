//go:build integration

package integrationtest

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alexandremgo/baguettedb/pkg/cli"
)

// TestRun_MCP_ServesTheLocalInstance runs `bdb mcp` twice on a fresh data directory, each time driven over its stdin
// and stdout by an MCP client. The first run creates the data directory (owner-only, with the instance store, the
// databases directory and the generated KEK), reports the local server identity with the version `bdb version`
// prints and the data directory in its instructions, and creates a bdb owned by the implicit owner; the second run
// lists that bdb back. Both exit 0 once the client closes their input.
func TestRun_MCP_ServesTheLocalInstance(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), "home", ".baguettedb")

	c, initResult, stop := startMCP(t, dataDir)
	_, versionLine, _ := runCLI(t, "bdb", "version")
	info := initResult.ServerInfo
	if info.Name != "baguettedb-local" || versionLine != "bdb "+info.Version+"\n" {
		t.Errorf("server info = %+v, want baguettedb-local at the version of %q", info, versionLine)
	}
	if !strings.Contains(initResult.Instructions, dataDir) {
		t.Errorf("instructions do not name the data directory %s: %q", dataDir, initResult.Instructions)
	}
	created := callToolJSON[map[string]any](t, c, "create_bdb", map[string]any{"name": "Groceries"})
	stop()

	assertMode(t, dataDir, os.ModeDir|0o700)
	assertMode(t, filepath.Join(dataDir, "databases"), os.ModeDir|0o700)
	assertMode(t, filepath.Join(dataDir, "kek"), 0o600)
	if info, err := os.Stat(filepath.Join(dataDir, "instance.sqlite")); err != nil || !info.Mode().IsRegular() {
		t.Errorf("instance store: %v, %v; want a regular file", info, err)
	}

	c, _, stop = startMCP(t, dataDir)
	bdbs := callToolJSON[[]map[string]any](t, c, "list_bdbs", nil)
	if len(bdbs) != 1 || bdbs[0]["id"] != created["id"] || bdbs[0]["role"] != "owner" {
		t.Errorf("list_bdbs after a restart = %v, want bdb %v with role owner", bdbs, created["id"])
	}
	stop()
}

// startMCP runs `bdb --data-dir dataDir mcp`, with the global flags in global before mcp, on in-memory pipes and
// returns an initialized MCP client on the other end, with the initialize result. stop closes the client, which ends
// the run's input, and fails the test unless the run then exits 0.
func startMCP(t *testing.T, dataDir string, global ...string) (*mcpclient.Client, *mcp.InitializeResult, func()) {
	t.Helper()
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()

	var stderr syncBuffer
	exited := make(chan int, 1)
	go func() {
		args := append(append([]string{"bdb", "--data-dir", dataDir}, global...), "mcp")
		exited <- cli.Run(args, serverIn, serverOut, &stderr)
		// Unblocks the client whether the run ended normally or failed before reading its input.
		serverIn.CloseWithError(io.ErrClosedPipe)
		serverOut.CloseWithError(io.EOF)
	}()

	c := mcpclient.NewClient(transport.NewIO(clientIn, clientOut, nil))
	stop := func() {
		if err := c.Close(); err != nil {
			t.Errorf("closing the MCP client: %v", err)
		}
		select {
		case code := <-exited:
			if code != exitOK {
				t.Errorf("bdb mcp exited %d; stderr:\n%s", code, stderr.String())
			}
		case <-time.After(10 * time.Second):
			t.Errorf("bdb mcp did not exit after its input closed; stderr:\n%s", stderr.String())
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("starting the MCP client: %v", err)
	}
	initResult, err := c.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{
		ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcp.Implementation{Name: "cli-test", Version: "1.0.0"},
	}})
	if err != nil {
		t.Fatalf("initializing the MCP client (stderr:\n%s): %v", stderr.String(), err)
	}
	return c, initResult, stop
}

// callToolJSON calls the tool name with args and decodes its text result as JSON into a T.
func callToolJSON[T any](t *testing.T, c *mcpclient.Client, name string, args map[string]any) T {
	t.Helper()
	var out T
	res, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: name, Arguments: args},
	})
	if err != nil {
		t.Fatalf("calling %s: %v", name, err)
	}
	if res.IsError || len(res.Content) == 0 {
		t.Fatalf("%s failed: %v", name, res.Content)
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("%s returned %T, want text", name, res.Content[0])
	}
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		t.Fatalf("decoding the %s result %q: %v", name, text.Text, err)
	}
	return out
}
