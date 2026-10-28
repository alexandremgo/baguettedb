package mcp

import (
	"context"
	"fmt"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// skillSizeGuidance is the authoring guidance shared by the create_skill and update_skill descriptions: skill
// bodies are context the agent pays for on every orientation, so they must stay lean.
const skillSizeGuidance = "Keep the skill concise. Every skill body is loaded into agent context by " +
	mcputil.ToolDescribeBdb + " (table-targeted bodies also appear in " + mcputil.ToolDescribeTable + " for " +
	"their tables). Long skills bloat context for every future interaction in this bdb — aim for the minimum " +
	"content that conveys the rule clearly.\n\nNote: deletion is only available via the web UI — ask the user " +
	"to delete the skill there if needed."

// registerSkillTools registers the three skill MCP tools (get_skill, create_skill,
// update_skill). Skill discovery for a bdb happens via describe_bdb (in
// the bdbs MCP module); describe_table surfaces bodies for skills targeting
// a specific table.
func registerSkillTools(s *mcpserver.MCPServer, skillSvc *service.SkillService, logger zerolog.Logger) {
	s.AddTool(
		mcplib.NewTool(mcputil.ToolGetSkill,
			mcplib.WithDescription("Fetches a single skill (with body) by name or UUID. Useful "+
				"when you need to re-read a specific skill body that's no longer in context."),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
			mcplib.WithString("ref", mcplib.Required(),
				mcplib.Description("Skill reference: either the human-readable name "+
					"(e.g. 'groceries-flow') or the UUID")),
		),
		getSkillHandler(skillSvc, logger),
	)

	s.AddTool(
		mcplib.NewTool(mcputil.ToolCreateSkill,
			mcplib.WithDescription("Creates a new skill in the bdb. Skills teach agents "+
				"how to work with one or more tables (or with the bdb "+
				"as a whole if target_tables is empty).\n\n"+skillSizeGuidance),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
			mcplib.WithString("name", mcplib.Required(),
				mcplib.Description("Slug-shaped human-readable name, unique per bdb. "+
					"Lowercase letters, digits and hyphens; must start with "+
					"a letter (e.g. 'groceries-flow').")),
			mcplib.WithString("description", mcplib.Required(),
				mcplib.Description("One-line description; appears alongside the name in "+
					mcputil.ToolListBdbs+" and "+mcputil.ToolDescribeBdb+" for routing.")),
			mcplib.WithString("body", mcplib.Required(),
				mcplib.Description("Full markdown body of the skill (max 100 KB).")),
			mcplib.WithArray("target_tables",
				mcplib.Description("Tables this skill targets. Empty (or omitted) for a bdb-level skill."),
				mcplib.Items(map[string]any{"type": "string"})),
		),
		createSkillHandler(skillSvc, logger),
	)

	s.AddTool(
		mcplib.NewTool(mcputil.ToolUpdateSkill,
			mcplib.WithDescription("Updates an existing skill. Pass only the fields you want "+
				"to change; omitted fields are left untouched. To replace "+
				"target tables, pass the full new list (an empty array "+
				"converts the skill into a bdb-level skill).\n\n"+skillSizeGuidance),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
			mcplib.WithString("ref", mcplib.Required(), mcplib.Description("Skill reference: name or UUID.")),
			mcplib.WithString("name", mcplib.Description("New name (slug-shaped, unique per bdb).")),
			mcplib.WithString("description", mcplib.Description("New one-line description.")),
			mcplib.WithString("body", mcplib.Description("New markdown body (max 100 KB).")),
			mcplib.WithArray("target_tables",
				mcplib.Description("Replacement target table list. Pass an empty array to "+
					"convert to a bdb-level skill. Omit to leave targets unchanged."),
				mcplib.Items(map[string]any{"type": "string"})),
		),
		updateSkillHandler(skillSvc, logger),
	)
}

func getSkillHandler(skillSvc *service.SkillService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.get_skill")
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

		skill, err := skillSvc.GetSkill(ctx, userID, bdbID, ref)
		if err != nil {
			logger.Error().
				Err(err).
				Str("user_id", userID.String()).
				Str("bdb_id", bdbID.String()).
				Str("ref", ref).
				Msg("MCP get_skill failed")
			return mcplib.NewToolResultError(fmt.Sprintf("failed to fetch skill: %v", err)), nil
		}
		return marshalToJSON(toSkillDetail(skill))
	}
}

func createSkillHandler(skillSvc *service.SkillService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.create_skill")
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
		description, _ := args["description"].(string)
		body, _ := args["body"].(string)
		targets := parseStringArray(args["target_tables"])

		skill, err := skillSvc.CreateSkill(ctx, userID, bdbID, service.SkillInput{
			Name:         name,
			Description:  description,
			Body:         body,
			TargetTables: targets,
		})
		if err != nil {
			logger.Error().
				Err(err).
				Str("user_id", userID.String()).
				Str("bdb_id", bdbID.String()).
				Str("name", name).
				Msg("MCP create_skill failed")
			return mcplib.NewToolResultError(fmt.Sprintf("failed to create skill: %v", err)), nil
		}
		return marshalToJSON(toSkillDetail(skill))
	}
}

func updateSkillHandler(skillSvc *service.SkillService, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.update_skill")
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

		patch := service.SkillPatch{}
		if v, ok := args["name"].(string); ok && v != "" {
			patch.Name = &v
		}
		if v, ok := args["description"].(string); ok && v != "" {
			patch.Description = &v
		}
		if v, ok := args["body"].(string); ok && v != "" {
			patch.Body = &v
		}
		if raw, ok := args["target_tables"]; ok && raw != nil {
			parsed := parseStringArray(raw)
			patch.TargetTables = &parsed
		}

		skill, err := skillSvc.UpdateSkill(ctx, userID, bdbID, ref, patch)
		if err != nil {
			logger.Error().
				Err(err).
				Str("user_id", userID.String()).
				Str("bdb_id", bdbID.String()).
				Str("ref", ref).
				Msg("MCP update_skill failed")
			return mcplib.NewToolResultError(fmt.Sprintf("failed to update skill: %v", err)), nil
		}
		return marshalToJSON(toSkillDetail(skill))
	}
}

// parseStringArray accepts the Go interface{} value an MCP arg-list field
// arrives as ([]any of strings or nil) and returns a flat []string.
func parseStringArray(raw any) []string {
	if raw == nil {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
