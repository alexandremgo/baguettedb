// Package dataapi serves the Data API: the versioned HTTP API an app calls with a Data API key to run the formulas
// picked for that key, on the one bdb the key is bound to. The contract is kept backward compatible, so its request and
// response types are its own, apart from the REST API's. Any origin may call it from a browser, since a key in the
// Authorization header, never a cookie, authenticates a request.
package dataapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// formulaRunner lists and runs the formulas of a Data API key.
type formulaRunner interface {
	// ListFormulasForDataAPIKey returns the formulas the key keyID of the bdb bdbID may run, as the user userID who
	// created the key sees them.
	ListFormulasForDataAPIKey(ctx context.Context, userID, keyID, bdbID uuid.UUID) ([]*domain.Formula, error)

	// ViewColumns returns the result columns of each view among formulas of the bdb bdbID, by formula ID, as the user
	// userID reads them.
	ViewColumns(ctx context.Context, userID, bdbID uuid.UUID, formulas []*domain.Formula) (map[string][]string, error)

	// RunFormulaAsDataAPIKey runs the formula formulaID for the key keyID of the bdb bdbID, acting as userID. A formula
	// not on the key is a domain.ErrFormulaNotOnDataAPIKey.
	RunFormulaAsDataAPIKey(
		ctx context.Context, userID, keyID, bdbID uuid.UUID, formulaID string, input service.FormulaRunInput,
	) (*service.FormulaRunResult, error)
}

// Register mounts the Data API on r under /api/data/v1/bdbs/{bdbID}: listing the key's formulas and running one. Each
// request is passed through routeToOwner when set, then let through from any origin, authenticated by its key with
// check, and limited to maxRequestBodySize.
//
// Routing comes first, needing only the path's bdb: a request reaching a pod that doesn't own the bdb is forwarded
// untouched, and the owner alone answers it, so its cross-origin headers are set once, not once by each pod, and its
// key is checked once, on the pod counting the key's in-flight slots. A request with a bad key costs one forward
// before its refusal.
func Register(
	r chi.Router, formulas formulaRunner, check authhttp.DataAPIKeyChecker,
	routeToOwner func(http.Handler) http.Handler, maxRequestBodySize int64, logger zerolog.Logger,
) {
	h := &handler{formulas: formulas, logger: logger}
	r.Route("/api/data/v1/bdbs/{bdbID}", func(r chi.Router) {
		if routeToOwner != nil {
			r.Use(routeToOwner)
		}
		r.Use(allowAnyOrigin)
		r.Use(requireKey(check, logger))
		r.Use(httputil.MaxBodySize(maxRequestBodySize))
		// Each route has an OPTIONS twin, so a preflight request is routed like the request it precedes, wherever these
		// routes are mounted, and allowAnyOrigin answers it.
		r.Get("/formulas", h.listFormulas)
		r.Options("/formulas", noContent)
		r.Post("/formulas/{formulaID}/run", h.runFormula)
		r.Options("/formulas/{formulaID}/run", noContent)
	})
}

// noContent answers with a 204 No Content.
func noContent(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// handler serves the Data API's routes, for the key the request carries.
type handler struct {
	formulas formulaRunner
	logger   zerolog.Logger
}

// listFormulas handles GET /api/data/v1/bdbs/{bdbID}/formulas: the formulas the key may run, with their parameters,
// and a view's columns.
func (h *handler) listFormulas(w http.ResponseWriter, r *http.Request) {
	key, bdbID, ok := h.keyAndBdb(w, r)
	if !ok {
		return
	}
	formulas, err := h.formulas.ListFormulasForDataAPIKey(r.Context(), key.UserID, key.KeyID, bdbID)
	if err != nil {
		h.respondFormulaError(w, key, err)
		return
	}
	columns, err := h.formulas.ViewColumns(r.Context(), key.UserID, bdbID, formulas)
	if err != nil {
		h.respondFormulaError(w, key, err)
		return
	}
	resp := formulaListResponse{Formulas: make([]formulaResponse, len(formulas))}
	for i, f := range formulas {
		resp.Formulas[i] = toFormulaResponse(f, columns[f.ID.String()])
	}
	httputil.RespondJSON(w, http.StatusOK, resp)
}

// runFormula handles POST /api/data/v1/bdbs/{bdbID}/formulas/{formulaID}/run: runs the formula with the parameter
// values of the body, which may be empty when the formula takes none. A view answers its rows, an action how many rows
// it changed.
func (h *handler) runFormula(w http.ResponseWriter, r *http.Request) {
	key, bdbID, ok := h.keyAndBdb(w, r)
	if !ok {
		return
	}
	var req runRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		if httputil.IsMaxBytesError(err) {
			respondError(w, http.StatusRequestEntityTooLarge, codeRequestTooLarge, "request body too large")
			return
		}
		respondError(w, http.StatusBadRequest, codeInvalidRequest, "invalid request body: want {\"params\": {...}}")
		return
	}

	result, err := h.formulas.RunFormulaAsDataAPIKey(r.Context(), key.UserID, key.KeyID, bdbID,
		chi.URLParam(r, "formulaID"), service.FormulaRunInput{Params: req.Params})
	if err != nil {
		h.respondFormulaError(w, key, err)
		return
	}
	if result.Kind == domain.FormulaKindAction {
		httputil.RespondJSON(w, http.StatusOK, toActionResponse(result))
		return
	}
	httputil.RespondJSON(w, http.StatusOK, toViewResponse(result))
}

// keyAndBdb returns the key the request was authenticated with and the bdb of its path. A request reaching a handler
// without a key is a server error, answered and reported as not ok.
func (h *handler) keyAndBdb(w http.ResponseWriter, r *http.Request) (authhttp.DataAPIKeyIdentity, uuid.UUID, bool) {
	key, ok := keyFromContext(r.Context())
	bdbID, err := uuid.Parse(chi.URLParam(r, "bdbID"))
	if !ok || err != nil {
		h.logger.Error().Bool("key", ok).Msg("data API request reached its handler without a key or bdb")
		respondError(w, http.StatusInternalServerError, codeInternalError, "internal server error")
		return authhttp.DataAPIKeyIdentity{}, uuid.Nil, false
	}
	return key, bdbID, true
}
