package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
	"github.com/alexandremgo/baguettedb/pkg/starters/service"
)

// listStartersHandler returns the whole catalog: it is a handful of entries, so it needs no search or paging.
func listStartersHandler(starterSvc *service.StarterService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.list_starters")
		defer span.End()

		manifests, err := starterSvc.ListStarters(ctx)
		if err != nil {
			logger.Error().Err(err).Ctx(ctx).Msg("MCP list_starters failed")
			return mcplib.NewToolResultError(fmt.Sprintf("failed to list starters: %v", err)), nil
		}

		entries := make([]starterEntry, len(manifests))
		for i, m := range manifests {
			entries[i] = toStarterEntry(m)
		}

		data, err := json.Marshal(entries)
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("failed to marshal result: %v", err)), nil
		}
		return mcplib.NewToolResultText(string(data)), nil
	}
}

// createBdbHandler creates the bdb and returns it. A starter key seeds the new bdb in one server-side
// transaction; without one the bdb is created empty and needs an explicit name.
func createBdbHandler(starterSvc *service.StarterService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.create_bdb")
		defer span.End()

		userID, err := parseUserID(ctx)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		roles := authz.RolesFromContext(ctx)

		args := request.GetArguments()
		name, _ := args["name"].(string)
		description, _ := args["description"].(string)
		starterKey, _ := args["starter_key"].(string)

		if starterKey == "" && strings.TrimSpace(name) == "" {
			return mcplib.NewToolResultError(
				"a name is required to create a blank bdb; pass a starter_key to take the starter's own name",
			), nil
		}
		bdbID, err := starterSvc.CreateBdb(ctx, userID, roles, starterKey, name, description)
		if err != nil {
			return createBdbError(ctx, err, userID, starterKey, logger), nil
		}

		created, err := starterSvc.GetBdb(ctx, userID, bdbID)
		if err != nil {
			logger.Error().Err(err).Ctx(ctx).Str("bdb_id", bdbID.String()).
				Msg("MCP create_bdb: loading the new bdb failed")
			return mcplib.NewToolResultError(
				fmt.Sprintf("the bdb was created but could not be read back: %v", err)), nil
		}

		data, err := json.Marshal(createdBdb{
			ID:          created.ID.String(),
			Name:        created.Name,
			Description: created.Description,
			StarterKey:  starterKey,
			NextStep:    fmt.Sprintf("Call %s on this bdb before working in it.", mcputil.ToolDescribeBdb),
		})
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("failed to marshal result: %v", err)), nil
		}
		return mcplib.NewToolResultText(string(data)), nil
	}
}

// createBdbError turns a creation failure into a plain-language tool result. A seeding failure leaves no bdb
// behind, so it is safe to retry; its cause is server-side and goes to the log rather than the caller.
func createBdbError(
	ctx context.Context, err error, userID uuid.UUID, starterKey string, logger zerolog.Logger,
) *mcplib.CallToolResult {
	if limitErr, ok := errors.AsType[domain.BdbLimitReachedError](err); ok {
		return mcplib.NewToolResultError(fmt.Sprintf(
			"you've reached your limit of %d bdbs — delete one from the web app to free a slot", limitErr.Max))
	}

	switch {
	case errors.Is(err, domain.ErrStarterNotFound):
		return mcplib.NewToolResultError(fmt.Sprintf(
			"no starter with key %q — call %s for the available ones", starterKey, mcputil.ToolListStarters))
	case errors.Is(err, domain.ErrBdbNameEmpty):
		return mcplib.NewToolResultError("a bdb name cannot be empty")
	case errors.Is(err, domain.ErrBdbNameTooLong):
		return mcplib.NewToolResultError("that name is too long")
	case errors.Is(err, domain.ErrBdbNameTaken):
		return mcplib.NewToolResultError("another of your bdbs already has that name, whatever the case — pick another")
	case errors.Is(err, domain.ErrStarterApplyFailed):
		logger.Error().Err(err).Ctx(ctx).
			Str("user_id", userID.String()).
			Str("starter_key", starterKey).
			Msg("MCP create_bdb: applying the starter failed")
		return mcplib.NewToolResultError("setting up the bdb failed and nothing was kept — try again")
	default:
		logger.Error().Err(err).Ctx(ctx).
			Str("user_id", userID.String()).
			Str("starter_key", starterKey).
			Msg("MCP create_bdb failed")
		return mcplib.NewToolResultError(fmt.Sprintf("failed to create bdb: %v", err))
	}
}
