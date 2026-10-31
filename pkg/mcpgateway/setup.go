package mcpgateway

import (
	"context"
	"fmt"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
)

// SetupResult holds the outputs of the MCP gateway setup.
type SetupResult struct {
	// MCPServer is the aggregated server, for transports other than StreamableServer (stdio).
	MCPServer        *mcpserver.MCPServer
	StreamableServer *mcpserver.StreamableHTTPServer
	Cleanup          func()
}

// SetupInProcess creates an MCP gateway from in-process MCP servers. Each server is wrapped with NewInProcessClient
// for zero-network-hop communication. gwOpts and opts are forwarded to the aggregated server (see NewGateway).
func SetupInProcess(
	servers []*mcpserver.MCPServer, logger zerolog.Logger, gwOpts Options, opts ...mcpserver.ServerOption,
) (*SetupResult, error) {
	clients := make([]*mcpclient.Client, 0, len(servers))
	cleanup := func() {
		for _, cl := range clients {
			if err := cl.Close(); err != nil {
				logger.Warn().Err(err).Msg("closing MCP client during cleanup")
			}
		}
	}

	for i, srv := range servers {
		c, err := mcpclient.NewInProcessClient(srv)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("creating in-process client for backend %d: %w", i, err)
		}

		ctx := context.Background()
		if err := c.Start(ctx); err != nil {
			if cerr := c.Close(); cerr != nil {
				logger.Warn().Err(cerr).Int("backend", i).Msg("closing MCP client after start failure")
			}
			cleanup()
			return nil, fmt.Errorf("starting in-process client %d: %w", i, err)
		}

		// Stays on the initialize handshake: a proxied call runs in the caller's HTTP request context, and the backend
		// checks a 2026-07-28 call against that request's headers, which a client of an earlier version never sends.
		if _, err := c.Initialize(ctx, mcp.InitializeRequest{
			Params: mcp.InitializeParams{
				ProtocolVersion: mcp.LATEST_LEGACY_PROTOCOL_VERSION,
				ClientInfo: mcp.Implementation{
					Name:    "baguettedb-gateway",
					Version: "1.0.0",
				},
			},
		}); err != nil {
			if cerr := c.Close(); cerr != nil {
				logger.Warn().Err(cerr).Int("backend", i).Msg("closing MCP client after init failure")
			}
			cleanup()
			return nil, fmt.Errorf("initializing in-process client %d: %w", i, err)
		}

		clients = append(clients, c)
	}

	gw, err := NewGateway(clients, logger, gwOpts, opts...)
	if err != nil {
		cleanup()
		return nil, err
	}

	streamableServer := mcpserver.NewStreamableHTTPServer(gw.MCPServer(),
		mcpserver.WithHTTPContextFunc(mcputil.SSEContextFunc),
		mcpserver.WithStateLess(true),
	)

	return &SetupResult{
		MCPServer:        gw.MCPServer(),
		StreamableServer: streamableServer,
		Cleanup:          cleanup,
	}, nil
}

// SetupHTTP creates an MCP gateway that connects to backend MCP servers over Streamable HTTP. Each tool call
// carries the shared internal API secret (the backends' internal endpoints are gated on it) and forwards the
// caller's JWT (from context) as an Authorization header so backends can identify the user. Connecting to each
// backend is retried with backoff for up to backendInitTimeout, so a backend that is briefly unreachable at
// startup (a rolling restart or a blue/green cutover) doesn't fail setup and crash-loop the gateway. gwOpts and opts
// are forwarded to the aggregated server (see NewGateway).
func SetupHTTP(
	backendURLs []string, backendInitTimeout time.Duration, internalAPISecret string, logger zerolog.Logger,
	gwOpts Options, opts ...mcpserver.ServerOption,
) (*SetupResult, error) {
	clients := make([]*mcpclient.Client, 0, len(backendURLs))
	cleanup := func() {
		for _, cl := range clients {
			if err := cl.Close(); err != nil {
				logger.Warn().Err(err).Msg("closing MCP client during cleanup")
			}
		}
	}

	headerFunc := func(ctx context.Context) map[string]string {
		headers := make(map[string]string)
		headers[authhttp.InternalSecretHeader] = internalAPISecret
		if token, ok := mcputil.JWTFromContext(ctx); ok {
			headers["Authorization"] = "Bearer " + token
		}
		// Propagates W3C trace context (traceparent header) to downstream services
		// so their spans join the same distributed trace.
		otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(headers))
		return headers
	}

	for i, url := range backendURLs {
		c, err := connectBackendWithRetry(url, headerFunc, backendInitTimeout, logger)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("connecting to streamable HTTP backend %d (%s): %w", i, url, err)
		}
		clients = append(clients, c)
	}

	gw, err := NewGateway(clients, logger, gwOpts, opts...)
	if err != nil {
		cleanup()
		return nil, err
	}

	streamableServer := mcpserver.NewStreamableHTTPServer(gw.MCPServer(),
		mcpserver.WithHTTPContextFunc(mcputil.GatewaySSEContextFunc),
		mcpserver.WithStateLess(true),
	)

	return &SetupResult{
		MCPServer:        gw.MCPServer(),
		StreamableServer: streamableServer,
		Cleanup:          cleanup,
	}, nil
}

// connectBackendWithRetry creates, starts, and initializes a Streamable HTTP MCP client for url, retrying with
// exponential backoff until timeout elapses. It lets the gateway ride out a backend that is briefly unreachable at
// startup (a rolling restart or a blue/green cutover) instead of failing fast and crash-looping.
func connectBackendWithRetry(
	url string,
	headerFunc func(context.Context) map[string]string,
	timeout time.Duration,
	logger zerolog.Logger,
) (*mcpclient.Client, error) {
	mcpURL := url + "/internal/mcp"

	tryConnect := func() (*mcpclient.Client, error) {
		c, err := mcpclient.NewStreamableHttpClient(mcpURL, transport.WithHTTPHeaderFunc(headerFunc))
		if err != nil {
			return nil, fmt.Errorf("creating client: %w", err)
		}
		ctx := context.Background()
		if err := c.Start(ctx); err != nil {
			if cerr := c.Close(); cerr != nil {
				logger.Warn().Err(cerr).Str("backend", url).Msg("closing MCP client after start failure")
			}
			return nil, fmt.Errorf("starting client: %w", err)
		}
		if _, err := c.Initialize(ctx, mcp.InitializeRequest{
			Params: mcp.InitializeParams{
				ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
				ClientInfo:      mcp.Implementation{Name: "baguettedb-gateway", Version: "1.0.0"},
			},
		}); err != nil {
			if cerr := c.Close(); cerr != nil {
				logger.Warn().Err(cerr).Str("backend", url).Msg("closing MCP client after init failure")
			}
			return nil, fmt.Errorf("initializing client: %w", err)
		}
		return c, nil
	}

	deadline := time.Now().Add(timeout)
	backoff := 500 * time.Millisecond
	const maxBackoff = 5 * time.Second
	for attempt := 1; ; attempt++ {
		c, err := tryConnect()
		if err == nil {
			if attempt > 1 {
				logger.Info().Str("backend", url).Int("attempts", attempt).Msg("MCP backend connected after retry")
			}
			return c, nil
		}
		// Stops once another backoff would run past the deadline, so the total wait honours timeout.
		if !time.Now().Add(backoff).Before(deadline) {
			return nil, fmt.Errorf("backend not reachable within %s: %w", timeout, err)
		}
		logger.Warn().Err(err).Str("backend", url).Dur("retry_in", backoff).Msg("MCP backend not ready; retrying")
		time.Sleep(backoff)
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}
