//go:build integration

package integrationtest

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/instance"
	"github.com/alexandremgo/baguettedb/pkg/mcpgateway"
)

// TestMCP_Stdio_ImplicitOwner serves an instance in local mode over a stdio pipe and drives it with an MCP client
// holding no token: the server reports the name, version and instructions preamble it was given, and a bdb created
// over the pipe is listed back with the implicit owner as its owner.
func TestMCP_Stdio_ImplicitOwner(t *testing.T) {
	truncateAll(t)
	gwOpts := mcpgateway.Options{
		ServerName:           "baguettedb-local",
		ServerVersion:        "test",
		InstructionsPreamble: "This is the local instance.",
	}
	inst := newLocalInstance(t, gwOpts)
	c, initResult := serveStdio(t, inst)

	if got := initResult.ServerInfo; got.Name != gwOpts.ServerName || got.Version != gwOpts.ServerVersion {
		t.Errorf("server info = %+v, want %s %s", got, gwOpts.ServerName, gwOpts.ServerVersion)
	}
	if want := gwOpts.InstructionsPreamble + "\n\n" + mcpgateway.Instructions; initResult.Instructions != want {
		t.Errorf("instructions = %q, want the preamble then the default instructions", initResult.Instructions)
	}

	created := callTool(t, c, "create_bdb", map[string]any{"name": "Local notes"})
	if created.IsError {
		t.Fatalf("create_bdb: %v", created.Content)
	}
	bdbID := parseToolJSON(t, created)["id"]

	listed := callTool(t, c, "list_bdbs", nil)
	if listed.IsError {
		t.Fatalf("list_bdbs: %v", listed.Content)
	}
	bdbs := parseToolJSONArray(t, listed)
	if len(bdbs) != 1 {
		t.Fatalf("list_bdbs returned %d bdbs, want 1", len(bdbs))
	}
	if got := bdbs[0].(map[string]any); got["id"] != bdbID || got["role"] != "owner" {
		t.Errorf("list_bdbs = %v, want bdb %v with role owner", got, bdbID)
	}
}

// newLocalInstance composes every app on the suite's instance store in local mode: no OAuth flow, HTTP routes
// refused, the implicit owner ensured, and bdb files in a temporary directory keyed by a KEK generated on the spot.
func newLocalInstance(t *testing.T, gwOpts mcpgateway.Options) *instance.Instance {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	cfg.DatabasesDataDir = t.TempDir()
	cfg.DatabasesKEKFile = filepath.Join(t.TempDir(), "kek")
	cfg.DatabasesKEKAutogenerate = true
	backups := false
	cfg.DatabasesBackupEnabled = &backups
	testDB.Configure(&cfg)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	inst, err := instance.New(ctx, cfg, testLogger, instance.Options{
		Authentication: instance.InjectedAuth(refuseAll),
		ImplicitOwner:  true,
		Gateway:        gwOpts,
	})
	if err != nil {
		t.Fatalf("instance.New: %v", err)
	}
	t.Cleanup(inst.Close)
	return inst
}

// refuseAll answers every request with 401, standing in for the HTTP authentication a stdio-only instance never
// serves.
func refuseAll(http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

// serveStdio runs inst.ServeStdio on a pair of in-memory pipes and returns an initialized MCP client on the other
// end, with the initialize result.
// The cleanup closes the client, which ends the server's input, and waits for ServeStdio to return.
func serveStdio(t *testing.T, inst *instance.Instance) (*mcpclient.Client, *mcp.InitializeResult) {
	t.Helper()
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()

	served := make(chan error, 1)
	go func() {
		served <- inst.ServeStdio(context.Background(), serverIn, serverOut, testLogger)
		_ = serverOut.Close()
	}()

	c := mcpclient.NewClient(transport.NewIO(clientIn, clientOut, nil))
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("closing the MCP client: %v", err)
		}
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("ServeStdio: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("ServeStdio did not return after its input closed")
		}
	})

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("starting the MCP client: %v", err)
	}
	initResult, err := c.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{
		ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcp.Implementation{Name: "stdio-test", Version: "1.0.0"},
	}})
	if err != nil {
		t.Fatalf("initializing the MCP client: %v", err)
	}
	if strings.TrimSpace(initResult.ServerInfo.Name) == "" {
		t.Fatal("initialize returned no server info")
	}
	return c, initResult
}
