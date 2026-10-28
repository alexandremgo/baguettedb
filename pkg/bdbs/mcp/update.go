package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/bdbs/service"
	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
)

// updatedBdb is the update_bdb response payload: the bdb as it stands after the update.
type updatedBdb struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// registerUpdateBdbTool registers update_bdb, which renames a bdb and rewrites its description.
func registerUpdateBdbTool(s *mcpserver.MCPServer, bdbSvc *service.BdbService, logger zerolog.Logger) {
	s.AddTool(
		mcplib.NewTool(mcputil.ToolUpdateBdb,
			mcplib.WithDescription("Renames a bdb or rewrites its description. Only the bdb's owner can do "+
				"either. Pass just the field being changed — an omitted one keeps its current value. "+
				"Only call this when the user has asked for the change; confirm the new name with them "+
				"first, since every member of a shared bdb sees it. Members, invites, and deletion are "+
				"managed in the web app."),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
			mcplib.WithString("name", mcplib.Description("New name; omit to keep the current one")),
			mcplib.WithString("description",
				mcplib.Description("New description; omit to keep the current one, pass \"\" to clear it")),
		),
		updateBdbHandler(bdbSvc, logger),
	)
}

// optionalString returns a pointer to the string argument stored under name, or nil when args carries no
// string under that name.
func optionalString(args map[string]any, name string) *string {
	v, ok := args[name].(string)
	if !ok {
		return nil
	}
	return &v
}

// updateBdbHandler renames a bdb and rewrites its description, leaving an omitted field unchanged.
func updateBdbHandler(bdbSvc *service.BdbService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.update_bdb")
		defer span.End()

		userID, err := parseUserID(ctx)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}

		args := request.GetArguments()
		bdbIDStr, _ := args["bdb_id"].(string)
		if bdbIDStr == "" {
			return mcplib.NewToolResultError("bdb_id is required"), nil
		}
		bdbID, err := uuid.Parse(bdbIDStr)
		if err != nil {
			return mcplib.NewToolResultError("bdb_id is not a valid ID"), nil
		}

		name := optionalString(args, "name")
		description := optionalString(args, "description")
		if name == nil && description == nil {
			return mcplib.NewToolResultError("nothing to update: pass a name, a description, or both"), nil
		}

		bdb, err := bdbSvc.UpdateBdb(ctx, userID, bdbID, name, description)
		if err != nil {
			return updateBdbError(ctx, err, userID, bdbID, logger), nil
		}

		data, err := json.Marshal(updatedBdb{
			ID:          bdb.ID.String(),
			Name:        bdb.Name,
			Description: bdb.Description,
		})
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("failed to marshal result: %v", err)), nil
		}
		return mcplib.NewToolResultText(string(data)), nil
	}
}

// updateBdbError turns an update failure into a plain-language tool result, logging only the causes that are
// not the caller's to fix.
func updateBdbError(
	ctx context.Context, err error, userID, bdbID uuid.UUID, logger zerolog.Logger,
) *mcplib.CallToolResult {
	switch {
	case errors.Is(err, domain.ErrNotBdbMember):
		return mcplib.NewToolResultError("only the owner can rename this bdb or change its description")
	case errors.Is(err, domain.ErrBdbNotFound):
		return mcplib.NewToolResultError("bdb not found or not accessible")
	case errors.Is(err, domain.ErrBdbAlreadyDeleted):
		return mcplib.NewToolResultError(
			"this bdb is deleted and waiting to be purged — restore it from the web app before editing it")
	case errors.Is(err, domain.ErrBdbNameEmpty):
		return mcplib.NewToolResultError("a bdb name cannot be empty")
	case errors.Is(err, domain.ErrBdbNameTooLong):
		return mcplib.NewToolResultError("that name is too long")
	case errors.Is(err, domain.ErrBdbNameTaken):
		return mcplib.NewToolResultError("another of your bdbs already has that name, whatever the case — pick another")
	default:
		logger.Error().Err(err).Ctx(ctx).
			Str("user_id", userID.String()).
			Str("bdb_id", bdbID.String()).
			Msg("MCP update_bdb failed")
		return mcplib.NewToolResultError(fmt.Sprintf("failed to update bdb: %v", err))
	}
}
