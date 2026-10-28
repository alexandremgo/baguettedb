package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/google/uuid"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// registerFormulaTools registers the formula MCP tools: authoring (create / update / get / list /
// delete) plus run_formula to execute one by name or UUID. Formula discovery for a bdb happens
// via describe_bdb (in the bdbs MCP module).
//
// The authoring descriptions and schemas stay deliberately terse — structure, enums, and short field
// hints only. The full authoring semantics are served on demand by get_docs, and validation is strict
// server-side, so a rejected call self-corrects cheaply; keeping the manual out of the always-injected
// tool definitions is what pays for that tradeoff.
func registerFormulaTools(s *mcpserver.MCPServer, formulaSvc *service.FormulaService, logger zerolog.Logger) {
	s.AddTool(
		mcplib.NewTool(mcputil.ToolCreateFormula,
			mcplib.WithDescription("Creates a formula: a named, parameterized SQL artifact members run from the "+
				"app and you run via "+mcputil.ToolRunFormula+". The kind is derived from the SQL — a single SELECT "+
				"is a 'view' (read); one or more INSERT/UPDATE/DELETE statements separated by ';' are an 'action' "+
				"(write, atomic). No DDL, and reads and writes cannot mix. Use NAMED bind parameters (e.g. "+
				":status), each declared in `params`. Before your first "+mcputil.ToolCreateFormula+" or "+
				mcputil.ToolUpdateFormula+" call in a conversation, call "+mcputil.ToolGetDocs+"(topic: "+
				"'formulas') — the full authoring manual (params and their sources, display modes, slots, "+
				"nested details)."),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
			mcplib.WithString("name", mcplib.Required(),
				mcplib.Description("Slug-shaped name, unique per bdb: lowercase letters, digits and hyphens, "+
					"starting with a letter (e.g. 'low-stock').")),
			mcplib.WithString("label", mcplib.Description("Human name shown in the app instead of the slug; "+
				"presentation only — the name stays the reference. Omit to show the name itself.")),
			mcplib.WithString("description", mcplib.Required(),
				mcplib.Description("One-line description, shown in listings for routing.")),
			mcplib.WithString("sql", mcplib.Required(),
				mcplib.Description("The SQL: a single SELECT for a view, or one or more DML statements separated "+
					"by ';' for an action, using named bind parameters.")),
			mcplib.WithArray("params",
				mcplib.Description("Ordered parameter schema. One entry per named bind parameter the SQL uses."),
				mcplib.Items(formulaParamItemSchema())),
			mcplib.WithString("attached_to_formula_id",
				mcplib.Description("UUID of the view this formula attaches to (as its row/bulk action or row "+
					"detail). Omit for a top-level formula.")),
			mcplib.WithNumber("display_order",
				mcplib.Description("Position among siblings (lower sorts first). Omit to append.")),
			mcplib.WithObject("display",
				mcplib.Description("Presentation config for a view. At create time only slot-free configs pass — "+
					"attach the actions first, then set slots via "+mcputil.ToolUpdateFormula+"."),
				mcplib.Properties(formulaDisplayProperties())),
			mcplib.WithBoolean("hidden",
				mcplib.Description("Marks the formula assistant-only: omitted from the members' formula list; "+
					"MCP tools always see it.")),
		),
		createFormulaHandler(formulaSvc, logger),
	)

	s.AddTool(
		mcplib.NewTool(mcputil.ToolUpdateFormula,
			mcplib.WithDescription("Updates an existing formula resolved by name or UUID. Pass only the fields "+
				"to change; omitted fields are left untouched. Same conventions as "+mcputil.ToolCreateFormula+
				" — call "+mcputil.ToolGetDocs+"(topic: 'formulas') before authoring. Changing `sql` re-derives "+
				"the kind and verb; `params` replaces the whole schema. `display` is tri-state: omit = "+
				"unchanged, null = clear, an object = replace — this is where slots are set (refs by name or "+
				"UUID)."),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
			mcplib.WithString("ref", mcplib.Required(),
				mcplib.Description("Formula reference: the name or the UUID.")),
			mcplib.WithString("name", mcplib.Description("New slug-shaped name, unique per bdb.")),
			mcplib.WithString("label", mcplib.Description("New human name; pass an empty string to drop it.")),
			mcplib.WithString("description", mcplib.Description("New one-line description.")),
			mcplib.WithString("sql", mcplib.Description("New SQL: a single SELECT for a view, or ';'-separated "+
				"DML statements for an action.")),
			mcplib.WithArray("params",
				mcplib.Description("Replacement parameter schema; entries take the same shape as "+
					mcputil.ToolCreateFormula+"'s params. Omit to leave the schema unchanged."),
				mcplib.Items(map[string]any{"type": "object"})),
			mcplib.WithNumber("display_order", mcplib.Description("New position among siblings.")),
			mcplib.WithObject("display",
				mcplib.Description("New presentation config, same shape as "+mcputil.ToolCreateFormula+"'s "+
					"display. Pass null to clear back to the plain table; omit to leave unchanged.")),
			mcplib.WithBoolean("hidden",
				mcplib.Description("New hidden flag: true = assistant-only; false = listed for everyone.")),
		),
		updateFormulaHandler(formulaSvc, logger),
	)

	s.AddTool(
		mcplib.NewTool(mcputil.ToolGetFormula,
			mcplib.WithDescription("Fetches a single formula (with its SQL and parameter schema) by name or UUID. "+
				"Useful to re-read a formula's definition before running or updating it."),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
			mcplib.WithString("ref", mcplib.Required(),
				mcplib.Description("Formula reference: the name or the UUID.")),
		),
		getFormulaHandler(formulaSvc, logger),
	)

	s.AddTool(
		mcplib.NewTool(mcputil.ToolListFormulas,
			mcplib.WithDescription("Lists every formula in the bdb (name, description, kind, SQL, parameter "+
				"schema, display config, hidden flag), ordered by display position then name. Hidden formulas "+
				"are included — hiding only trims the members' app listing, never this tool."),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
		),
		listFormulasHandler(formulaSvc, logger),
	)

	s.AddTool(
		mcplib.NewTool(mcputil.ToolDeleteFormula,
			mcplib.WithDescription("Deletes a formula resolved by name or UUID. Formulas attached to a deleted "+
				"view (its row/bulk actions and row detail) are removed with it."),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
			mcplib.WithString("ref", mcplib.Required(),
				mcplib.Description("Formula reference: the name or the UUID.")),
		),
		deleteFormulaHandler(formulaSvc, logger),
	)

	s.AddTool(
		mcplib.NewTool(mcputil.ToolRunFormula,
			mcplib.WithDescription("Runs a formula by name or UUID, with every declared parameter supplied in "+
				"`params` as name → value (an optional parameter may be omitted). A view returns rows — run "+
				"again with `cursor` set to the response's next_cursor when a `pagination` block reports "+
				"has_more — and needs read permission; an action runs atomically as one write (one undo entry), "+
				"returns the affected-row count, and needs write permission. Selection params bind as `rowid` / "+
				"`rowids` / `parent_rowid`; group and multi-select lookup params take arrays — full passing "+
				"rules in "+mcputil.ToolGetDocs+"(topic: 'formulas')."),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
			mcplib.WithString("ref", mcplib.Required(),
				mcplib.Description("Formula reference: the name or the UUID.")),
			mcplib.WithObject("params",
				mcplib.Description("Parameter values keyed by name (e.g. {\"status\": \"open\", "+
					"\"rowids\": [3, 7]}). Omit a parameter to leave a pagination control at its default.")),
		),
		runFormulaHandler(formulaSvc, logger),
	)
}

// formulaDisplayProperties is the JSON-schema property map of the `display` object argument. Terse by
// design: structure and enums only, with one-line slot hints — the display semantics live in the manual
// served by get_docs, and the service remains the authoritative validator.
func formulaDisplayProperties() map[string]any {
	slotRef := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	weekday := func(desc string) map[string]any {
		enum := make([]any, 0, len(domain.DisplayWeekdays))
		for _, day := range domain.DisplayWeekdays {
			enum = append(enum, string(day))
		}
		return map[string]any{"type": "string", "enum": enum, "description": desc}
	}
	return map[string]any{
		"mode": map[string]any{
			"type": "string",
			"enum": []any{
				string(domain.DisplayModeTable), string(domain.DisplayModeChecklist), string(domain.DisplayModeCards),
				string(domain.DisplayModeBoard), string(domain.DisplayModeCalendar),
			},
		},
		"columns": map[string]any{
			"type":        "array",
			"description": "Per-column presentation, one entry per result column: a role and/or format, or hidden.",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"column": map[string]any{"type": "string"},
					"role": map[string]any{
						"type": "string",
						"enum": []any{
							string(domain.DisplayRoleTitle), string(domain.DisplayRoleBody),
							string(domain.DisplayRoleCheck), string(domain.DisplayRoleGroup),
							string(domain.DisplayRoleDay),
						},
					},
					"format": map[string]any{
						"type": "string",
						"enum": []any{string(domain.DisplayFormatMarkdown)},
					},
					"hidden": map[string]any{"type": "boolean"},
				},
				"required": []any{"column"},
			},
		},
		"window": map[string]any{
			"type": "object",
			"description": "Calendar only: the days shown. EITHER a weekly window (week_start/week_end weekday " +
				"names, default monday..sunday, week_start alone = a full week from it, repeats every 7 days) OR a " +
				"fixed range (start YYYY-MM-DD + days " +
				"1..31, default 7, steps by its length). Never both.",
			"properties": map[string]any{
				"week_start": weekday("First weekday of a weekly window."),
				"week_end":   weekday("Last weekday of a weekly window; may wrap past week_start."),
				"start": map[string]any{
					"type": "string", "description": "First day of a fixed range, YYYY-MM-DD.",
				},
				"days": map[string]any{
					"type": "integer", "description": "Length of a fixed range in days, 1..31.",
				},
			},
		},
		"slots": map[string]any{
			"type":        "object",
			"description": "Interaction slots, each naming a formula ATTACHED to this view (name or UUID).",
			"properties": map[string]any{
				"add":     slotRef("Insert action; prompt/lookup params only."),
				"toggle":  slotRef("Any-verb action flipping the check-role column; :rowid + optional extras."),
				"edit":    slotRef("Any-verb action with :rowid + at least one prompt/lookup param (seeded form)."),
				"delete":  slotRef("Delete action with :rowid + optional extras."),
				"detail":  slotRef("A :rowid SELECT view rendered on row tap."),
				"reorder": slotRef("Update action with :ordered_rowids, enabling drag-to-reorder."),
				"move": slotRef("Update action with :rowid + one target param: " +
					"option-declaring (board) or date (calendar)."),
				"bulk": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Ordered :rowids actions filling the selection toolbar (table mode only).",
				},
				"row_actions": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
					"description": "Formula refs (name or UUID) giving the per-row menu in order: extra " +
						"single-row actions, plus optionally the formula filling the edit, delete, or detail " +
						"slot, which places that built-in entry (unlisted ones trail as Details, Edit, Delete).",
				},
			},
		},
	}
}

// formulaParamItemSchema is the JSON-schema for one entry of the create / update `params` array. Terse
// by design: structure and enums, plus short hints only where a missing one causes systematic first-shot
// failures — the parameter semantics live in the manual served by get_docs.
func formulaParamItemSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Bind parameter name without the colon (e.g. 'status' for :status).",
			},
			"type": map[string]any{
				"type": "string",
				"enum": []any{
					domain.ParamTypeText, domain.ParamTypeInteger, domain.ParamTypeFloat, domain.ParamTypeBoolean,
					domain.ParamTypeDate, domain.ParamTypeDateTime, domain.ParamTypeTime, domain.ParamTypeGroup,
				},
			},
			"source": map[string]any{
				"type": "string",
				"enum": []any{
					string(domain.ParamSourcePrompt),
					string(domain.ParamSourceSelectionSingle),
					string(domain.ParamSourceSelectionBulk),
					string(domain.ParamSourcePagination),
					string(domain.ParamSourceLookup),
					string(domain.ParamSourceReorder),
					string(domain.ParamSourceWindow),
				},
			},
			"optional": map[string]any{
				"type":        "boolean",
				"description": "The caller may omit the param; it binds 'default' when declared, else SQL NULL.",
			},
			"hint": map[string]any{
				"type":        "string",
				"description": "Display-only input placeholder; never affects binding.",
			},
			"multiline": map[string]any{
				"type":        "boolean",
				"description": "Display-only: renders the input as a textarea (prompt text params only).",
			},
			"inline": map[string]any{
				"type":        "boolean",
				"description": "Display-only: places the param in the add slot's quick-add form.",
			},
			"multiple": map[string]any{
				"type": "boolean",
				"description": "Multi-select (a lookup, or a prompt param with options): the run value is an " +
					"array binding as ONE JSON TEXT value.",
			},
			"options": map[string]any{
				"type": "array",
				"items": map[string]any{"anyOf": []any{
					map[string]any{"type": "string"},
					map[string]any{"type": "number"},
					map[string]any{
						"type": "object",
						"properties": map[string]any{
							"value": map[string]any{},
							"label": map[string]any{"type": "string"},
						},
						"required": []any{"value"},
					},
				}},
				"description": "Closed, ENFORCED vocabulary for a prompt param; runs pass values, never labels.",
			},
			"default": map[string]any{
				"type": "string",
				"description": "Value an omitted optional param binds, in string form, coerced to the type; on a " +
					"multi-select only the literal 'all'.",
			},
			"defaults": map[string]any{
				"type": "array",
				"items": map[string]any{"anyOf": []any{
					map[string]any{"type": "string"}, map[string]any{"type": "number"},
				}},
				"description": "A multi-select's declared subset — the values an omitted param binds.",
			},
			"when": map[string]any{
				"type": "object",
				"description": "Conditional visibility: the field renders only while the sibling `param` " +
					"compares true against `value` under `op` (default eq).",
				"properties": map[string]any{
					"param": map[string]any{"type": "string"},
					"op":    map[string]any{"type": "string", "enum": []any{"eq", "ne", "lt", "lte", "gt", "gte"}},
					"value": map[string]any{"type": "string"},
				},
				"required": []any{"param", "value"},
			},
			"lookup": map[string]any{
				"type": "object",
				"description": "Required for source 'lookup': a picker over `table` — label_column shown, " +
					"value_column bound.",
				"properties": map[string]any{
					"table":             map[string]any{"type": "string"},
					"label_column":      map[string]any{"type": "string"},
					"value_column":      map[string]any{"type": "string"},
					"exclude_selection": map[string]any{"type": "boolean"},
					"allow_new":         map[string]any{"type": "boolean"},
					"scope": map[string]any{
						"type":                 "object",
						"additionalProperties": map[string]any{"type": "string"},
					},
					"fill": map[string]any{
						"type":                 "object",
						"additionalProperties": map[string]any{"type": "string"},
					},
				},
				"required": []any{"table", "label_column", "value_column"},
			},
			"fields": map[string]any{
				"type":        "array",
				"description": "Group-only: the per-row schema — scalar prompt/lookup entries, no nested groups.",
				"items":       map[string]any{"type": "object"},
			},
		},
		"required": []any{"name", "type", "source"},
	}
}

// formulaAuthoringErrors lists the validation sentinels a formula author can fix by following the
// authoring rules — the failures worth pointing at the doc. Authorization, not-found, and
// infrastructure errors stay out: no documentation fixes those.
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

// formulaErrorResult wraps a create/update failure as a tool error, appending a get_docs pointer to
// authoring-validation rejections so the manual stays discoverable even when it was skipped.
func formulaErrorResult(action string, err error) *mcplib.CallToolResult {
	msg := fmt.Sprintf("failed to %s formula: %v", action, err)
	if slices.ContainsFunc(formulaAuthoringErrors, func(target error) bool { return errors.Is(err, target) }) {
		msg += ". Call " + mcputil.ToolGetDocs + "(topic: 'formulas') for the authoring rules."
	}
	return mcplib.NewToolResultError(msg)
}

func createFormulaHandler(formulaSvc *service.FormulaService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.create_formula")
		defer span.End()

		userID, err := parseUserID(ctx)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		bdbID, err := parseBdbID(request)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}

		args := request.GetArguments()
		name, _ := args["name"].(string)
		label, _ := args["label"].(string)
		description, _ := args["description"].(string)
		sql, _ := args["sql"].(string)

		input := service.FormulaInput{
			Name:        name,
			Label:       label,
			Description: description,
			SQL:         sql,
		}
		params, perr := parseParamSchema(args["params"])
		if perr != nil {
			return mcplib.NewToolResultError(perr.Error()), nil
		}
		input.Params = params
		if v, ok := args["attached_to_formula_id"].(string); ok && v != "" {
			id, perr := uuid.Parse(v)
			if perr != nil {
				return mcplib.NewToolResultError("invalid attached_to_formula_id"), nil
			}
			input.AttachedToFormulaID = &id
		}
		if v, ok := args["display_order"].(float64); ok {
			n := int(v)
			input.DisplayOrder = &n
		}
		if dm, ok := args["display"].(map[string]any); ok {
			input.Display = parseDisplay(dm)
		}
		if v, ok := args["hidden"].(bool); ok {
			input.Hidden = v
		}

		formula, err := formulaSvc.CreateFormula(ctx, userID, bdbID, input)
		if err != nil {
			logger.Error().Err(err).
				Str("user_id", userID.String()).Str("bdb_id", bdbID.String()).
				Str("name", name).Msg("MCP create_formula failed")
			return formulaErrorResult("create", err), nil
		}
		return marshalToJSON(toFormulaDetail(formula))
	}
}

func updateFormulaHandler(formulaSvc *service.FormulaService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.update_formula")
		defer span.End()

		userID, err := parseUserID(ctx)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		bdbID, err := parseBdbID(request)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}

		args := request.GetArguments()
		ref, _ := args["ref"].(string)
		if ref == "" {
			return mcplib.NewToolResultError("ref is required"), nil
		}

		patch := service.FormulaPatch{}
		if v, ok := args["name"].(string); ok && v != "" {
			patch.Name = &v
		}
		// Empty is meaningful for the label, unlike the fields around it: it clears the label back to the name.
		if v, ok := args["label"].(string); ok {
			patch.Label = &v
		}
		if v, ok := args["description"].(string); ok && v != "" {
			patch.Description = &v
		}
		if v, ok := args["sql"].(string); ok && v != "" {
			patch.SQL = &v
		}
		if raw, ok := args["params"]; ok && raw != nil {
			parsed, perr := parseParamSchema(raw)
			if perr != nil {
				return mcplib.NewToolResultError(perr.Error()), nil
			}
			patch.Params = &parsed
		}
		if v, ok := args["display_order"].(float64); ok {
			n := int(v)
			patch.DisplayOrder = &n
		}
		// Display is tri-state: absent = unchanged, explicit null = clear, object = replace.
		if raw, exists := args["display"]; exists {
			switch dm := raw.(type) {
			case nil:
				patch.ClearDisplay = true
			case map[string]any:
				patch.Display = parseDisplay(dm)
			default:
				return mcplib.NewToolResultError("display must be an object or null"), nil
			}
		}
		if v, ok := args["hidden"].(bool); ok {
			patch.Hidden = &v
		}

		formula, err := formulaSvc.UpdateFormula(ctx, userID, bdbID, ref, patch)
		if err != nil {
			logger.Error().Err(err).
				Str("user_id", userID.String()).Str("bdb_id", bdbID.String()).
				Str("ref", ref).Msg("MCP update_formula failed")
			return formulaErrorResult("update", err), nil
		}
		return marshalToJSON(toFormulaDetail(formula))
	}
}

func getFormulaHandler(formulaSvc *service.FormulaService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.get_formula")
		defer span.End()

		userID, err := parseUserID(ctx)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		bdbID, err := parseBdbID(request)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}

		args := request.GetArguments()
		ref, _ := args["ref"].(string)
		if ref == "" {
			return mcplib.NewToolResultError("ref is required"), nil
		}

		formula, err := formulaSvc.GetFormula(ctx, userID, bdbID, ref)
		if err != nil {
			logger.Error().Err(err).
				Str("user_id", userID.String()).Str("bdb_id", bdbID.String()).
				Str("ref", ref).Msg("MCP get_formula failed")
			return mcplib.NewToolResultError(fmt.Sprintf("failed to fetch formula: %v", err)), nil
		}
		return marshalToJSON(toFormulaDetail(formula))
	}
}

func listFormulasHandler(formulaSvc *service.FormulaService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.list_formulas")
		defer span.End()

		userID, err := parseUserID(ctx)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		bdbID, err := parseBdbID(request)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}

		formulas, err := formulaSvc.ListFormulasIncludingHidden(ctx, userID, bdbID)
		if err != nil {
			logger.Error().Err(err).
				Str("user_id", userID.String()).Str("bdb_id", bdbID.String()).
				Msg("MCP list_formulas failed")
			return mcplib.NewToolResultError(fmt.Sprintf("failed to list formulas: %v", err)), nil
		}
		return marshalToJSON(toFormulaDetails(formulas))
	}
}

func deleteFormulaHandler(formulaSvc *service.FormulaService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.delete_formula")
		defer span.End()

		userID, err := parseUserID(ctx)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		bdbID, err := parseBdbID(request)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}

		args := request.GetArguments()
		ref, _ := args["ref"].(string)
		if ref == "" {
			return mcplib.NewToolResultError("ref is required"), nil
		}

		if err := formulaSvc.DeleteFormula(ctx, userID, bdbID, ref); err != nil {
			logger.Error().Err(err).
				Str("user_id", userID.String()).Str("bdb_id", bdbID.String()).
				Str("ref", ref).Msg("MCP delete_formula failed")
			return mcplib.NewToolResultError(fmt.Sprintf("failed to delete formula: %v", err)), nil
		}
		return mcplib.NewToolResultText(fmt.Sprintf("formula %q deleted", ref)), nil
	}
}

func runFormulaHandler(formulaSvc *service.FormulaService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.run_formula")
		defer span.End()

		userID, err := parseUserID(ctx)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		bdbID, err := parseBdbID(request)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}

		args := request.GetArguments()
		ref, _ := args["ref"].(string)
		if ref == "" {
			return mcplib.NewToolResultError("ref is required"), nil
		}

		result, err := formulaSvc.RunFormula(ctx, userID, bdbID, ref, buildFormulaRunInput(args))
		if err != nil {
			logger.Error().Err(err).
				Str("user_id", userID.String()).Str("bdb_id", bdbID.String()).
				Str("ref", ref).Msg("MCP run_formula failed")
			return mcplib.NewToolResultError(fmt.Sprintf("failed to run formula: %v", err)), nil
		}
		return marshalToJSON(toFormulaRunResult(result))
	}
}

// parseParamSchema converts the params arg-list (a []any of {name, type, source} objects) into a
// domain.ParamSchema. Malformed entries are skipped and the service validates the resulting schema;
// the one shape reported here is a default of a type the schema cannot mean (see parseDefault), since
// silently dropping it would store a param with no default at all.
func parseParamSchema(raw any) (domain.ParamSchema, error) {
	arr, ok := raw.([]any)
	if !ok {
		return domain.ParamSchema{}, nil
	}
	out := make(domain.ParamSchema, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		fp, err := parseParam(m)
		if err != nil {
			return nil, err
		}
		out = append(out, fp)
	}
	return out, nil
}

// parseParam converts one raw params entry — group fields included — into a domain parameter.
func parseParam(m map[string]any) (domain.FormulaParam, error) {
	name, _ := m["name"].(string)
	typ, _ := m["type"].(string)
	source, _ := m["source"].(string)
	optional, _ := m["optional"].(bool)
	var lookup *domain.LookupConfig
	if lm, ok := m["lookup"].(map[string]any); ok {
		table, _ := lm["table"].(string)
		labelColumn, _ := lm["label_column"].(string)
		valueColumn, _ := lm["value_column"].(string)
		excludeSelection, _ := lm["exclude_selection"].(bool)
		allowNew, _ := lm["allow_new"].(bool)
		var scope map[string]string
		if sm, ok := lm["scope"].(map[string]any); ok {
			scope = make(map[string]string, len(sm))
			for sibling, col := range sm {
				if c, ok := col.(string); ok {
					scope[sibling] = c
				}
			}
		}
		var fill map[string]string
		if fm, ok := lm["fill"].(map[string]any); ok {
			fill = make(map[string]string, len(fm))
			for sibling, col := range fm {
				if c, ok := col.(string); ok {
					fill[sibling] = c
				}
			}
		}
		lookup = &domain.LookupConfig{
			Table: table, LabelColumn: labelColumn, ValueColumn: valueColumn, ExcludeSelection: excludeSelection,
			AllowNew: allowNew, Scope: scope, Fill: fill,
		}
	}
	hint, _ := m["hint"].(string)
	multiline, _ := m["multiline"].(bool)
	inline, _ := m["inline"].(bool)
	multiple, _ := m["multiple"].(bool)
	def, err := parseDefault(m["default"])
	if err != nil {
		return domain.FormulaParam{}, fmt.Errorf("parameter %q: %w", name, err)
	}
	defaults, err := parseDefaults(m["defaults"])
	if err != nil {
		return domain.FormulaParam{}, fmt.Errorf("parameter %q: %w", name, err)
	}
	var options []domain.FormulaOption
	if raw, ok := m["options"].([]any); ok {
		for _, o := range raw {
			if opt, ok := parseOption(o); ok {
				options = append(options, opt)
			}
		}
	}
	var when *domain.ParamWhen
	if wm, ok := m["when"].(map[string]any); ok {
		param, _ := wm["param"].(string)
		op, _ := wm["op"].(string)
		value, _ := wm["value"].(string)
		when = &domain.ParamWhen{Param: param, Op: op, Value: value}
	}
	fp := domain.FormulaParam{
		Name: name, Type: typ, Source: domain.ParamSource(source), Optional: optional, Lookup: lookup, Hint: hint,
		Multiline: multiline, Inline: inline, Multiple: multiple, Options: options, Default: def, Defaults: defaults,
		When: when,
	}
	if fields, ok := m["fields"].([]any); ok {
		for _, field := range fields {
			fm, ok := field.(map[string]any)
			if !ok {
				continue
			}
			sub, err := parseParam(fm)
			if err != nil {
				return domain.FormulaParam{}, err
			}
			fp.Fields = append(fp.Fields, sub)
		}
	}
	return fp, nil
}

// parseDefault reads a param's default: a string (a scalar default, or the "all" literal) or nothing.
// Any other shape is an error rather than a silent drop — a multi-select's subset goes in defaults.
func parseDefault(raw any) (string, error) {
	switch v := raw.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	default:
		return "", errors.New("default must be a string — a multi-select's subset goes in defaults")
	}
}

// parseDefaults reads a multi-select's declared subset: an array of strings and numbers, each kept in
// its string form, or nothing. Any other shape is an error rather than a silent drop.
func parseDefaults(raw any) ([]string, error) {
	switch v := raw.(type) {
	case nil:
		return nil, nil
	case []any:
		values := make([]string, 0, len(v))
		for _, el := range v {
			s, ok := optionScalarString(el)
			if !ok {
				return nil, errors.New("a defaults entry must be a string or a number")
			}
			values = append(values, s)
		}
		return values, nil
	default:
		return nil, errors.New("defaults must be an array of values")
	}
}

// parseOption converts one raw options entry into a domain option: a bare string or number means
// value = label, a {value, label} object carries both (label folding to the value when absent, a
// numeric value to its literal string form). ok is false for any other shape — the entry is skipped
// and the service's schema validation reports what remains.
func parseOption(raw any) (domain.FormulaOption, bool) {
	if s, ok := optionScalarString(raw); ok {
		return domain.FormulaOption{Value: s, Label: s}, true
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return domain.FormulaOption{}, false
	}
	value, ok := optionScalarString(m["value"])
	if !ok {
		return domain.FormulaOption{}, false
	}
	label, _ := m["label"].(string)
	if label == "" {
		label = value
	}
	return domain.FormulaOption{Value: value, Label: label}, true
}

// optionScalarString renders a JSON-decoded string or number as its string form (a whole-valued
// number without a decimal point); ok is false for any other type.
func optionScalarString(raw any) (string, bool) {
	switch v := raw.(type) {
	case string:
		return v, true
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), true
	default:
		return "", false
	}
}

// parseDisplay converts the display arg object into the domain config. Unknown or mistyped fields
// are zero-valued; the service validates the result.
func parseDisplay(m map[string]any) *domain.FormulaDisplay {
	d := &domain.FormulaDisplay{}
	if v, ok := m["mode"].(string); ok {
		d.Mode = domain.DisplayMode(v)
	}
	if cols, ok := m["columns"].([]any); ok {
		for _, col := range cols {
			cm, ok := col.(map[string]any)
			if !ok {
				continue
			}
			entry := domain.FormulaDisplayColumn{}
			entry.Column, _ = cm["column"].(string)
			if v, ok := cm["role"].(string); ok {
				entry.Role = domain.DisplayColumnRole(v)
			}
			if v, ok := cm["format"].(string); ok {
				entry.Format = domain.DisplayColumnFormat(v)
			}
			if v, ok := cm["hidden"].(bool); ok {
				entry.Hidden = v
			}
			d.Columns = append(d.Columns, entry)
		}
	}
	if sm, ok := m["slots"].(map[string]any); ok {
		d.Slots.Add, _ = sm["add"].(string)
		d.Slots.Toggle, _ = sm["toggle"].(string)
		d.Slots.Edit, _ = sm["edit"].(string)
		d.Slots.Delete, _ = sm["delete"].(string)
		d.Slots.Detail, _ = sm["detail"].(string)
		d.Slots.Reorder, _ = sm["reorder"].(string)
		d.Slots.Move, _ = sm["move"].(string)
		d.Slots.Bulk = parseRefList(sm["bulk"])
		d.Slots.RowActions = parseRefList(sm["row_actions"])
	}
	if wm, ok := m["window"].(map[string]any); ok {
		w := &domain.FormulaDisplayWindow{}
		if v, ok := wm["week_start"].(string); ok {
			w.WeekStart = domain.DisplayWeekday(v)
		}
		if v, ok := wm["week_end"].(string); ok {
			w.WeekEnd = domain.DisplayWeekday(v)
		}
		w.Start, _ = wm["start"].(string)
		if v, ok := wm["days"].(float64); ok {
			w.Days = int(v)
		}
		d.Window = w
	}
	return d
}

// parseRefList reads an ordered list of formula refs from a raw JSON array, dropping non-string
// and empty entries.
func parseRefList(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range items {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// buildFormulaRunInput converts the raw run_formula MCP arguments into a service.FormulaRunInput: the
// "params" object passes through as Params — every declared parameter value keyed by name, which the
// run engine validates and coerces against the stored schema.
func buildFormulaRunInput(args map[string]any) service.FormulaRunInput {
	params, _ := args["params"].(map[string]any)
	return service.FormulaRunInput{Params: params}
}
