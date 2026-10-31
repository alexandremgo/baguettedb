package mcpgateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
)

// TestConnectBackendWithRetry_FailsAfterTimeoutWhenDown checks that a permanently-unreachable backend is retried
// (not failed-fast) and still gives up around the timeout instead of hanging.
func TestConnectBackendWithRetry_FailsAfterTimeoutWhenDown(t *testing.T) {
	headerFunc := func(context.Context) map[string]string { return nil }

	start := time.Now()
	// 127.0.0.1:1 has nothing listening, so each attempt fails fast (connection refused) — exercising the
	// retry+timeout path rather than a per-attempt network timeout.
	_, err := connectBackendWithRetry("http://127.0.0.1:1", headerFunc, 600*time.Millisecond, zerolog.Nop())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error connecting to a dead backend")
	}
	if elapsed < 400*time.Millisecond {
		t.Errorf("gave up in %s — expected at least one ~500ms backoff (i.e. a retry) before failing", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Errorf("took %s — expected it to honour the ~600ms timeout, not hang", elapsed)
	}
}

// TestSetupHTTP_ConnectsAfterBackendBecomesReady checks that the gateway rides out a backend that is unreachable at
// first and only becomes ready after the initial attempt — it must succeed on a retry rather than crash-looping.
func TestSetupHTTP_ConnectsAfterBackendBecomesReady(t *testing.T) {
	backend := mcpserver.NewMCPServer("test-backend", "1.0.0")
	backend.AddTool(
		mcp.NewTool("ping", mcp.WithDescription("ping")),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("pong"), nil
		},
	)
	streamable := mcpserver.NewStreamableHTTPServer(backend)

	const internalAPISecret = "test-internal-api-secret"
	var ready atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/internal/mcp", func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// The backend gates its internal MCP endpoint on the shared secret, so this also pins that the
		// gateway's header func sends it.
		if r.Header.Get(authhttp.InternalSecretHeader) != internalAPISecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		streamable.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Becomes reachable only after the first attempt, so setup must succeed on a subsequent retry.
	time.AfterFunc(150*time.Millisecond, func() { ready.Store(true) })

	result, err := SetupHTTP([]string{srv.URL}, 10*time.Second, internalAPISecret, zerolog.Nop(), Options{})
	if err != nil {
		t.Fatalf("SetupHTTP did not recover once the backend became ready: %v", err)
	}
	defer result.Cleanup()
	if result.StreamableServer == nil {
		t.Fatal("expected a streamable server after setup")
	}
}

// TestSetupInProcess_ServesClientsOfEitherProtocolEra checks that a tool call reaches an in-process backend through
// the gateway's Streamable HTTP server whichever protocol era the client speaks: one on the initialize handshake
// sends no Mcp-Method or Mcp-Name header, and that must not fail the call on the backend.
func TestSetupInProcess_ServesClientsOfEitherProtocolEra(t *testing.T) {
	for _, version := range []string{mcp.LATEST_LEGACY_PROTOCOL_VERSION, mcp.LATEST_PROTOCOL_VERSION} {
		t.Run(version, func(t *testing.T) {
			result, err := SetupInProcess([]*mcpserver.MCPServer{newPingBackend()}, zerolog.Nop(), Options{})
			if err != nil {
				t.Fatalf("SetupInProcess: %v", err)
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
				ProtocolVersion: version,
				ClientInfo:      mcp.Implementation{Name: "test", Version: "1.0.0"},
			}}); err != nil {
				t.Fatalf("initializing gateway client: %v", err)
			}
			if got := c.ProtocolVersion(); got != version {
				t.Fatalf("negotiated protocol version %q, want %q", got, version)
			}

			res, err := c.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "ping"}})
			if err != nil {
				t.Fatalf("CallTool through the gateway: %v", err)
			}
			if len(res.Content) != 1 {
				t.Fatalf("tool result content = %+v, want one text", res.Content)
			}
			if text, ok := res.Content[0].(mcp.TextContent); !ok || res.IsError || text.Text != "pong" {
				t.Fatalf("tool result = %+v (error %t), want text pong", res.Content[0], res.IsError)
			}
		})
	}
}
