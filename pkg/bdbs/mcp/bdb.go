package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/bdbs/service"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
)

// tableSummary is the lightweight table representation used by list_bdbs: just name + type (plus the virtual-table
// module, e.g. "fts5", when type is "virtual"). Column-level schemas live in describe_bdb and describe_table.
type tableSummary struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Module string `json:"module,omitempty"`
}

// tableDetail is the describe_bdb table entry: the tableSummary fields plus the table's column-level schema.
// Columns and ForeignKeys are dropped for every table when the bdb exceeds the schema table limit — the response's
// omitted note says so; ForeignKeys is also absent for tables that simply have none.
type tableDetail struct {
	Name        string                 `json:"name"`
	Type        string                 `json:"type"`
	Module      string                 `json:"module,omitempty"`
	Columns     []apptypes.TableColumn `json:"columns,omitempty"`
	ForeignKeys []apptypes.ForeignKey  `json:"foreign_keys,omitempty"`

	// DescribeError marks a degraded entry whose schema introspection failed (Columns is then empty).
	DescribeError string `json:"describe_error,omitempty"`
}

// skillEntry is the per-skill payload of describe_bdb. Body is inlined for every skill while the bdb's summed skill
// bodies fit its size budget; over budget, table-targeted bodies degrade to summaries (bdb-level bodies always
// inline) and the response's omitted note says so.
type skillEntry struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	TargetTables []string `json:"target_tables"`
	Body         string   `json:"body,omitempty"`
}

// listSkillEntry is the per-skill entry of list_bdbs: the routing signal only. Skill ids and target tables add
// nothing when picking a bdb — get_skill resolves by name and describe_bdb carries the full entries.
type listSkillEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// formulaEntry is the lightweight formula summary surfaced by describe_bdb for
// orientation: name, description and kind ("view" or "action"). The SQL and
// parameter schema are fetched on demand via get_formula.
type formulaEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Label is the optional human name the app renders instead of Name; empty falls back to Name.
	Label       string `json:"label,omitempty"`
	Description string `json:"description"`
	Kind        string `json:"kind"`
	// Hidden marks an assistant-only formula: members don't see it in the app's formula list.
	Hidden bool `json:"hidden,omitempty"`
}

// bdbOverview is the describe_bdb response payload.
// Omitted lists, in plain language, anything the response dropped to stay within its size budgets, each entry naming
// the tool to call instead; absent when nothing was dropped.
// DeletedAt and PurgeAt are set only for soft-deleted bdbs; PurgeAt is the deletion timestamp plus the grace period
// after which the bdb is purged.
type bdbOverview struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Role        string `json:"role"`
	// Owner is the bdb owner's display name, set only when the caller is not the owner — it marks the bdb as
	// someone else's ("Emma's Workout"), which is a routing signal names alone don't carry.
	Owner     string         `json:"owner,omitempty"`
	Tables    []tableDetail  `json:"tables"`
	Skills    []skillEntry   `json:"skills"`
	Formulas  []formulaEntry `json:"formulas"`
	Omitted   []string       `json:"omitted,omitempty"`
	DeletedAt *time.Time     `json:"deleted_at,omitempty"`
	PurgeAt   *time.Time     `json:"purge_at,omitempty"`
}

// bdbSummary is the per-bdb entry in the list_bdbs response. Tables and skills are deliberately lightweight —
// list_bdbs is the routing directory, so it never returns column schemas or skill bodies; describe_bdb does.
// DeletedAt and PurgeAt are set only for soft-deleted bdbs.
type bdbSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Role        string `json:"role"`
	// Owner is the bdb owner's display name, set only when the caller is not the owner — it marks the bdb as
	// someone else's ("Emma's Workout"), which is a routing signal names alone don't carry.
	Owner     string           `json:"owner,omitempty"`
	Tables    []tableSummary   `json:"tables"`
	Skills    []listSkillEntry `json:"skills"`
	DeletedAt *time.Time       `json:"deleted_at,omitempty"`
	PurgeAt   *time.Time       `json:"purge_at,omitempty"`
}

// registerBdbTools registers all bdb-level MCP tools (list_bdbs for
// multi-bdb browsing, describe_bdb for bdb-bound orientation).
func registerBdbTools(s *mcpserver.MCPServer, bdbSvc *service.BdbService, dbRef *DatabasesAppRef, opts Options, logger zerolog.Logger) {
	s.AddTool(
		mcplib.NewTool(mcputil.ToolListBdbs,
			mcplib.WithDescription("Lists all bdbs the authenticated user belongs to, with their role in each. "+
				"This is the routing directory: per bdb it returns id, name, description, table "+
				"names+types, and skill summaries (name, description) — no column schemas and no skill "+
				"bodies. Use it to decide which bdb the task belongs to, then call "+
				mcputil.ToolDescribeBdb+" on the chosen bdb for its full orientation. "+
				"A bdb with `deleted_at` set is soft-deleted (purged on or after `purge_at`) — warn the "+
				"user before working in it; restorable from the web UI until then."),
		),
		listBdbsHandler(bdbSvc, dbRef, logger),
	)

	s.AddTool(
		mcplib.NewTool(mcputil.ToolDescribeBdb,
			mcplib.WithDescription("Describes a single bdb: tables with their column schemas (columns and "+
				"foreign keys), skills with their full bodies inlined (bdb-level and table-targeted "+
				"alike), and formulas (name, description and kind — 'view' or 'action' — their SQL and "+
				"parameters live in "+mcputil.ToolGetFormula+"). When a bdb is too large, schemas or "+
				"table-targeted skill bodies fall back to summaries and the `omitted` field says what "+
				"to call instead. "+
				"Always call this orientation tool once you have decided which bdb to work in — right "+
				"after picking it from "+mcputil.ToolListBdbs+" or being pointed at it by the user, and "+
				"before querying, mutating, or running formulas there. "+
				"If the bdb has `deleted_at` set, it has been soft-deleted by its owner and will be "+
				"permanently purged on or after `purge_at`. Warn the user before working in this bdb and "+
				"tell them they can restore it from the web UI before the purge date."),
			mcplib.WithString("bdb_id", mcplib.Required(), mcplib.Description("Bdb ID")),
		),
		describeBdbHandler(bdbSvc, dbRef, opts, logger),
	)
}

func listBdbsHandler(bdbSvc *service.BdbService, dbRef *DatabasesAppRef, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.list_bdbs")
		defer span.End()

		userID, err := parseUserID(ctx)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}

		bdbs, err := bdbSvc.ListUserBdbs(ctx, userID)
		if err != nil {
			logger.Error().Err(err).Str("user_id", userID.String()).Msg("MCP list_bdbs failed")
			return mcplib.NewToolResultError(fmt.Sprintf("failed to list bdbs: %v", err)), nil
		}

		// Owner display names are resolved in one batched lookup, and only for bdbs the caller doesn't own —
		// the caller's own bdbs need no attribution.
		shared := make([]*domain.Bdb, 0, len(bdbs))
		for _, b := range bdbs {
			if m := b.FindMember(userID); m != nil && m.Role != authz.BdbRoleOwner {
				shared = append(shared, b)
			}
		}
		ownerProfiles := map[uuid.UUID]*apptypes.UserProfile{}
		if len(shared) > 0 {
			ownerProfiles = bdbSvc.ResolveOwnerProfiles(ctx, shared)
		}

		summaries := make([]bdbSummary, 0, len(bdbs))
		for _, b := range bdbs {
			member := b.FindMember(userID)
			if member == nil {
				continue
			}

			ownerName := ""
			if p, ok := ownerProfiles[b.ID]; ok {
				ownerName = displayName(p)
			}

			tables, err := dbRef.App.ListTables(ctx, userID, b.ID)
			if err != nil {
				logger.Warn().Err(err).Str("bdb_id", b.ID.String()).Msg("failed to fetch tables for bdb")
				tables = nil
			}
			tableEntries := make([]tableSummary, 0, len(tables))
			for _, t := range tables {
				tableEntries = append(tableEntries, tableSummary{Name: t.Name, Type: t.Type, Module: t.Module})
			}

			skills, err := dbRef.App.ListSkills(ctx, userID, b.ID)
			if err != nil {
				logger.Warn().Err(err).Str("bdb_id", b.ID.String()).Msg("failed to fetch skills for bdb")
				skills = nil
			}
			skillEntries := make([]listSkillEntry, 0, len(skills))
			for _, sk := range skills {
				skillEntries = append(skillEntries, listSkillEntry{Name: sk.Name, Description: sk.Description})
			}

			summaries = append(summaries, bdbSummary{
				ID:          b.ID.String(),
				Name:        b.Name,
				Description: b.Description,
				Role:        string(member.Role),
				Owner:       ownerName,
				Tables:      tableEntries,
				Skills:      skillEntries,
				DeletedAt:   b.DeletedAt,
				PurgeAt:     b.PurgeAt(),
			})
		}

		// A brand-new account has no bdbs at all; a bare [] reads like something is broken, so guide the agent
		// (and through it the user) to the app's creation flow instead.
		if len(summaries) == 0 {
			return mcplib.NewToolResultText("The user has no bdbs yet — this is normal for a new account, " +
				"nothing is broken. Offer to create their first bdb right here: " + mcputil.ToolListStarters +
				" shows the curated starters (ready-made tables, formulas, and a skill), and " +
				mcputil.ToolCreateBdb + " creates one from a starter or blank — ask before creating and " +
				"confirm the name. The \"+ New Bdb\" gallery in the BaguetteDB web app is the alternative."), nil
		}

		data, err := json.Marshal(summaries)
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("failed to marshal result: %v", err)), nil
		}
		return mcplib.NewToolResultText(string(data)), nil
	}
}

func describeBdbHandler(bdbSvc *service.BdbService, dbRef *DatabasesAppRef, opts Options, logger zerolog.Logger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		ctx, span := tracer.Start(ctx, "mcp.describe_bdb")
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

		bdbs, err := bdbSvc.ListUserBdbs(ctx, userID)
		if err != nil {
			logger.Error().Err(err).Str("user_id", userID.String()).Msg("MCP describe_bdb failed")
			return mcplib.NewToolResultError(fmt.Sprintf("failed to load bdbs: %v", err)), nil
		}

		var matched *domain.Bdb
		for _, b := range bdbs {
			if b.ID.String() == bdbIDStr {
				matched = b
				break
			}
		}
		if matched == nil {
			return mcplib.NewToolResultError("bdb not found or not accessible"), nil
		}

		member := matched.FindMember(userID)
		if member == nil {
			return mcplib.NewToolResultError("bdb not found or not accessible"), nil
		}

		ownerName := ""
		if member.Role != authz.BdbRoleOwner {
			if p, ok := bdbSvc.ResolveOwnerProfiles(ctx, []*domain.Bdb{matched})[matched.ID]; ok {
				ownerName = displayName(p)
			}
		}

		var omitted []string

		tables, err := dbRef.App.ListTablesWithSchemas(ctx, userID, matched.ID)
		if err != nil {
			logger.Warn().Err(err).Str("bdb_id", matched.ID.String()).Msg("describe_bdb: failed to fetch tables")
			tables = nil
		}
		// Every table entry carries its columns and foreign keys while the bdb stays within the table limit.
		// Over it, all entries degrade to name+type — a bdb that large needs per-table introspection anyway.
		inlineSchemas := len(tables) <= opts.SchemaTableLimit
		if !inlineSchemas {
			omitted = append(omitted, fmt.Sprintf(
				"column schemas (%d tables exceed the %d-table limit) — call %s per table",
				len(tables), opts.SchemaTableLimit, mcputil.ToolDescribeTable))
		}
		tableEntries := make([]tableDetail, 0, len(tables))
		for _, t := range tables {
			entry := tableDetail{Name: t.Name, Type: t.Type, Module: t.Module, DescribeError: t.DescribeError}
			if inlineSchemas {
				entry.Columns = t.Schema.Columns
				entry.ForeignKeys = t.Schema.ForeignKeys
			}
			tableEntries = append(tableEntries, entry)
		}

		skills, err := dbRef.App.ListSkills(ctx, userID, matched.ID)
		if err != nil {
			logger.Warn().Err(err).Str("bdb_id", matched.ID.String()).Msg("describe_bdb: failed to fetch skills")
			skills = nil
		}
		// Every skill body is inlined while the bdb's summed bodies fit the budget. Over budget, table-targeted
		// bodies degrade to summaries; bdb-level bodies are orientation and always inline.
		totalBodyBytes := 0
		for _, s := range skills {
			totalBodyBytes += len(s.Body)
		}
		inlineTargetedBodies := totalBodyBytes <= opts.SkillBodyBudgetBytes

		if !inlineTargetedBodies {
			omitted = append(omitted, fmt.Sprintf(
				"table-targeted skill bodies (%d bytes of skill bodies exceed the %d-byte budget) — call %s for the skills targeting a table, or %s by name",
				totalBodyBytes, opts.SkillBodyBudgetBytes, mcputil.ToolDescribeTable, mcputil.ToolGetSkill))
		}

		skillEntries := make([]skillEntry, 0, len(skills))
		for _, s := range skills {
			targets := s.TargetTables
			if targets == nil {
				targets = []string{}
			}
			entry := skillEntry{
				ID:           s.ID,
				Name:         s.Name,
				Description:  s.Description,
				TargetTables: targets,
			}
			if len(targets) == 0 || inlineTargetedBodies {
				entry.Body = s.Body
			}
			skillEntries = append(skillEntries, entry)
		}

		formulas, err := dbRef.App.ListFormulas(ctx, userID, matched.ID)
		if err != nil {
			logger.Warn().Err(err).Str("bdb_id", matched.ID.String()).Msg("describe_bdb: failed to fetch formulas")
			formulas = nil
		}
		formulaEntries := make([]formulaEntry, 0, len(formulas))
		for _, f := range formulas {
			formulaEntries = append(formulaEntries, formulaEntry{
				ID:          f.ID,
				Name:        f.Name,
				Label:       f.Label,
				Description: f.Description,
				Kind:        f.Kind,
				Hidden:      f.Hidden,
			})
		}

		overview := bdbOverview{
			ID:          matched.ID.String(),
			Name:        matched.Name,
			Description: matched.Description,
			Role:        string(member.Role),
			Owner:       ownerName,
			Tables:      tableEntries,
			Skills:      skillEntries,
			Formulas:    formulaEntries,
			Omitted:     omitted,
			DeletedAt:   matched.DeletedAt,
			PurgeAt:     matched.PurgeAt(),
		}

		data, err := json.Marshal(overview)
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("failed to marshal result: %v", err)), nil
		}
		return mcplib.NewToolResultText(string(data)), nil
	}
}

// displayName returns a profile's human-readable name: "First Last" (or just the first name) when set, falling back
// to the email, and "" when the profile carries neither.
func displayName(p *apptypes.UserProfile) string {
	switch {
	case p.FirstName != "" && p.LastName != "":
		return p.FirstName + " " + p.LastName
	case p.FirstName != "":
		return p.FirstName
	default:
		return p.Email
	}
}
