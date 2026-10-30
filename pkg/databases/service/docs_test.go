package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// testLimits are the caps the doc tests render against.
var testLimits = quota.Limits{
	MaxStatementsPerRequest: 100, MaxRowsPerRequest: 1000, MaxQueryResultRows: 1000, MaxConcurrentQueries: 4,
}

// renderDoc renders one topic against testLimits, failing the test on a render error.
func renderDoc(t *testing.T, topic string) string {
	t.Helper()
	doc, err := renderManual(topic, testLimits, ToolNames)
	if err != nil {
		t.Fatalf("renderManual(%q): %v", topic, err)
	}
	return doc
}

// TestDocs_RenderToolNames pins that the template placeholders resolved to the real tool names: the
// rendered docs mention current mcputil constants and carry no unrendered `{{` marker.
func TestDocs_RenderToolNames(t *testing.T) {
	tests := []struct {
		name  string
		doc   string
		tools []string
	}{
		{name: "formulas", doc: renderDoc(t, "formulas"),
			tools: []string{mcputil.ToolRunFormula, mcputil.ToolUpdateFormula}},
		{name: "sql", doc: renderDoc(t, "sql"), tools: []string{mcputil.ToolExecuteData, mcputil.ToolListChanges}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, tool := range tt.tools {
				if !strings.Contains(tt.doc, "`"+tool+"`") {
					t.Errorf("%s doc does not mention tool %q", tt.name, tool)
				}
			}
			if strings.Contains(tt.doc, "{{") {
				t.Errorf("%s doc still contains an unrendered template marker", tt.name)
			}
		})
	}
}

// TestFormulasDoc_CoversAuthoringSections guards the embedded manual against accidental truncation or
// section loss: every load-bearing authoring concept must keep a home in the served document.
func TestFormulasDoc_CoversAuthoringSections(t *testing.T) {
	formulasDoc := renderDoc(t, "formulas")
	if len(formulasDoc) < 5000 {
		t.Fatalf("formulasDoc suspiciously short: %d bytes", len(formulasDoc))
	}

	sections := []string{
		"## Naming and metadata",
		"## Parameters",
		"### Required, optional, default",
		"### Options (closed vocabulary)",
		"### Conditional visibility (`when`)",
		"### Group parameters (repeated rows)",
		"## Parameter sources",
		"### Lookups",
		"## Multi-statement actions",
		"## Paged views",
		"## Attachment and selection-bound actions",
		"## Display config (views only)",
		"### Slots",
		"### Reorder slot (drag-to-reorder)",
		"### Move slot (drag across board columns or calendar days)",
		"### Calendar mode",
		"## Nested lists (interactive row details)",
		"## Authoring order",
		"## Updating formulas",
	}
	for _, s := range sections {
		if !strings.Contains(formulasDoc, s) {
			t.Errorf("formulasDoc missing section %q", s)
		}
	}

	// Load-bearing rules that must survive any future doc rewrite.
	rules := []string{
		"last_insert_rowid()",
		"rowid AS rowid",
		"delete > insert > update",
		":parent_rowid",
		":ordered_rowids",
		"json_each",
	}
	for _, r := range rules {
		if !strings.Contains(formulasDoc, r) {
			t.Errorf("formulasDoc missing load-bearing rule %q", r)
		}
	}
}

// TestDocsForTopic pins topic resolution: each registered topic renders its document with the given
// limits, and an unknown or empty topic errors with a deterministic, alphabetically ordered list of
// valid topics.
func TestDocsForTopic(t *testing.T) {
	if len(manualTemplates) != 2 {
		t.Fatalf("expected 2 registered doc topics, got %d", len(manualTemplates))
	}

	tests := []struct {
		name      string
		topic     string
		want      string
		wantError bool
	}{
		{name: "formulas topic", topic: "formulas", want: renderDoc(t, "formulas")},
		{name: "sql topic", topic: "sql", want: renderDoc(t, "sql")},
		{name: "unknown topic", topic: "recipes", wantError: true},
		{name: "empty topic", topic: "", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := renderManual(tt.topic, testLimits, ToolNames)
			if tt.wantError {
				if err == nil {
					t.Fatalf("renderManual(%q): expected an error, got none", tt.topic)
				}
				if !strings.Contains(err.Error(), "'formulas', 'sql'") {
					t.Errorf("renderManual(%q) error %q should list the valid topics in alphabetical order",
						tt.topic, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("renderManual(%q): unexpected error: %v", tt.topic, err)
			}
			if doc != tt.want {
				t.Errorf("renderManual(%q) returned the wrong document", tt.topic)
			}
		})
	}
}

// TestSQLDoc_CoversEngineSections guards the embedded SQL capabilities reference the same way.
func TestSQLDoc_CoversEngineSections(t *testing.T) {
	sqlDoc := renderDoc(t, "sql")
	if len(sqlDoc) < 1500 {
		t.Fatalf("sqlDoc suspiciously short: %d bytes", len(sqlDoc))
	}

	sections := []string{
		"## Identifier rules",
		"## Reserved `_` prefix (platform tables)",
		"## JSON support",
		"## Full-text search",
		"## Change log semantics",
		"## Statement limits",
	}
	for _, s := range sections {
		if !strings.Contains(sqlDoc, s) {
			t.Errorf("sqlDoc missing section %q", s)
		}
	}

	rules := []string{
		"sqlite_version()",
		"json_valid",
		"json_extract",
		"jsonb_",
		"fts5",
		"batch_id",
	}
	for _, r := range rules {
		if !strings.Contains(sqlDoc, r) {
			t.Errorf("sqlDoc missing load-bearing rule %q", r)
		}
	}
}

// TestSQLDoc_RendersCallerLimits pins that the limits section quotes the caller's own caps, and that a zero cap,
// which is off, is described as no cap instead of being quoted as a number.
func TestSQLDoc_RendersCallerLimits(t *testing.T) {
	doc, err := renderManual("sql",
		quota.Limits{MaxStatementsPerRequest: 3, MaxRowsPerRequest: 7, MaxQueryResultRows: 11, MaxConcurrentQueries: 5},
		ToolNames)
	if err != nil {
		t.Fatalf("renderManual: %v", err)
	}
	for _, want := range []string{"At most\n  11 result rows", "up to 3 DML statements", "at most\n  7 affected rows",
		"up to 3 DDL statements", "At most 5 of your requests"} {
		if !strings.Contains(doc, want) {
			t.Errorf("sql doc missing %q", want)
		}
	}

	doc, err = renderManual("sql", quota.Limits{}, ToolNames)
	if err != nil {
		t.Fatalf("renderManual with zero limits: %v", err)
	}
	_, limitsSection, found := strings.Cut(doc, "## Statement limits")
	if !found {
		t.Fatalf("sql doc with zero limits has no Statement limits section: %s", doc)
	}
	for _, want := range []string{"Results are not capped", "any number of DML statements",
		"no cap on\n  affected rows", "no concurrency cap", "any number of DDL statements"} {
		if !strings.Contains(limitsSection, want) {
			t.Errorf("sql doc with zero limits missing %q, got: %s", want, limitsSection)
		}
	}
	for _, unwanted := range []string{"unlimited", "At most", "up to "} {
		if strings.Contains(limitsSection, unwanted) {
			t.Errorf("sql doc with zero limits still quotes a cap (%q), got: %s", unwanted, limitsSection)
		}
	}
}

// TestRenderManual_NamesOperationsAsTheReaderCallsThem checks that a reader's Namer replaces every tool name the
// manuals cite, so a reader who calls operations by other names meets none of the tool names.
func TestRenderManual_NamesOperationsAsTheReaderCallsThem(t *testing.T) {
	cited := []string{mcputil.ToolRunFormula, mcputil.ToolUpdateFormula, mcputil.ToolGetFormula,
		mcputil.ToolDescribeBdb, mcputil.ToolListFormulas, mcputil.ToolExecuteQuery, mcputil.ToolExecuteData,
		mcputil.ToolExecuteSchema, mcputil.ToolListChanges, mcputil.ToolUndoLastChange, mcputil.ToolRedoLastChange,
		mcputil.ToolRevertChange}
	renamed := func(tool string) string { return "op:" + strings.ToUpper(tool) }
	for _, topic := range ManualTopics() {
		doc, err := renderManual(topic, testLimits, renamed)
		if err != nil {
			t.Fatalf("renderManual(%q): %v", topic, err)
		}
		for _, tool := range cited {
			if strings.Contains(doc, "`"+tool+"`") {
				t.Errorf("the %s manual still cites `%s`", topic, tool)
			}
		}
		if !strings.Contains(doc, "op:") {
			t.Errorf("the %s manual cites no renamed operation", topic)
		}
	}
}

// TestFormulasManualCovers checks that a formula refused for an authoring rule is one the formulas manual covers, even
// wrapped, and that a not-found error is not.
func TestFormulasManualCovers(t *testing.T) {
	if !FormulasManualCovers(fmt.Errorf("creating: %w", domain.ErrFormulaParamInvalid)) {
		t.Error("an invalid parameter should be covered by the formulas manual")
	}
	if FormulasManualCovers(domain.ErrFormulaNotFound) {
		t.Error("an unknown formula should not be covered by the formulas manual")
	}
}
