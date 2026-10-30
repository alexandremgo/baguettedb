package service

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"text/template"

	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// formulasManual is the formula-authoring manual: the conventions for naming, parameters and their sources,
// multi-statement actions, pagination, display modes, slots, and nested row details.
//
//go:embed manuals/formulas.md
var formulasManual string

// sqlManual is the SQL capabilities reference: identifier rules, the reserved `_` prefix, JSON1/JSONB and FTS5
// support, change-log semantics, and the statement limits.
//
//go:embed manuals/sql.md
var sqlManual string

// manualTemplates maps each topic to its parsed manual. Parsing happens once at package init (a manual that cannot
// parse is a build defect); rendering happens per reader, whose names and limits differ. An unresolved placeholder
// surfaces as a render error, which this package's tests catch.
var manualTemplates = map[string]*template.Template{
	"formulas": template.Must(template.New("formulas").Option("missingkey=error").Parse(formulasManual)),
	"sql":      template.Must(template.New("sql").Option("missingkey=error").Parse(sqlManual)),
}

// Namer returns the name a reader calls an operation by, given the operation's MCP tool name.
type Namer func(tool string) string

// ToolNames names each operation by its MCP tool name.
func ToolNames(tool string) string {
	return tool
}

// ManualTopics lists the topics of the built-in manuals in alphabetical order.
func ManualTopics() []string {
	return slices.Sorted(maps.Keys(manualTemplates))
}

// limitsReader reads the limits a caller works under.
type limitsReader interface {
	// Limits returns the caps that apply to the caller in ctx.
	Limits(ctx context.Context) quota.Limits
}

// DocsService serves the built-in manuals, the SQL engine's rules and the formula-authoring guide, to a caller: each
// operation they cite under the name the caller calls it by (an MCP tool, a command-line command), and the limits it
// works under as the caps in force.
type DocsService struct {
	limits limitsReader
}

// NewDocsService creates a DocsService reading each caller's limits from limits.
func NewDocsService(limits limitsReader) *DocsService {
	return &DocsService{limits: limits}
}

// Manual renders the manual for topic for the caller in ctx: each operation it cites named as name returns it, and
// the caller's caps quoted as the ones in force (a zero cap is described as no cap). An unknown (or empty) topic
// returns an error naming every topic, in alphabetical order so the message is deterministic.
func (s *DocsService) Manual(ctx context.Context, topic string, name Namer) (string, error) {
	return renderManual(topic, s.limits.Limits(ctx), name)
}

// renderManual renders the manual for topic with name and limits.
func renderManual(topic string, limits quota.Limits, name Namer) (string, error) {
	tmpl, ok := manualTemplates[topic]
	if !ok {
		return "", fmt.Errorf("unknown doc topic: %q valid topics are: '%s'", topic,
			strings.Join(ManualTopics(), "', '"))
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, manualData(limits, name)); err != nil {
		return "", fmt.Errorf("rendering %s doc: %w", topic, err)
	}
	return sb.String(), nil
}

// manualData builds the placeholder values for one render: the name of each operation the manuals cite, taken from
// the mcputil constants so a renamed tool breaks rendering (and this package's tests) instead of silently orphaning
// the prose, and the reader's limits, each cap its integer value, zero meaning the cap is off.
func manualData(limits quota.Limits, name Namer) map[string]any {
	return map[string]any{
		"ToolRunFormula":           name(mcputil.ToolRunFormula),
		"ToolUpdateFormula":        name(mcputil.ToolUpdateFormula),
		"ToolGetFormula":           name(mcputil.ToolGetFormula),
		"ToolDescribeBdb":          name(mcputil.ToolDescribeBdb),
		"ToolListFormulas":         name(mcputil.ToolListFormulas),
		"ToolExecuteQuery":         name(mcputil.ToolExecuteQuery),
		"ToolExecuteData":          name(mcputil.ToolExecuteData),
		"ToolExecuteSchema":        name(mcputil.ToolExecuteSchema),
		"ToolListChanges":          name(mcputil.ToolListChanges),
		"ToolUndoLastChange":       name(mcputil.ToolUndoLastChange),
		"ToolRedoLastChange":       name(mcputil.ToolRedoLastChange),
		"ToolUndoLastChangeAnyone": name(mcputil.ToolUndoLastChangeAnyone),
		"ToolRedoLastChangeAnyone": name(mcputil.ToolRedoLastChangeAnyone),
		"ToolRevertChange":         name(mcputil.ToolRevertChange),
		"MaxStatementsPerRequest":  limits.MaxStatementsPerRequest,
		"MaxRowsPerRequest":        limits.MaxRowsPerRequest,
		"MaxQueryResultRows":       limits.MaxQueryResultRows,
		"MaxConcurrentQueries":     limits.MaxConcurrentQueries,
	}
}

// formulaAuthoringErrors lists the validation errors a formula's author fixes by following the formulas manual.
// Authorization, not-found and infrastructure errors stay out: no manual fixes those.
var formulaAuthoringErrors = []error{
	domain.ErrFormulaNameInvalid,
	domain.ErrFormulaDescriptionInvalid,
	domain.ErrFormulaLabelInvalid,
	domain.ErrFormulaSQLInvalid,
	domain.ErrFormulaKindUndeterminable,
	domain.ErrFormulaParamInvalid,
	domain.ErrFormulaAttachConflict,
	domain.ErrFormulaDisplayInvalid,
	domain.ErrFormulaViewNotPageable,
	domain.ErrUnsupportedStatement,
}

// FormulasManualCovers reports whether the formulas manual explains how to fix err, a refused formula: a rule its
// name, description, label, SQL, parameters, attachment or display broke.
func FormulasManualCovers(err error) bool {
	return slices.ContainsFunc(formulaAuthoringErrors, func(target error) bool { return errors.Is(err, target) })
}
