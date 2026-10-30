package mcp

import (
	"context"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// registerDocsTool registers the get_docs tool serving the embedded reference documentation, rendered
// with the limits that apply to the calling user.
func registerDocsTool(s *mcpserver.MCPServer, docsSvc *service.DocsService) {
	s.AddTool(
		mcplib.NewTool(mcputil.ToolGetDocs,
			mcplib.WithDescription("Returns the full built-in reference documentation for one topic: "+
				"'formulas' — the formula-authoring manual (naming, params and their sources, display modes, "+
				"slots, nested details); 'sql' — SQL capabilities and engine rules (identifier rules, the "+
				"reserved '_' prefix, JSON1/JSONB, FTS5, change-log semantics including the no-BLOB rule, "+
				"statement limits). Fetch a "+
				"topic once per conversation, before the first call that needs it."),
			mcplib.WithString("topic", mcplib.Required(), mcplib.Enum("formulas", "sql"),
				mcplib.Description("Documentation topic to fetch.")),
		),
		getDocsHandler(docsSvc),
	)
}

func getDocsHandler(docsSvc *service.DocsService) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.get_docs")
		defer span.End()

		args := request.GetArguments()
		topic, _ := args["topic"].(string)

		doc, err := docsSvc.Manual(ctx, topic, service.ToolNames)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}

		return mcplib.NewToolResultText(doc), nil
	}
}
