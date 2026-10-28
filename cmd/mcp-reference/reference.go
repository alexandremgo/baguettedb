package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
)

// area groups the tools of the reference under one heading.
type area struct {
	title string
	tools []string
}

// areas is the section order of the tool reference, the order the README presents the tools in. Every
// registered tool must appear in exactly one area and every name here must be registered: renderToolsDoc fails
// otherwise, so a new tool has to be placed deliberately instead of landing in an arbitrary section.
var areas = []area{
	{title: "Bdbs", tools: []string{
		mcputil.ToolListBdbs, mcputil.ToolListStarters, mcputil.ToolCreateBdb, mcputil.ToolDescribeBdb,
		mcputil.ToolUpdateBdb,
	}},
	{title: "Schema and data", tools: []string{
		mcputil.ToolDescribeTable, mcputil.ToolExecuteQuery, mcputil.ToolExecuteData, mcputil.ToolExecuteSchema,
	}},
	{title: "History", tools: []string{
		mcputil.ToolListChanges, mcputil.ToolUndoLastChange, mcputil.ToolRedoLastChange, mcputil.ToolRevertChange,
		mcputil.ToolUndoLastChangeAnyone, mcputil.ToolRedoLastChangeAnyone,
	}},
	{title: "Formulas", tools: []string{
		mcputil.ToolListFormulas, mcputil.ToolGetFormula, mcputil.ToolCreateFormula, mcputil.ToolUpdateFormula,
		mcputil.ToolDeleteFormula, mcputil.ToolRunFormula,
	}},
	{title: "Skills", tools: []string{mcputil.ToolGetSkill, mcputil.ToolCreateSkill, mcputil.ToolUpdateSkill}},
	{title: "Reference", tools: []string{mcputil.ToolGetDocs}},
}

// propertySchema is the subset of JSON Schema the tool registrations use, decoded from a tool's marshalled input
// schema so the rendering does not depend on how the MCP library stores it in memory.
type propertySchema struct {
	Type        string                    `json:"type"`
	Description string                    `json:"description"`
	Enum        []any                     `json:"enum"`
	Properties  map[string]propertySchema `json:"properties"`
	Required    []string                  `json:"required"`
	Items       *propertySchema           `json:"items"`
}

// renderToolsDoc renders the tool reference: one section per area, one entry per tool with its description and
// a parameter table read from its input schema. It fails when a registered tool has no area or an area names a
// tool nothing registers.
func renderToolsDoc(tools []mcplib.Tool) (string, error) {
	byName := make(map[string]mcplib.Tool, len(tools))
	for _, tool := range tools {
		if _, dup := byName[tool.Name]; dup {
			return "", fmt.Errorf("tool %q is registered twice", tool.Name)
		}
		byName[tool.Name] = tool
	}
	placed := make(map[string]bool, len(tools))
	for _, a := range areas {
		for _, name := range a.tools {
			if _, ok := byName[name]; !ok {
				return "", fmt.Errorf("area %q lists tool %q but no module registers it", a.title, name)
			}
			if placed[name] {
				return "", fmt.Errorf("tool %q is listed in two areas", name)
			}
			placed[name] = true
		}
	}
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		if !placed[name] {
			return "", fmt.Errorf("registered tool %q has no area in cmd/mcp-reference: add it to areas", name)
		}
	}

	var sb strings.Builder
	sb.WriteString("# MCP tools\n\n")
	sb.WriteString("The MCP server of an instance exposes the tools below. An agent acts with the permissions of one " +
		"account, on the bdbs that account is a member of: the owner, or the account `--as` names, when `bdb mcp` " +
		"serves it over stdio; otherwise the account it signed in with or whose token it sends. A tool reserved to " +
		"bdb owners says so in its description. `" + mcputil.ToolGetDocs + "` serves two manuals an agent reads " +
		"before authoring formulas or writing SQL: the [formula authoring manual](formulas.md) and the [SQL " +
		"capabilities and engine rules](sql.md). Every session also opens with the [agent " +
		"instructions](agent-instructions.md).\n")
	for _, a := range areas {
		fmt.Fprintf(&sb, "\n## %s\n", a.title)
		for _, name := range a.tools {
			if err := renderTool(&sb, byName[name]); err != nil {
				return "", err
			}
		}
	}
	return sb.String(), nil
}

// renderTool writes one tool entry: heading, description, parameter table, then the fields of every object or
// array-of-object parameter as nested lists.
func renderTool(sb *strings.Builder, tool mcplib.Tool) error {
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return fmt.Errorf("marshalling %s input schema: %w", tool.Name, err)
	}
	var schema propertySchema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return fmt.Errorf("decoding %s input schema: %w", tool.Name, err)
	}

	fmt.Fprintf(sb, "\n### %s\n\n%s\n", tool.Name, strings.TrimSpace(tool.Description))
	if len(schema.Properties) == 0 {
		sb.WriteString("\nParameters: none.\n")
		return nil
	}

	names := orderedProperties(schema)
	required := make(map[string]bool, len(schema.Required))
	for _, name := range schema.Required {
		required[name] = true
	}
	sb.WriteString("\n| Parameter | Type | Required | Description |\n|---|---|---|---|\n")
	for _, name := range names {
		prop := schema.Properties[name]
		yesNo := "no"
		if required[name] {
			yesNo = "yes"
		}
		fmt.Fprintf(sb, "| `%s` | %s | %s | %s |\n", name, typeLabel(prop), yesNo, tableCell(describe(prop)))
	}
	for _, name := range names {
		renderNested(sb, name, schema.Properties[name])
	}
	return nil
}

// orderedProperties returns the property names of schema, required ones first in declaration order (the order
// the registration listed them), then the optional ones alphabetically.
func orderedProperties(schema propertySchema) []string {
	names := make([]string, 0, len(schema.Properties))
	seen := make(map[string]bool, len(schema.Required))
	for _, name := range schema.Required {
		if _, ok := schema.Properties[name]; ok && !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	optional := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		if !seen[name] {
			optional = append(optional, name)
		}
	}
	slices.Sort(optional)
	return append(names, optional...)
}

// renderNested writes the fields of an object parameter, or of the objects an array parameter holds, as a
// nested list. Scalars and arrays of scalars have nothing to add beyond their table row.
func renderNested(sb *strings.Builder, name string, prop propertySchema) {
	switch {
	case len(prop.Properties) > 0:
		fmt.Fprintf(sb, "\n`%s` fields:\n\n", name)
		writeFields(sb, prop, 0)
	case prop.Items != nil && len(prop.Items.Properties) > 0:
		fmt.Fprintf(sb, "\n`%s` entries:\n\n", name)
		writeFields(sb, *prop.Items, 0)
	}
}

// writeFields writes one bullet per field of an object schema, recursing into object-typed fields and into the
// objects of array-typed fields with a deeper indent.
func writeFields(sb *strings.Builder, obj propertySchema, depth int) {
	required := make(map[string]bool, len(obj.Required))
	for _, name := range obj.Required {
		required[name] = true
	}
	indent := strings.Repeat("  ", depth)
	for _, name := range orderedProperties(obj) {
		field := obj.Properties[name]
		qualifiers := []string{typeLabel(field)}
		if required[name] {
			qualifiers = append(qualifiers, "required")
		}
		if enum := enumText(field); enum != "" {
			qualifiers = append(qualifiers, enum)
		}
		fmt.Fprintf(sb, "%s- `%s` (%s)", indent, name, strings.Join(qualifiers, ", "))
		if desc := strings.TrimSpace(field.Description); desc != "" {
			sb.WriteString(": " + strings.ReplaceAll(desc, "\n", " "))
		}
		sb.WriteString("\n")
		switch {
		case len(field.Properties) > 0:
			writeFields(sb, field, depth+1)
		case field.Items != nil && len(field.Items.Properties) > 0:
			writeFields(sb, *field.Items, depth+1)
		}
	}
}

// typeLabel names a property's type for readers: arrays say what they hold, a missing type reads as any.
func typeLabel(prop propertySchema) string {
	switch {
	case prop.Type == "":
		return "any"
	case prop.Type == "array" && prop.Items != nil && prop.Items.Type != "":
		return "array of " + prop.Items.Type
	default:
		return prop.Type
	}
}

// describe joins a property's description with its allowed values, so an enum is never lost in the table.
func describe(prop propertySchema) string {
	desc := strings.TrimSpace(prop.Description)
	enum := enumText(prop)
	switch {
	case desc == "" && enum == "":
		return ""
	case desc == "":
		return upperFirst(enum) + "."
	case enum == "":
		return desc
	default:
		return strings.TrimSuffix(desc, ".") + ". " + upperFirst(enum) + "."
	}
}

// enumText renders the allowed values of a property, or nothing when it has none.
func enumText(prop propertySchema) string {
	if len(prop.Enum) == 0 {
		return ""
	}
	values := make([]string, 0, len(prop.Enum))
	for _, v := range prop.Enum {
		values = append(values, fmt.Sprintf("`%v`", v))
	}
	return "one of " + strings.Join(values, ", ")
}

// tableCell makes text safe inside a Markdown table cell: pipes are escaped and line breaks become spaces.
func tableCell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "|", "\\|")
}

// upperFirst capitalises the first byte of an ASCII sentence fragment.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
