// Package starters wires the starters app: the starter catalog, the service creating bdbs from it, and the REST
// routes and MCP tools serving them. The app has no server of its own and mounts on the bdbs app's.
package starters

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/embedded"
	startersapi "github.com/alexandremgo/baguettedb/pkg/starters/api"
	startersmcp "github.com/alexandremgo/baguettedb/pkg/starters/mcp"
	"github.com/alexandremgo/baguettedb/pkg/starters/service"
)

// Host is where the starters app serves from: the bdbs app's router, the middleware guarding its protected routes,
// and its MCP server. A nil Router or MCPServer mounts nothing there.
type Host struct {
	Router       chi.Router
	RequireToken func(http.Handler) http.Handler
	MCPServer    *mcpserver.MCPServer
}

// SetupOptions configures optional behaviors of the starters module setup.
type SetupOptions struct {
	// Sources are starter sources offered after the built-in catalog, in precedence order. They add starters but
	// never replace a built-in one: the first source offering a key wins.
	Sources []service.StarterSource
}

// SetupResult holds the outputs of the starters module setup.
type SetupResult struct {
	StarterSvc *service.StarterService
}

// Setup builds the starters app over the bdbs and databases apps: its catalog, the built-in starters followed by
// opts.Sources, and the service creating bdbs from it. Its REST routes go on host's router behind host's token
// middleware, its MCP tools on host's MCP server.
func Setup(
	cfg config.Config, logger zerolog.Logger, bdbsApp service.BdbsApp, dbApp service.DatabasesApp, host Host,
	opts SetupOptions,
) (*SetupResult, error) {
	builtin, err := embedded.NewSource()
	if err != nil {
		return nil, fmt.Errorf("loading built-in starters: %w", err)
	}
	registry := service.NewRegistry(catalogSources(builtin, opts.Sources...)...)
	starterSvc := service.NewStarterService(registry, bdbsApp, dbApp, logger)

	if host.Router != nil {
		startersapi.RegisterRoutes(
			host.Router, startersapi.NewHandler(starterSvc, logger), host.RequireToken, cfg.MaxRequestBodySize)
	}
	if host.MCPServer != nil {
		startersmcp.RegisterTools(host.MCPServer, starterSvc, logger)
	}
	return &SetupResult{StarterSvc: starterSvc}, nil
}

// catalogSources returns the sources the catalog consults, in precedence order: builtin, then extra in its own order.
func catalogSources(builtin service.StarterSource, extra ...service.StarterSource) []service.StarterSource {
	return slices.Concat([]service.StarterSource{builtin}, extra)
}
