// Package mcp serves the databases MCP surface: table descriptions, query, data and schema execution, the change log
// with undo, redo and revert, skills, formulas and their runs, and the built-in reference documentation, rendered with
// the caller's limits. NewServer registers the tools on a server with no transport attached.
package mcp

import (
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// NewServer creates an MCP server with all database query tools registered.
func NewServer(
	querySvc *service.QueryService, skillSvc *service.SkillService,
	formulaSvc *service.FormulaService, docsSvc *service.DocsService, logger zerolog.Logger,
) *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer(
		"baguettedb-databases",
		"1.0.0",
	)

	registerTools(s, querySvc, skillSvc, formulaSvc, docsSvc, logger)

	return s
}
