package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	"github.com/alexandremgo/baguettedb/pkg/databases/appinput"
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

		var req appinput.CreateFormula
		if err := request.BindArguments(&req); err != nil {
			return mcplib.NewToolResultError("invalid arguments: " + err.Error()), nil
		}
		input, err := req.Input()
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}

		formula, err := formulaSvc.CreateFormula(ctx, userID, bdbID, input)
		if err != nil {
			logger.Error().Err(err).
				Str("user_id", userID.String()).Str("bdb_id", bdbID.String()).
				Str("name", input.Name).Msg("MCP create_formula failed")
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

		var req appinput.UpdateFormula
		if err := request.BindArguments(&req); err != nil {
			return mcplib.NewToolResultError("invalid arguments: " + err.Error()), nil
		}
		patch, err := req.Patch()
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
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

// buildFormulaRunInput converts the raw run_formula MCP arguments into a service.FormulaRunInput: the
// "params" object passes through as Params — every declared parameter value keyed by name, which the
// run engine validates and coerces against the stored schema.
func buildFormulaRunInput(args map[string]any) service.FormulaRunInput {
	params, _ := args["params"].(map[string]any)
	return service.FormulaRunInput{Params: params}
}
