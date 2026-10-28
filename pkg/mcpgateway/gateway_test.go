package mcpgateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
)

// newPingBackend returns an MCP server exposing one tool, ping, that answers "pong".
func newPingBackend() *mcpserver.MCPServer {
	backend := mcpserver.NewMCPServer("test-backend", "1.0.0")
	backend.AddTool(
		mcp.NewTool("ping", mcp.WithDescription("ping")),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("pong"), nil
		},
	)
	return backend
}

// toolCallRecorder is a tool-handler middleware that records the name of every tool call it wraps.
type toolCallRecorder struct {
	mu    sync.Mutex
	names []string
}

func (r *toolCallRecorder) middleware(next mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		r.mu.Lock()
		r.names = append(r.names, req.Params.Name)
		r.mu.Unlock()
		return next(ctx, req)
	}
}

func (r *toolCallRecorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.names...)
}

// startInProcessClient starts and initializes an in-process client over backend, closing it when the test ends.
func startInProcessClient(t *testing.T, backend *mcpserver.MCPServer) *mcpclient.Client {
	t.Helper()
	c, err := mcpclient.NewInProcessClient(backend)
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("starting in-process client: %v", err)
	}
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{
		ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcp.Implementation{Name: "test", Version: "1.0.0"},
	}}); err != nil {
		t.Fatalf("initializing in-process client: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("closing in-process client: %v", err)
		}
	})
	return c
}

// TestNewGateway_AppliesServerOptions checks that a server option handed to NewGateway reaches the aggregated
// server: a tool-handler middleware must observe a proxied call, and the call must still reach the backend.
func TestNewGateway_AppliesServerOptions(t *testing.T) {
	client := startInProcessClient(t, newPingBackend())
	rec := &toolCallRecorder{}

	gw, err := NewGateway(
		[]*mcpclient.Client{client}, zerolog.Nop(), Options{}, mcpserver.WithToolHandlerMiddleware(rec.middleware),
	)
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	call := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ping","arguments":{}}}`)
	resp, err := json.Marshal(gw.MCPServer().HandleMessage(context.Background(), call))
	if err != nil {
		t.Fatalf("marshalling response: %v", err)
	}
	if !strings.Contains(string(resp), `"pong"`) || strings.Contains(string(resp), `"error"`) {
		t.Fatalf("unexpected tools/call response: %s", resp)
	}
	if got := rec.seen(); len(got) != 1 || got[0] != "ping" {
		t.Errorf("middleware saw %v, want [ping]", got)
	}
}

// TestSetupHTTP_AppliesServerOptions checks that a server option handed to SetupHTTP reaches the server behind the
// returned Streamable HTTP transport, not just some intermediate: a client calling through that transport must be
// observed by the middleware.
func TestSetupHTTP_AppliesServerOptions(t *testing.T) {
	const internalAPISecret = "test-internal-api-secret"
	backendTransport := mcpserver.NewStreamableHTTPServer(newPingBackend())
	mux := http.NewServeMux()
	mux.HandleFunc("/internal/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(authhttp.InternalSecretHeader) != internalAPISecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		backendTransport.ServeHTTP(w, r)
	})
	backendSrv := httptest.NewServer(mux)
	defer backendSrv.Close()

	rec := &toolCallRecorder{}
	result, err := SetupHTTP(
		[]string{backendSrv.URL}, 5*time.Second, internalAPISecret, zerolog.Nop(), Options{},
		mcpserver.WithToolHandlerMiddleware(rec.middleware),
	)
	if err != nil {
		t.Fatalf("SetupHTTP: %v", err)
	}
	defer result.Cleanup()

	gatewaySrv := httptest.NewServer(result.StreamableServer)
	defer gatewaySrv.Close()

	ctx := context.Background()
	c, err := mcpclient.NewStreamableHttpClient(gatewaySrv.URL)
	if err != nil {
		t.Fatalf("NewStreamableHttpClient: %v", err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Errorf("closing gateway client: %v", err)
		}
	}()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("starting gateway client: %v", err)
	}
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{
		ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcp.Implementation{Name: "test", Version: "1.0.0"},
	}}); err != nil {
		t.Fatalf("initializing gateway client: %v", err)
	}

	res, err := c.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "ping"}})
	if err != nil {
		t.Fatalf("CallTool through the gateway: %v", err)
	}
	if res.IsError || len(res.Content) != 1 {
		t.Fatalf("unexpected tool result: %+v", res)
	}
	if text, ok := res.Content[0].(mcp.TextContent); !ok || text.Text != "pong" {
		t.Fatalf("tool result content = %+v, want text pong", res.Content[0])
	}
	if got := rec.seen(); len(got) != 1 || got[0] != "ping" {
		t.Errorf("middleware saw %v, want [ping]", got)
	}
}

// TestNewGateway_ReportsOptions checks the server identity and instructions a client receives on initialize: the
// defaults with empty Options, and the given name, version and preamble (before the default instructions) otherwise.
func TestNewGateway_ReportsOptions(t *testing.T) {
	tests := []struct {
		name             string
		opts             Options
		wantName         string
		wantVersion      string
		wantInstructions string
	}{
		{
			name:             "defaults",
			wantName:         DefaultServerName,
			wantVersion:      DefaultServerVersion,
			wantInstructions: Instructions,
		},
		{
			name: "local",
			opts: Options{
				ServerName: "baguettedb-local", ServerVersion: "v1.2.3", InstructionsPreamble: "Hi.",
			},
			wantName:         "baguettedb-local",
			wantVersion:      "v1.2.3",
			wantInstructions: "Hi.\n\n" + Instructions,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := startInProcessClient(t, newPingBackend())
			gw, err := NewGateway([]*mcpclient.Client{backend}, zerolog.Nop(), tt.opts)
			if err != nil {
				t.Fatalf("NewGateway: %v", err)
			}
			c, err := mcpclient.NewInProcessClient(gw.MCPServer())
			if err != nil {
				t.Fatalf("NewInProcessClient: %v", err)
			}
			defer func() {
				if err := c.Close(); err != nil {
					t.Errorf("closing the client: %v", err)
				}
			}()
			ctx := context.Background()
			if err := c.Start(ctx); err != nil {
				t.Fatalf("starting the client: %v", err)
			}
			res, err := c.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{
				ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
				ClientInfo:      mcp.Implementation{Name: "test", Version: "1.0.0"},
			}})
			if err != nil {
				t.Fatalf("initializing: %v", err)
			}
			if res.ServerInfo.Name != tt.wantName || res.ServerInfo.Version != tt.wantVersion {
				t.Errorf("server info = %+v, want %s %s", res.ServerInfo, tt.wantName, tt.wantVersion)
			}
			if res.Instructions != tt.wantInstructions {
				t.Errorf("instructions = %q, want %q", res.Instructions, tt.wantInstructions)
			}
		})
	}
}
