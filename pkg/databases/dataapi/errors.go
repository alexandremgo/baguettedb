package dataapi

import (
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// The codes of the Data API's errors: stable, so an app may branch on them while their messages change.
const (
	codeMissingKey      = "missing_key"
	codeInvalidKey      = "invalid_key"
	codeKeyNotForBdb    = "key_not_for_bdb"
	codeFormulaNotOnKey = "formula_not_on_key"
	codeAccessDenied    = "access_denied"
	codeFormulaNotFound = "formula_not_found"
	codeInvalidRequest  = "invalid_request"
	codeInvalidParams   = "invalid_params"
	codeRequestTooLarge = "request_too_large"
	codeQueryFailed     = "query_failed"
	codeResultTooLarge  = "result_too_large"
	codeTimeout         = "timeout"
	codeRateLimited     = "rate_limited"
	codeUnavailable     = "unavailable"
	codeInternalError   = "internal_error"
)

// unavailableRetryAfter is how long, in seconds, the Retry-After header tells an app to wait before it retries a
// request refused because the bdb is briefly unavailable.
const unavailableRetryAfter = "5"

// errorResponse is a Data API error: a message for people, and a code for the app.
type errorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// respondError answers with status and an errorResponse of message and code.
func respondError(w http.ResponseWriter, status int, code, message string) {
	httputil.RespondJSON(w, status, errorResponse{Error: message, Code: code})
}

// OwnerUnreachable answers a Data API request that could not be forwarded to the pod owning its bdb, as that pod
// answers for a bdb that is briefly unavailable: a 503 with the unavailable code and a Retry-After header, allowing any
// origin so a page can read it. A preflight request is answered with a 204 No Content, as it needs nothing of the bdb.
func OwnerUnreachable(w http.ResponseWriter, r *http.Request) {
	allowAnyOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", unavailableRetryAfter)
		respondError(w, http.StatusServiceUnavailable, codeUnavailable, "the bdb is briefly unavailable; retry shortly")
	})).ServeHTTP(w, r)
}

// respondFormulaError maps the error of listing or running the key's formulas to a status and a code. An error the
// app can't act on is logged, and answered as an internal error.
func (h *handler) respondFormulaError(w http.ResponseWriter, key authhttp.DataAPIKeyIdentity, err error) {
	if limited, ok := errors.AsType[*domain.QueryRateLimitedError](err); ok {
		w.Header().Set("Retry-After", strconv.FormatInt(int64(math.Ceil(limited.RetryAfter.Seconds())), 10))
		respondError(w, http.StatusTooManyRequests, codeRateLimited, limited.Error())
		return
	}
	switch {
	case errors.Is(err, domain.ErrFormulaNotOnDataAPIKey):
		respondError(w, http.StatusForbidden, codeFormulaNotOnKey, "the formula is not on this data API key")
	case errors.Is(err, domain.ErrAccessDenied), errors.Is(err, domain.ErrReservedTable):
		respondError(w, http.StatusForbidden, codeAccessDenied, "access denied")
	case errors.Is(err, domain.ErrFormulaNotFound):
		respondError(w, http.StatusNotFound, codeFormulaNotFound, "formula not found")
	case errors.Is(err, domain.ErrFormulaParamInvalid), errors.Is(err, domain.ErrFormulaParamValue):
		respondError(w, http.StatusBadRequest, codeInvalidParams, err.Error())
	case errors.Is(err, domain.ErrQueryExecution), errors.Is(err, domain.ErrTooManyRows),
		errors.Is(err, domain.ErrFormulaViewNotPageable):
		respondError(w, http.StatusUnprocessableEntity, codeQueryFailed, err.Error())
	case errors.Is(err, domain.ErrQueryResultTooLarge):
		respondError(w, http.StatusRequestEntityTooLarge, codeResultTooLarge, err.Error())
	case errors.Is(err, domain.ErrFormulaRunTimeout), errors.Is(err, domain.ErrQueryTimeout):
		respondError(w, http.StatusGatewayTimeout, codeTimeout, "the formula took too long to run")
	case errors.Is(err, domain.ErrDatabaseReadOnly):
		w.Header().Set("Retry-After", unavailableRetryAfter)
		respondError(w, http.StatusServiceUnavailable, codeUnavailable, "the bdb is briefly read-only; retry shortly")
	case errors.Is(err, domain.ErrNewerPlatformSchema):
		w.Header().Set("Retry-After", unavailableRetryAfter)
		respondError(w, http.StatusServiceUnavailable, codeUnavailable, "the bdb is being upgraded; retry shortly")
	default:
		h.logger.Error().Err(err).Str("key_id", key.KeyID.String()).Msg("data API request failed")
		respondError(w, http.StatusInternalServerError, codeInternalError, "internal server error")
	}
}
