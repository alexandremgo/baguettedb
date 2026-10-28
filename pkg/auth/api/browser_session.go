package api

import (
	"cmp"
	"encoding/json"
	"errors"
	"mime"
	"net/http"

	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/auth/service"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
)

// BrowserSessionHandler signs browsers in and out of an instance with no OAuth flow: a browser signs in with its user's
// email and password and gets a session, which it keeps in the httpOnly session cookie; signing out revokes that
// session.
type BrowserSessionHandler struct {
	tokens        *service.TokenService
	cookieName    string
	secureCookies bool
	logger        zerolog.Logger
}

// NewBrowserSessionHandler creates a BrowserSessionHandler managing browsers' tokens through tokens, in the cookie
// cookieName (empty for the default access token cookie), with the Secure flag when secureCookies is set.
func NewBrowserSessionHandler(
	tokens *service.TokenService, cookieName string, secureCookies bool, logger zerolog.Logger,
) *BrowserSessionHandler {
	return &BrowserSessionHandler{
		tokens: tokens, cookieName: cmp.Or(cookieName, accessTokenCookie), secureCookies: secureCookies, logger: logger,
	}
}

// signInRequest is the body of POST /api/auth/v1/session: a user's email and password.
type signInRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// SignIn handles POST /api/auth/v1/session: it checks the user's email and password and signs the browser in with a
// session, set as the session cookie. An unknown email, a wrong password and an account without one are the same 401,
// so the route never tells which accounts exist.
func (h *BrowserSessionHandler) SignIn(w http.ResponseWriter, r *http.Request) {
	var req signInRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	plain, _, err := h.tokens.SignIn(r.Context(), req.Email, req.Password)
	if errors.Is(err, domain.ErrInvalidCredentials) {
		httputil.RespondJSON(w, http.StatusUnauthorized, errorResponse{Error: "invalid email or password"})
		return
	}
	if err != nil {
		h.logger.Error().Err(err).Msg("failed to sign in")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}
	h.setSessionCookie(w, plain)
	w.WriteHeader(http.StatusNoContent)
}

// Logout handles POST /api/auth/v1/logout: it ends the session in the session cookie, if any, and clears the cookie.
// Holding the cookie is what entitles the request to end its session, so no other authentication is asked.
func (h *BrowserSessionHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(h.cookieName); err == nil {
		if err := h.tokens.EndSession(r.Context(), cookie.Value); err != nil {
			h.logger.Error().Err(err).Msg("failed to end a browser's session")
			httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
			return
		}
	}
	h.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// setSessionCookie sets the session cookie to the browser's session plain, lasting as long as the session.
func (h *BrowserSessionHandler) setSessionCookie(w http.ResponseWriter, plain string) {
	http.SetCookie(w, &http.Cookie{
		Name:     h.cookieName,
		Value:    plain,
		Path:     "/",
		MaxAge:   int(service.SessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   h.secureCookies,
		SameSite: http.SameSiteStrictMode,
	})
}

// clearSessionCookie expires the session cookie.
func (h *BrowserSessionHandler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     h.cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.secureCookies,
		SameSite: http.SameSiteStrictMode,
	})
}

// decodeJSONBody decodes r's JSON body into v, or answers and reports false: 415 when the body isn't declared as
// JSON, 400 when it doesn't decode. Requiring the JSON content type keeps another site's plain HTML form, which can't
// set it, from posting to the route.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil ||
		mediaType != "application/json" {
		httputil.RespondJSON(w, http.StatusUnsupportedMediaType, errorResponse{Error: "the body must be JSON"})
		return false
	}
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return false
	}
	return true
}
