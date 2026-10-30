package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"

	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

var tracer = otel.Tracer("baguettedb/databases/mcp")

// registerTools registers all database query tools on the MCP server.
func registerTools(
	s *mcpserver.MCPServer,
	querySvc *service.QueryService,
	skillSvc *service.SkillService,
	formulaSvc *service.FormulaService,
	docsSvc *service.DocsService,
	logger zerolog.Logger,
) {
	s.AddTool(
		mcplib.NewTool(mcputil.ToolDescribeTable,
			mcplib.WithDescription(
				"Describes one table: columns, foreign keys, CHECK constraints, kind (table / view / virtual / "+
					"shadow, with the virtual-table module, e.g. fts5), plus the full bodies of skills targeting it. "+
					mcputil.ToolDescribeBdb+" normally returns all of this for every table at once — "+
					"reach for this tool when a "+mcputil.ToolDescribeBdb+" response's `omitted` note "+
					"says schemas or skill bodies were dropped, or to re-read one table without "+
					"re-fetching the whole bdb."),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
			mcplib.WithString("table_name", mcplib.Required(), mcplib.Description("Name of the table to describe")),
		),
		describeTableHandler(querySvc, skillSvc, logger),
	)

	registerSkillTools(s, skillSvc, logger)
	registerFormulaTools(s, formulaSvc, logger)

	s.AddTool(
		mcplib.NewTool(mcputil.ToolExecuteQuery,
			mcplib.WithDescription(
				"Executes a single read-only SELECT statement. "+
					"Multi-statement input is not supported. Results are capped: a larger result is refused with the "+
					"limit named in the error, so add a LIMIT or aggregate for big tables. The full JSON1 function "+
					"set, JSONB, and FTS5 are built in — engine capabilities, rules and your limits in "+
					mcputil.ToolGetDocs+"(topic: 'sql')."),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
			mcplib.WithString("sql", mcplib.Required(), mcplib.Description("Single SELECT statement to execute")),
		),
		executeQueryHandler(querySvc, logger),
	)

	s.AddTool(
		mcplib.NewTool(mcputil.ToolExecuteData,
			mcplib.WithDescription(
				"Writes data: executes one or more DML statements (INSERT, UPDATE, DELETE) atomically. Multiple "+
					"statements separated by ';' run in a single transaction; if any fails the whole request is "+
					"rolled back. All statements share one batch_id and produce a single undo entry. The statement "+
					"and affected-row caps are listed in "+mcputil.ToolGetDocs+"(topic: 'sql') and named in a "+
					"refusal. For schema changes use "+
					mcputil.ToolExecuteSchema+"; for read-only queries use "+mcputil.ToolExecuteQuery+". JSON "+
					"values bind and store as TEXT, with the full JSON1 set usable in DML. BLOB values cannot be "+
					"stored (keep binary as base64 or hex text) — engine capabilities and rules in "+
					mcputil.ToolGetDocs+"(topic: 'sql')."),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
			mcplib.WithString("sql", mcplib.Required(),
				mcplib.Description("DML statement(s) to execute; multiple separated by ';'")),
		),
		executeDataHandler(querySvc, logger),
	)

	s.AddTool(
		mcplib.NewTool(mcputil.ToolListChanges,
			mcplib.WithDescription(
				"Lists recent changes (data and schema) in this bdb, grouped by batch. By default returns "+
					"changes from all users; pass user_id to scope to one author, kind ('DML' or 'DDL') to filter "+
					"by category, or table_name to filter to a single table (matches DML target tables and DDL "+
					"object names). Each entry carries id (change_log_id), batch_id, and kind; pass id or "+
					"batch_id to revert_change to undo a specific change. Entries written by undo, redo, or revert "+
					"record the statement that ran (a column restored by undo appears as its ADD COLUMN). "+
					"Note: virtual tables (e.g. FTS5) are captured only at the DDL level — direct row writes bypass "+
					"the change log, and undo of DROP returns an empty vtable to be rebuilt from the source."),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
			mcplib.WithString("kind",
				mcplib.Description("'DML' for data changes, 'DDL' for schema changes; omit to return both")),
			mcplib.WithString("table_name",
				mcplib.Description("Filter to a single table (DML target or DDL object name)")),
			mcplib.WithString("user_id",
				mcplib.Description("UUID — scopes results to changes by this user; omit to return all users")),
			mcplib.WithString("limit",
				mcplib.Description("Max number of entries to return (default 20, max 100)")),
		),
		listChangesHandler(querySvc, logger),
	)

	s.AddTool(
		mcplib.NewTool(mcputil.ToolUndoLastChange,
			mcplib.WithDescription(
				"Reverses your most recent change in this bdb, data or schema; other members' history is "+
					"untouched. Call repeatedly to walk back further. The undone batch moves to your redo stack "+
					"("+mcputil.ToolRedoLastChange+"). To reverse a specific entry by anyone, use "+
					mcputil.ToolRevertChange+"."),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
		),
		undoLastChangeHandler(querySvc, logger),
	)

	s.AddTool(
		mcplib.NewTool(mcputil.ToolRedoLastChange,
			mcplib.WithDescription(
				"Re-applies your most recently undone change in this bdb (data or schema). Any new change "+
					"you make clears your redo stack; other members' activity does not."),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
		),
		redoLastChangeHandler(querySvc, logger),
	)

	s.AddTool(
		mcplib.NewTool(mcputil.ToolRevertChange,
			mcplib.WithDescription(
				"Reverts a specific change (data or schema) by executing its inverse. Targets any user's "+
					"change in this bdb — call "+mcputil.ToolListChanges+" first to find the change_log_id "+
					"or batch_id. "+
					"Pass either a single change_log_id or a batch_id to revert all entries in that batch. The "+
					"revert lands on your own undo stack, so you can undo it via "+mcputil.ToolUndoLastChange+"."),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
			mcplib.WithString("change_log_id",
				mcplib.Description("Change log entry ID to revert (provide this OR batch_id)")),
			mcplib.WithString("batch_id",
				mcplib.Description("Batch ID to revert all entries from a single change (provide this OR "+
					"change_log_id)")),
		),
		revertChangeHandler(querySvc, logger),
	)

	s.AddTool(
		mcplib.NewTool(mcputil.ToolUndoLastChangeAnyone,
			mcplib.WithDescription(
				"Owner-only: reverses the most recent change in this bdb regardless of who made it (data or "+
					"schema). The reversed batch lands on YOUR redo stack "+
					"("+mcputil.ToolRedoLastChangeAnyone+"). Inspect "+
					"the timeline with "+mcputil.ToolListChanges+" first."),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
		),
		undoLastChangeAnyoneHandler(querySvc, logger),
	)

	s.AddTool(
		mcplib.NewTool(mcputil.ToolRedoLastChangeAnyone,
			mcplib.WithDescription(
				"Owner-only: re-applies the most recently undone change in this bdb regardless of who undid "+
					"it. The re-applied batch lands on YOUR undo stack."),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
		),
		redoLastChangeAnyoneHandler(querySvc, logger),
	)

	s.AddTool(
		mcplib.NewTool(mcputil.ToolExecuteSchema,
			mcplib.WithDescription(
				"Executes one or more DDL statements (CREATE/ALTER/DROP TABLE/INDEX/VIEW/TRIGGER) "+
					"atomically. Multiple statements separated by ';' run in a single transaction; "+
					"if any fails, all are rolled back. The statement cap is listed in "+mcputil.ToolGetDocs+
					"(topic: 'sql'). "+
					"Every created or renamed-to name (tables, columns, indexes, views, triggers) "+
					"must start with a letter and contain only ASCII letters, digits, and "+
					"underscores, at most 128 chars. SQL keywords (e.g. 'order') are allowed but "+
					"best avoided — every later query has to double-quote them. "+
					"Note: ALTER TABLE RENAME TO is single-statement only, and statements that "+
					"reference objects created by an earlier statement in the same request (e.g. "+
					"CREATE TABLE foo; CREATE INDEX ON foo) must be split into separate requests. "+
					"Declare JSON-holding columns TEXT with CHECK (json_valid(col)); expression indexes on "+
					"json_extract(...) are supported. BLOB columns can be declared but cannot hold BLOB values "+
					"(binary belongs in TEXT as base64 or hex) — engine capabilities and rules in "+
					mcputil.ToolGetDocs+"(topic: 'sql')."),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
			mcplib.WithString("sql", mcplib.Required(),
				mcplib.Description("DDL statement(s) to execute; multiple separated by ';'")),
		),
		executeSchemaHandler(querySvc, logger),
	)

	registerDocsTool(s, docsSvc)
}

func describeTableHandler(
	querySvc *service.QueryService,
	skillSvc *service.SkillService,
	logger zerolog.Logger,
) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.describe_table")
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
		tableName, _ := args["table_name"].(string)
		if tableName == "" {
			return mcplib.NewToolResultError("table_name is required"), nil
		}

		result, err := querySvc.DescribeTable(ctx, userID, bdbID, tableName)
		if err != nil {
			logger.Error().Err(err).
				Str("user_id", userID.String()).Str("bdb_id", bdbID.String()).
				Str("table", tableName).Msg("MCP describe_table failed")
			return mcplib.NewToolResultError(fmt.Sprintf("failed to describe table: %v", err)), nil
		}

		skills, err := skillSvc.ListSkillsForTable(ctx, userID, bdbID, tableName)
		if err != nil {
			logger.Error().Err(err).
				Str("user_id", userID.String()).Str("bdb_id", bdbID.String()).
				Str("table", tableName).
				Msg("MCP describe_table: failed to load skills, returning schema-only")
			skills = nil
		}

		return marshalToJSON(toTableDescription(result, skills))
	}
}

func executeQueryHandler(querySvc *service.QueryService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.execute_query")
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
		sql, _ := args["sql"].(string)
		if sql == "" {
			return mcplib.NewToolResultError("sql is required"), nil
		}

		result, err := querySvc.Execute(ctx, userID, bdbID, sql, nil, domain.IntentQuery)
		if err != nil {
			logger.Error().Err(err).
				Str("user_id", userID.String()).Str("bdb_id", bdbID.String()).
				Msg("MCP execute_query failed")
			return executeErrorResult("query execution failed", err), nil
		}

		return marshalToJSON(toQueryResult(result))
	}
}

func executeDataHandler(querySvc *service.QueryService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.execute_data")
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
		sql, _ := args["sql"].(string)
		if sql == "" {
			return mcplib.NewToolResultError("sql is required"), nil
		}

		result, err := querySvc.Execute(ctx, userID, bdbID, sql, nil, domain.IntentExecute)
		if err != nil {
			logger.Error().Err(err).
				Str("user_id", userID.String()).Str("bdb_id", bdbID.String()).
				Msg("MCP execute_data failed")
			return executeErrorResult("mutation execution failed", err), nil
		}

		return marshalToJSON(toQueryResult(result))
	}
}

func executeSchemaHandler(querySvc *service.QueryService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.execute_schema")
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
		sql, _ := args["sql"].(string)
		if sql == "" {
			return mcplib.NewToolResultError("sql is required"), nil
		}

		result, err := querySvc.Execute(ctx, userID, bdbID, sql, nil, domain.IntentSchema)
		if err != nil {
			logger.Error().Err(err).
				Str("user_id", userID.String()).Str("bdb_id", bdbID.String()).
				Msg("MCP execute_schema failed")
			return executeErrorResult("schema execution failed", err), nil
		}

		return marshalToJSON(toQueryResult(result))
	}
}

func listChangesHandler(querySvc *service.QueryService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.list_changes")
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
		tableName, _ := args["table_name"].(string)

		kindArg, _ := args["kind"].(string)
		var kind domain.ChangeKind
		switch kindArg {
		case "", "DML", "DDL":
			kind = domain.ChangeKind(kindArg)
		default:
			return mcplib.NewToolResultError("kind must be 'DML', 'DDL', or omitted"), nil
		}

		var filterUserID *uuid.UUID
		if raw, ok := args["user_id"].(string); ok && raw != "" {
			parsed, parseErr := uuid.Parse(raw)
			if parseErr != nil {
				return mcplib.NewToolResultError(fmt.Sprintf("invalid user_id: %v", parseErr)), nil
			}
			filterUserID = &parsed
		}

		limit := 20
		if limitStr, ok := args["limit"].(string); ok && limitStr != "" {
			if parsed, parseErr := strconv.Atoi(limitStr); parseErr == nil {
				limit = parsed
			}
		}

		result, err := querySvc.ListChanges(ctx, userID, bdbID, kind, tableName, filterUserID, limit)
		if err != nil {
			logger.Error().Err(err).Str("user_id", userID.String()).Str("bdb_id", bdbID.String()).
				Msg("MCP list_changes failed")
			return mcplib.NewToolResultError(fmt.Sprintf("list changes failed: %v", err)), nil
		}

		return marshalToJSON(toListChanges(result))
	}
}

func undoLastChangeHandler(querySvc *service.QueryService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.undo_last_change")
		defer span.End()

		userID, err := parseUserID(ctx)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}

		bdbID, err := parseBdbID(request)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}

		result, err := querySvc.UndoLastChange(ctx, userID, bdbID)
		if err != nil {
			if conflictErr, ok := errors.AsType[*service.ConflictError](err); ok {
				return conflictToolResult(conflictErr.Conflict), nil
			}
			logger.Error().Err(err).
				Str("user_id", userID.String()).Str("bdb_id", bdbID.String()).
				Msg("MCP undo_last_mutation failed")
			return mcplib.NewToolResultError(fmt.Sprintf("undo failed: %v", err)), nil
		}

		return marshalToJSON(toQueryResult(result))
	}
}

func redoLastChangeHandler(querySvc *service.QueryService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.redo_last_change")
		defer span.End()

		userID, err := parseUserID(ctx)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}

		bdbID, err := parseBdbID(request)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}

		result, err := querySvc.RedoLastChange(ctx, userID, bdbID)
		if err != nil {
			if conflictErr, ok := errors.AsType[*service.ConflictError](err); ok {
				return conflictToolResult(conflictErr.Conflict), nil
			}
			logger.Error().Err(err).
				Str("user_id", userID.String()).Str("bdb_id", bdbID.String()).
				Msg("MCP redo_last_mutation failed")
			return mcplib.NewToolResultError(fmt.Sprintf("redo failed: %v", err)), nil
		}

		return marshalToJSON(toQueryResult(result))
	}
}

func undoLastChangeAnyoneHandler(querySvc *service.QueryService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.undo_last_change_anyone")
		defer span.End()

		userID, err := parseUserID(ctx)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}

		bdbID, err := parseBdbID(request)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}

		result, err := querySvc.UndoLastChangeAnyone(ctx, userID, bdbID)
		if err != nil {
			if conflictErr, ok := errors.AsType[*service.ConflictError](err); ok {
				return conflictToolResult(conflictErr.Conflict), nil
			}
			logger.Error().Err(err).
				Str("user_id", userID.String()).Str("bdb_id", bdbID.String()).
				Msg("MCP undo_last_change_anyone failed")
			return mcplib.NewToolResultError(fmt.Sprintf("undo-anyone failed: %v", err)), nil
		}

		return marshalToJSON(toQueryResult(result))
	}
}

func redoLastChangeAnyoneHandler(querySvc *service.QueryService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.redo_last_change_anyone")
		defer span.End()

		userID, err := parseUserID(ctx)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}

		bdbID, err := parseBdbID(request)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}

		result, err := querySvc.RedoLastChangeAnyone(ctx, userID, bdbID)
		if err != nil {
			if conflictErr, ok := errors.AsType[*service.ConflictError](err); ok {
				return conflictToolResult(conflictErr.Conflict), nil
			}
			logger.Error().Err(err).
				Str("user_id", userID.String()).Str("bdb_id", bdbID.String()).
				Msg("MCP redo_last_change_anyone failed")
			return mcplib.NewToolResultError(fmt.Sprintf("redo-anyone failed: %v", err)), nil
		}

		return marshalToJSON(toQueryResult(result))
	}
}

func revertChangeHandler(querySvc *service.QueryService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.revert_change")
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
		changeLogIDStr, _ := args["change_log_id"].(string)
		batchID, _ := args["batch_id"].(string)

		// Routes to single-entry or batch revert based on which parameter is provided.
		switch {
		case changeLogIDStr != "" && batchID != "":
			return mcplib.NewToolResultError("provide either change_log_id or batch_id, not both"), nil

		case changeLogIDStr != "":
			changeLogID, parseErr := strconv.ParseInt(changeLogIDStr, 10, 64)
			if parseErr != nil {
				return mcplib.NewToolResultError("invalid change_log_id"), nil
			}
			result, revertErr := querySvc.RevertChange(ctx, userID, bdbID, changeLogID)
			if revertErr != nil {
				if conflictErr, ok := errors.AsType[*service.ConflictError](revertErr); ok {
					return conflictToolResult(conflictErr.Conflict), nil
				}
				logger.Error().Err(revertErr).
					Str("user_id", userID.String()).Str("bdb_id", bdbID.String()).
					Int64("change_log_id", changeLogID).Msg("MCP revert_change failed")
				return mcplib.NewToolResultError(fmt.Sprintf("revert failed: %v", revertErr)), nil
			}
			return marshalToJSON(toQueryResult(result))

		case batchID != "":
			result, revertErr := querySvc.RevertChangeBatch(ctx, userID, bdbID, batchID)
			if revertErr != nil {
				if conflictErr, ok := errors.AsType[*service.ConflictError](revertErr); ok {
					return conflictToolResult(conflictErr.Conflict), nil
				}
				logger.Error().Err(revertErr).
					Str("user_id", userID.String()).Str("bdb_id", bdbID.String()).
					Str("batch_id", batchID).Msg("MCP revert_change (batch) failed")
				return mcplib.NewToolResultError(fmt.Sprintf("revert failed: %v", revertErr)), nil
			}
			return marshalToJSON(toQueryResult(result))

		default:
			return mcplib.NewToolResultError("change_log_id or batch_id is required"), nil
		}
	}
}

// parseUserID extracts and parses the user ID from the MCP context.
func parseUserID(ctx context.Context) (uuid.UUID, error) {
	sub, ok := mcputil.UserIDFromContext(ctx)
	if !ok {
		return uuid.Nil, errors.New("missing user identity")
	}

	userID, err := uuid.Parse(sub)
	if err != nil {
		return uuid.Nil, errors.New("invalid user identity")
	}

	return userID, nil
}

// executeErrorResult returns the tool error for a failed execute_* call: failure (e.g. "query execution failed") and
// its cause, with advice naming the tool to use when the statement's kind or its parameters are the cause.
func executeErrorResult(failure string, err error) *mcplib.CallToolResult {
	if refused, ok := errors.AsType[*domain.StatementKindError](err); ok {
		var advice string
		switch {
		case refused.Count > 1:
			advice = "send one statement per " + mcputil.ToolExecuteQuery + " call"
		case refused.Kind == domain.StatementDQL:
			advice = "use " + mcputil.ToolExecuteQuery + " for it"
		case refused.Kind == domain.StatementDML:
			advice = "use " + mcputil.ToolExecuteData + " for it"
		case refused.Kind == domain.StatementDDL:
			advice = "use " + mcputil.ToolExecuteSchema + " for it"
		default:
			advice = "no tool runs it"
		}
		return mcplib.NewToolResultError(fmt.Sprintf("%s: %v; %s", failure, refused, advice))
	}
	if errors.Is(err, domain.ErrMissingParameters) {
		return mcplib.NewToolResultError(fmt.Sprintf("%s: %v; this tool takes none, so write the values in the SQL, "+
			"or save it as a formula (%s) and run it with its parameters (%s)", failure, err,
			mcputil.ToolCreateFormula, mcputil.ToolRunFormula))
	}
	return mcplib.NewToolResultError(fmt.Sprintf("%s: %v", failure, err))
}

// parseBdbID extracts and parses the bdb ID from the MCP tool request.
func parseBdbID(request mcplib.CallToolRequest) (uuid.UUID, error) {
	args := request.GetArguments()
	bdbIDStr, _ := args["bdb_id"].(string)
	if bdbIDStr == "" {
		return uuid.Nil, errors.New("bdb_id is required")
	}

	bdbID, err := uuid.Parse(bdbIDStr)
	if err != nil {
		return uuid.Nil, errors.New("invalid bdb_id")
	}

	return bdbID, nil
}

// conflictToolResult renders a refused undo / redo / revert as a tool error whose text is the JSON conflict payload,
// so an agent reads the reason, the blockers, and the resolution hints.
// A conflict is a normal outcome of the contract, not a failure of the tool, so it is not logged as an error.
func conflictToolResult(c *domain.ChangeConflict) *mcplib.CallToolResult {
	body, err := json.Marshal(toChangeConflictResult(c))
	if err != nil {
		return mcplib.NewToolResultError(domain.ErrChangeConflict.Error())
	}
	return mcplib.NewToolResultError(string(body))
}

// marshalToJSON returns result as a JSON text tool result, or an error tool result when it cannot be marshaled.
func marshalToJSON(result any) (*mcplib.CallToolResult, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to marshal result: %v", err)), nil
	}
	return mcplib.NewToolResultText(string(data)), nil
}
