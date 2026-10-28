package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
)

// NewRouter creates a chi router with all bdbs routes mounted. requireToken authenticates the public API routes: it
// rejects an unauthenticated request or puts the caller's identity in the request context.
func NewRouter(
	handler *Handler, inviteHandler *InviteHandler, internalHandler *InternalHandler,
	requireToken func(http.Handler) http.Handler, maxRequestBodySize int64, internalAPISecret string,
) chi.Router {
	r := chi.NewRouter()

	r.Get("/api/bdbs/healthz", func(w http.ResponseWriter, _ *http.Request) {
		httputil.RespondJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "bdbs"})
	})

	// Public API routes — requires valid JWT.
	r.Group(func(r chi.Router) {
		r.Use(requireToken)
		r.Use(httputil.MaxBodySize(maxRequestBodySize))

		r.Post("/api/bdbs/v1/bdbs", handler.CreateBdb)
		r.Get("/api/bdbs/v1/bdbs", handler.ListBdbs)
		// Declared before the {bdbID} routes for readability; chi matches this literal segment first
		// regardless of registration order.
		r.Patch("/api/bdbs/v1/bdbs/order", handler.ReorderBdbs)
		r.Get("/api/bdbs/v1/bdbs/{bdbID}", handler.GetBdb)
		r.Patch("/api/bdbs/v1/bdbs/{bdbID}", handler.UpdateBdb)
		r.Delete("/api/bdbs/v1/bdbs/{bdbID}", handler.DeleteBdb)
		r.Post("/api/bdbs/v1/bdbs/{bdbID}/restore", handler.RestoreBdb)

		r.Get("/api/bdbs/v1/bdbs/{bdbID}/members", handler.ListMembers)
		r.Patch("/api/bdbs/v1/bdbs/{bdbID}/members/{userID}", handler.UpdateMemberRole)
		r.Delete("/api/bdbs/v1/bdbs/{bdbID}/members/{userID}", handler.RemoveMember)

		// Invite routes.
		r.Post("/api/bdbs/v1/bdbs/{bdbID}/invites", inviteHandler.CreateInvite)
		r.Get("/api/bdbs/v1/bdbs/{bdbID}/invites", inviteHandler.ListBdbInvites)
		r.Delete("/api/bdbs/v1/bdbs/{bdbID}/invites/{inviteID}", inviteHandler.RevokeInvite)
		r.Get("/api/bdbs/v1/invites/{token}", inviteHandler.GetInvite)
		r.Post("/api/bdbs/v1/invites/{token}/accept", inviteHandler.AcceptInvite)
	})

	// Internal service-to-service routes — no JWT, but callers must present the shared internal API secret.
	r.Group(func(r chi.Router) {
		r.Use(authhttp.RequireInternalSecret(internalAPISecret))
		r.Post("/internal/api/bdbs/v1/check-permission", internalHandler.CheckPermission)
		r.Post("/internal/api/bdbs/v1/bdb-owner", internalHandler.GetBdbOwner)
		r.Post("/internal/api/bdbs/v1/owned-bdb-ids", internalHandler.GetOwnedBdbIDs)
	})

	return r
}
