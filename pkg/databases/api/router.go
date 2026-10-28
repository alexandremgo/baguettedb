package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
)

// NewRouter creates a chi router with all databases routes mounted. requireToken authenticates the public API
// routes: it rejects an unauthenticated request or puts the caller's identity in the request context. The admin
// routes also need a permission that policy grants to the caller's roles.
//
// shardRouter is nil in single-pod / monolith deployments; when set, its middleware is installed on every
// bdb-scoped route group so requests for a bdb owned by another pod are transparently forwarded.
func NewRouter(
	handler *Handler,
	skillHandler *SkillHandler,
	formulaHandler *FormulaHandler,
	exportHandler *ExportHandler,
	internalHandler *InternalHandler,
	shardRouter *ShardRouter,
	requireToken func(http.Handler) http.Handler,
	policy authz.Policy,
	sseServer *mcpserver.SSEServer,
	maxRequestBodySize int64,
	internalAPISecret string,
) chi.Router {
	r := chi.NewRouter()

	r.Get("/api/databases/healthz", func(w http.ResponseWriter, _ *http.Request) {
		httputil.RespondJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "databases"})
	})

	// All database routes require authentication.
	r.Group(func(r chi.Router) {
		r.Use(requireToken)
		// Shard routing runs after auth (don't forward unauthenticated junk) and before MaxBodySize, so a
		// forwarded request carries its original body and the owner applies the limit.
		if shardRouter != nil {
			r.Use(shardRouter.Middleware)
		}
		r.Use(httputil.MaxBodySize(maxRequestBodySize))

		// Account-scoped: no bdb in the URL, so the shard-routing middleware passes it through and any
		// pod serves it from the shared metadata store.
		r.Get("/api/databases/v1/account/storage", handler.GetAccountStorage)

		r.Get("/api/databases/v1/bdbs/{bdbID}/database", handler.GetDatabase)
		r.Get("/api/databases/v1/bdbs/{bdbID}/database/stats", handler.GetDatabaseStats)
		r.Get("/api/databases/v1/bdbs/{bdbID}/database/tables", handler.ListTables)
		r.Get("/api/databases/v1/bdbs/{bdbID}/database/tables/{tableName}", handler.DescribeTable)
		r.Get("/api/databases/v1/bdbs/{bdbID}/database/export", exportHandler.ExportDatabase)
		r.Get("/api/databases/v1/bdbs/{bdbID}/database/export/status", exportHandler.GetExportStatus)
		r.Post("/api/databases/v1/bdbs/{bdbID}/database/query", handler.ExecuteQuery)
		r.Post("/api/databases/v1/bdbs/{bdbID}/database/data", handler.ExecuteData)
		r.Post("/api/databases/v1/bdbs/{bdbID}/database/schema", handler.ExecuteSchema)
		r.Get("/api/databases/v1/bdbs/{bdbID}/database/changes", handler.ListChanges)
		r.Post("/api/databases/v1/bdbs/{bdbID}/database/changes/undo", handler.UndoLastChange)
		r.Post("/api/databases/v1/bdbs/{bdbID}/database/changes/redo", handler.RedoLastChange)
		r.Post("/api/databases/v1/bdbs/{bdbID}/database/changes/undo/anyone", handler.UndoLastChangeAnyone)
		r.Post("/api/databases/v1/bdbs/{bdbID}/database/changes/redo/anyone", handler.RedoLastChangeAnyone)
		r.Post("/api/databases/v1/bdbs/{bdbID}/database/changes/{changeLogID}/revert", handler.RevertChange)
		r.Post("/api/databases/v1/bdbs/{bdbID}/database/changes/batch/{batchID}/revert",
			handler.RevertChangeBatch)

		// Skills CRUD. Permission (viewer for reads, editor+ for writes) is
		// enforced inside the SkillService via AuthorizeAndResolveDatabase.
		r.Get("/api/databases/v1/bdbs/{bdbID}/skills", skillHandler.ListSkills)
		r.Get("/api/databases/v1/bdbs/{bdbID}/skills/{ref}", skillHandler.GetSkill)
		r.Post("/api/databases/v1/bdbs/{bdbID}/skills", skillHandler.CreateSkill)
		r.Put("/api/databases/v1/bdbs/{bdbID}/skills/{ref}", skillHandler.UpdateSkill)
		r.Delete("/api/databases/v1/bdbs/{bdbID}/skills/{ref}", skillHandler.DeleteSkill)

		// Formulas CRUD. Permission (viewer for reads, editor+ for writes) is
		// enforced inside the FormulaService via AuthorizeAndResolveDatabase.
		r.Get("/api/databases/v1/bdbs/{bdbID}/formulas", formulaHandler.ListFormulas)
		r.Get("/api/databases/v1/bdbs/{bdbID}/formulas/{ref}", formulaHandler.GetFormula)
		r.Post("/api/databases/v1/bdbs/{bdbID}/formulas", formulaHandler.CreateFormula)
		r.Post("/api/databases/v1/bdbs/{bdbID}/formulas/{ref}/run", formulaHandler.RunFormula)
		r.Get("/api/databases/v1/bdbs/{bdbID}/formulas/{ref}/export.csv", exportHandler.ExportFormulaCSV)
		r.Put("/api/databases/v1/bdbs/{bdbID}/formulas/{ref}", formulaHandler.UpdateFormula)
		r.Delete("/api/databases/v1/bdbs/{bdbID}/formulas/{ref}", formulaHandler.DeleteFormula)

		// MCP SSE endpoints (standalone mode only; in monolith mode, the gateway handles MCP).
		if sseServer != nil {
			r.Handle("/mcp/sse", sseServer.SSEHandler())
			r.Handle("/mcp/message", sseServer.MessageHandler())
		}
	})

	// Admin routes — requires manage:users permission. Also shard-routed: admin stats read the local file, so
	// the request must reach the owning pod.
	r.Group(func(r chi.Router) {
		r.Use(requireToken)
		r.Use(authhttp.RequirePermission(policy, authz.PermManageUsers))
		if shardRouter != nil {
			r.Use(shardRouter.Middleware)
		}

		r.Get("/api/databases/v1/admin/bdbs/{bdbID}/database/stats", handler.GetDatabaseStatsAdmin)
	})

	// Internal service-to-service routes — no JWT, but callers must present the shared internal API secret. The
	// secret check runs before the shard proxy; a forwarded request passes it again on the owning pod since the
	// proxy preserves headers.
	r.Group(func(r chi.Router) {
		r.Use(authhttp.RequireInternalSecret(internalAPISecret))

		// The bdb-scoped routes are reached over the load-balanced databases Service, so they're shard-routed:
		// the proxy reads bdb_id from the body and forwards to the owning pod (the bdb isn't in the URL here).
		r.Group(func(r chi.Router) {
			if shardRouter != nil {
				r.Use(func(next http.Handler) http.Handler {
					return shardRouter.InternalBodyProxy(maxRequestBodySize, next)
				})
			}
			r.Post("/internal/api/databases/v1/tables", internalHandler.ListTables)
			r.Post("/internal/api/databases/v1/tables-with-schemas", internalHandler.TablesWithSchemas)
			r.Post("/internal/api/databases/v1/skills", internalHandler.ListSkills)
			r.Post("/internal/api/databases/v1/formulas", internalHandler.ListFormulas)
			r.Post("/internal/api/databases/v1/bdb-database/delete", internalHandler.DeleteBdbDatabase)
			r.Post("/internal/api/databases/v1/schema/execute", internalHandler.ExecuteSchema)
			r.Post("/internal/api/databases/v1/data/execute", internalHandler.ExecuteData)
			r.Post("/internal/api/databases/v1/formulas/create", internalHandler.CreateFormula)
			r.Post("/internal/api/databases/v1/formulas/update", internalHandler.UpdateFormula)
			r.Post("/internal/api/databases/v1/skills/create", internalHandler.CreateSkill)
		})

		// The snapshot routes stay outside that proxy: sweep has no bdb target and acts on whichever pod
		// receives it, and single-database snapshot is keyed by database_id (not a bdb) and addressed at a
		// specific pod directly - so neither is bdb-routed.
		r.Post("/internal/api/databases/v1/snapshot", internalHandler.SnapshotDatabase)
		r.Post("/internal/api/databases/v1/snapshot-sweep", internalHandler.SnapshotSweep)

		// Move primitives (rebalancer). Also pod-direct: the orchestrator addresses a specific pod by ordinal
		// over the headless DNS (release the source, adopt on the target), so these must act on the called pod
		// and are never shard-routed.
		r.Post("/internal/api/databases/v1/release", internalHandler.ReleaseDatabase)
		r.Post("/internal/api/databases/v1/adopt", internalHandler.AdoptDatabase)
		r.Post("/internal/api/databases/v1/complete-move", internalHandler.CompleteMove)
		r.Post("/internal/api/databases/v1/delete-local", internalHandler.DeleteLocalDatabase)
	})

	return r
}
