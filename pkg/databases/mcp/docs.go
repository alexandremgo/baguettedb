package mcp

import (
	"context"
	_ "embed"
	"fmt"
	"maps"
	"slices"
	"strings"
	"text/template"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// formulasDocTmpl is the formula-authoring manual: the conventions for naming, parameters and their
// sources, multi-statement actions, pagination, display modes, slots, and nested row details. Tool
// names appear as template placeholders resolved from the mcputil constants at render time.
//
//go:embed docs/formulas.md
var formulasDocTmpl string

// sqlDocTmpl is the SQL capabilities reference: identifier rules, the reserved `_` prefix,
// JSON1/JSONB and FTS5 support, change-log semantics, and per-tool statement limits. Tool names
// appear as template placeholders resolved from the mcputil constants at render time.
//
//go:embed docs/sql.md
var sqlDocTmpl string

// docTemplateData builds the placeholder values for one render: the tool-name constants, so a renamed
// tool breaks rendering (and this package's tests) instead of silently orphaning the prose, and the
// caller's limits, so the SQL reference quotes the caps that apply to them on this instance.
//
// Each cap is its integer value, zero meaning the cap is off.
func docTemplateData(limits quota.Limits) map[string]any {
	return map[string]any{
		"ToolRunFormula":           mcputil.ToolRunFormula,
		"ToolUpdateFormula":        mcputil.ToolUpdateFormula,
		"ToolDescribeBdb":          mcputil.ToolDescribeBdb,
		"ToolListFormulas":         mcputil.ToolListFormulas,
		"ToolExecuteQuery":         mcputil.ToolExecuteQuery,
		"ToolExecuteData":          mcputil.ToolExecuteData,
		"ToolExecuteSchema":        mcputil.ToolExecuteSchema,
		"ToolListChanges":          mcputil.ToolListChanges,
		"ToolUndoLastChange":       mcputil.ToolUndoLastChange,
		"ToolRedoLastChange":       mcputil.ToolRedoLastChange,
		"ToolUndoLastChangeAnyone": mcputil.ToolUndoLastChangeAnyone,
		"ToolRedoLastChangeAnyone": mcputil.ToolRedoLastChangeAnyone,
		"ToolRevertChange":         mcputil.ToolRevertChange,
		"MaxStatementsPerRequest":  limits.MaxStatementsPerRequest,
		"MaxRowsPerRequest":        limits.MaxRowsPerRequest,
		"MaxQueryResultRows":       limits.MaxQueryResultRows,
		"MaxConcurrentQueries":     limits.MaxConcurrentQueries,
	}
}

// docTemplates maps each servable documentation topic to its parsed template. Parsing happens once at
// package init (a template that cannot parse is a build defect); rendering happens per request because
// the SQL reference quotes the caller's limits. An unresolved placeholder surfaces as a render error,
// which this package's tests catch before any server boots.
var docTemplates = map[string]*template.Template{
	"formulas": template.Must(template.New("formulas").Option("missingkey=error").Parse(formulasDocTmpl)),
	"sql":      template.Must(template.New("sql").Option("missingkey=error").Parse(sqlDocTmpl)),
}

// DocTopics lists the servable documentation topics in alphabetical order.
func DocTopics() []string {
	return slices.Sorted(maps.Keys(docTemplates))
}

// RenderDocs renders the document for topic against the given limits, which the SQL reference quotes as the
// caps in force (a zero cap is described as no cap). An unknown (or empty) topic returns an error naming every
// valid topic, in alphabetical order so the message is deterministic.
func RenderDocs(topic string, limits quota.Limits) (string, error) {
	tmpl, ok := docTemplates[topic]
	if !ok {
		return "", fmt.Errorf("unknown doc topic: %q valid topics are: '%s'", topic, strings.Join(DocTopics(), "', '"))
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, docTemplateData(limits)); err != nil {
		return "", fmt.Errorf("rendering %s doc: %w", topic, err)
	}
	return sb.String(), nil
}

// registerDocsTool registers the get_docs tool serving the embedded reference documentation, rendered
// with the limits that apply to the calling user.
func registerDocsTool(s *mcpserver.MCPServer, querySvc *service.QueryService) {
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
		getDocsHandler(querySvc),
	)
}

func getDocsHandler(querySvc *service.QueryService) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.get_docs")
		defer span.End()

		args := request.GetArguments()
		topic, _ := args["topic"].(string)

		doc, err := RenderDocs(topic, querySvc.Limits(ctx))
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}

		return mcplib.NewToolResultText(doc), nil
	}
}
