// Package mcp serves the databases MCP surface: table descriptions, query, data and schema execution, the change log
// with undo, redo and revert, skills, formulas and their runs, and the built-in reference documentation, rendered with
// the caller's limits. NewServer registers the tools on a server with no transport attached.
package mcp

import (
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// Options tunes how the tools shape their answers.
type Options struct {
	// AffectedValueMaxChars is the longest text value, in characters, a write tool's affected_rows carry whole: a
	// longer one is answered as its length and an excerpt, and stays whole in the change log. Zero or negative
	// selects the default (500).
	AffectedValueMaxChars int
}

// NewServer creates an MCP server with all database query tools registered.
func NewServer(
	querySvc *service.QueryService, skillSvc *service.SkillService,
	formulaSvc *service.FormulaService, docsSvc *service.DocsService, opts Options, logger zerolog.Logger,
) *mcpserver.MCPServer {
	if opts.AffectedValueMaxChars <= 0 {
		opts.AffectedValueMaxChars = defaultAffectedValueMaxChars
	}

	s := mcpserver.NewMCPServer(
		"baguettedb-databases",
		"1.0.0",
	)

	registerTools(s, querySvc, skillSvc, formulaSvc, docsSvc, opts, logger)

	return s
}
