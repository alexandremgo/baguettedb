// Package mcp serves the starters MCP surface: the catalog and bdb creation.
package mcp

import (
	"context"
	"errors"

	"github.com/google/uuid"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"

	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	"github.com/alexandremgo/baguettedb/pkg/starters/service"
)

var tracer = otel.Tracer("baguettedb/starters/mcp")

// RegisterTools registers the starters tools on the given MCP server.
func RegisterTools(
	s *mcpserver.MCPServer,
	starterSvc *service.StarterService,
	logger zerolog.Logger,
) {
	s.AddTool(
		mcplib.NewTool(mcputil.ToolListStarters,
			mcplib.WithDescription("Lists the starters a new bdb can be created from. Each entry carries the "+
				"key to pass to "+mcputil.ToolCreateBdb+", what the starter is for, and the formulas the "+
				"bdb will open with. Read it before suggesting a starter, so the suggestion names what "+
				"the user actually gets."),
		),
		listStartersHandler(starterSvc, logger),
	)

	s.AddTool(
		mcplib.NewTool(mcputil.ToolCreateBdb,
			mcplib.WithDescription("Creates a new bdb for the user, either blank or seeded from a starter. "+
				"With `starter_key` the bdb arrives with its tables, formulas, and skill already set up — "+
				"never build a starter's contents by hand instead. "+
				"Only call this when the user has asked for a new bdb, and confirm the name with them "+
				"first. Prefer an existing bdb from "+mcputil.ToolListBdbs+" when one fits: bdbs are a "+
				"limited resource and are deleted from the web app, not from here."),
			mcplib.WithString("name",
				mcplib.Description("Name for the bdb. Required for a blank bdb; with `starter_key` it "+
					"defaults to the starter's own name.")),
			mcplib.WithString("description",
				mcplib.Description("What this bdb is for. Defaults to the starter's description.")),
			mcplib.WithString("starter_key",
				mcplib.Description("Key of the starter to seed from, as listed by "+
					mcputil.ToolListStarters+". Omit for a blank bdb.")),
		),
		createBdbHandler(starterSvc, logger),
	)
}

// parseUserID extracts and parses the user ID from the MCP context.
func parseUserID(ctx context.Context) (uuid.UUID, error) {
	sub, ok := mcputil.UserIDFromContext(ctx)
	if !ok {
		return uuid.Nil, errors.New("missing user identity")
	}

	userID, err := uuid.Parse(sub)
	if err != nil {
		return uuid.Nil, errors.New("invalid user identity")
	}

	return userID, nil
}
