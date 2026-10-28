package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
)

// RegisterRoutes mounts the starters routes on the given router (the bdbs service's), behind requireToken and the
// same body-size limit as the bdb routes.
func RegisterRoutes(
	r chi.Router, h *Handler, requireToken func(http.Handler) http.Handler, maxRequestBodySize int64,
) {
	r.Group(func(r chi.Router) {
		r.Use(requireToken)
		r.Use(httputil.MaxBodySize(maxRequestBodySize))

		r.Get("/api/bdbs/v1/starters", h.ListStarters)
		r.Post("/api/bdbs/v1/bdbs/from-starter", h.CreateFromStarter)
	})
}
