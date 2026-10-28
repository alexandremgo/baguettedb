package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
)

// TestRenderToolsDoc_CoversEveryRegisteredTool pins that the live listing and the area map agree:
// every registered tool gets exactly one heading, every area is rendered, and the count matches the map.
func TestRenderToolsDoc_CoversEveryRegisteredTool(t *testing.T) {
	tools := collectTools()
	doc, err := renderToolsDoc(tools)
	if err != nil {
		t.Fatalf("renderToolsDoc: %v", err)
	}

	mapped := 0
	for _, a := range areas {
		mapped += len(a.tools)
		if !strings.Contains(doc, "\n## "+a.title+"\n") {
			t.Errorf("area %q has no heading", a.title)
		}
	}
	if mapped != len(tools) {
		t.Errorf("areas map %d tools, modules register %d", mapped, len(tools))
	}
	for _, tool := range tools {
		if n := strings.Count(doc, "\n### "+tool.Name+"\n"); n != 1 {
			t.Errorf("tool %q has %d headings, want 1", tool.Name, n)
		}
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
	}
}

// TestRenderToolsDoc_RejectsUnplacedAndMissingTools pins the two guards: a registered tool without an area and
// an area naming an unregistered tool both fail with the tool's name in the error.
func TestRenderToolsDoc_RejectsUnplacedAndMissingTools(t *testing.T) {
	tools := collectTools()

	extra := append(copyTools(tools), mcplib.NewTool("brand_new_tool", mcplib.WithDescription("Does something.")))
	if _, err := renderToolsDoc(extra); err == nil || !strings.Contains(err.Error(), "brand_new_tool") {
		t.Errorf("unplaced tool: want an error naming it, got %v", err)
	}

	var without []mcplib.Tool
	for _, tool := range tools {
		if tool.Name != mcputil.ToolGetDocs {
			without = append(without, tool)
		}
	}
	if _, err := renderToolsDoc(without); err == nil || !strings.Contains(err.Error(), mcputil.ToolGetDocs) {
		t.Errorf("missing tool: want an error naming %s, got %v", mcputil.ToolGetDocs, err)
	}
}

// copyTools copies a tool list so a test can append without aliasing the original backing array.
func copyTools(tools []mcplib.Tool) []mcplib.Tool {
	return append([]mcplib.Tool(nil), tools...)
}

// TestRenderTool_ParameterTableAndNestedFields pins the rendering shapes on a synthetic tool: required
// parameters lead the table in declaration order, optional ones follow alphabetically, enums land in the
// description, and object and array-of-object parameters get a nested field list.
func TestRenderTool_ParameterTableAndNestedFields(t *testing.T) {
	tool := mcplib.NewTool("sample",
		mcplib.WithDescription("Samples things."),
		mcplib.WithString("zeta", mcplib.Description("Optional, sorts last.")),
		mcplib.WithString("kind", mcplib.Required(), mcplib.Enum("a", "b"), mcplib.Description("Which kind.")),
		mcplib.WithString("alpha", mcplib.Description("Optional, sorts first among optionals.")),
		mcplib.WithArray("entries", mcplib.Description("Rows."), mcplib.Items(map[string]any{
			"type":     "object",
			"required": []string{"name"},
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "Row name."},
				"mode": map[string]any{"type": "string", "enum": []any{"x", "y"}},
			},
		})),
		mcplib.WithObject("display", mcplib.Description("Presentation."), mcplib.Properties(map[string]any{
			"columns": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		})),
	)

	var sb strings.Builder
	if err := renderTool(&sb, tool); err != nil {
		t.Fatalf("renderTool: %v", err)
	}
	doc := sb.String()

	wantInOrder := []string{
		"### sample\n\nSamples things.\n",
		"| `kind` | string | yes | Which kind. One of `a`, `b`. |",
		"| `alpha` | string | no | Optional, sorts first among optionals. |",
		"| `display` | object | no | Presentation. |",
		"| `entries` | array of object | no | Rows. |",
		"| `zeta` | string | no | Optional, sorts last. |",
		"`display` fields:\n\n- `columns` (array of string)\n",
		"`entries` entries:\n\n- `name` (string, required): Row name.\n- `mode` (string, one of `x`, `y`)\n",
	}
	pos := 0
	for _, want := range wantInOrder {
		idx := strings.Index(doc[pos:], want)
		if idx < 0 {
			t.Fatalf("rendered doc lacks %q after position %d:\n%s", want, pos, doc)
		}
		pos += idx + len(want)
	}
}

// TestRun_WritesEveryReferenceFile pins the file set and the generated header on each file.
func TestRun_WritesEveryReferenceFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "reference")
	if err := run(dir); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, name := range []string{"mcp-tools.md", "formulas.md", "sql.md", "agent-instructions.md"} {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if !strings.HasPrefix(string(content), generatedHeader) {
			t.Errorf("%s does not start with the generated header", name)
		}
		if len(content) < len(generatedHeader)+500 {
			t.Errorf("%s suspiciously short: %d bytes", name, len(content))
		}
	}
}

// TestRenderAll_SQLQuotesDefaultLimits pins that the SQL reference quotes the caps an unconfigured instance
// enforces, read from the config package's exported defaults, and never describes them as uncapped.
func TestRenderAll_SQLQuotesDefaultLimits(t *testing.T) {
	files, err := renderAll(collectTools())
	if err != nil {
		t.Fatalf("renderAll: %v", err)
	}
	sql := files["sql.md"]
	for _, want := range []string{
		fmt.Sprintf("At most\n  %d result rows", config.DefaultQuotaMaxQueryResultRows),
		fmt.Sprintf("up to %d DML statements", config.DefaultQuotaMaxStatementsPerRequest),
		fmt.Sprintf("at most\n  %d affected rows", config.DefaultQuotaMaxRowsPerRequest),
		fmt.Sprintf("At most %d of your requests", config.DefaultQuotaMaxConcurrentQueries),
		fmt.Sprintf("up to %d DDL statements", config.DefaultQuotaMaxStatementsPerRequest),
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("sql.md missing %q", want)
		}
	}
	for _, unwanted := range []string{"not capped", "no cap", "any number of"} {
		if strings.Contains(sql, unwanted) {
			t.Errorf("sql.md describes a default cap as off (%q)", unwanted)
		}
	}
}

// TestRenderAll_SQLNotesLimitsAreDefaults pins the note under the SQL reference's title, which frames the quoted
// limits as defaults and links to their section, and that no other manual carries it.
func TestRenderAll_SQLNotesLimitsAreDefaults(t *testing.T) {
	files, err := renderAll(collectTools())
	if err != nil {
		t.Fatalf("renderAll: %v", err)
	}
	wantOpening := "# SQL capabilities and engine rules\n\n> " + manualNotes["sql"] + "\n\nEach bdb is a SQLite"
	if !strings.HasPrefix(files["sql.md"], wantOpening) {
		t.Errorf("sql.md does not open with its title, the note, then the body:\n%.400s", files["sql.md"])
	}
	if !strings.Contains(files["sql.md"], "\n## Statement limits\n") {
		t.Error("sql.md lost the Statement limits heading the note links to")
	}
	if strings.Contains(files["formulas.md"], manualNotes["sql"]) {
		t.Error("formulas.md carries the SQL note")
	}
}

// TestAddNoteUnderTitle_RequiresOpeningH1 pins that a document not opening with an H1 is refused rather than
// annotated in the wrong place.
func TestAddNoteUnderTitle_RequiresOpeningH1(t *testing.T) {
	if _, err := addNoteUnderTitle("Intro first.\n# Title\n", "note"); err == nil {
		t.Error("expected an error for a document that does not open with an H1")
	}
}
