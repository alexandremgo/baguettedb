package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/auth/service"
)

// OIDCLoginHandler serves the start and callback halves of an external identity provider sign-in flow. The
// inter-request flow context lives in a server-side store keyed by the OAuth state parameter — not in a cookie —
// because the IdP round-trip may happen in browsing contexts (e.g. an OAuth client's popup) where cookies set
// mid-flow are not reliably sent back on the callback navigation.
type OIDCLoginHandler struct {
	providers    map[string]service.OIDCProvider
	loginService *service.OIDCLoginService
	loginFlow    *service.LoginFlowService
	signupHook   SignupInterstitialHandler
	parent       *Handler
	logger       zerolog.Logger
}

// NewOIDCLoginHandler creates a new OIDCLoginHandler. The providers slice is indexed by Name() for O(1) lookups; the
// constructor panics if two providers share a name, since that is a wiring bug discoverable at startup. parent supplies
// the shared error-page renderer. signupHook is the optional extension point for the "no local user" branch — when
// nil, the handler renders a generic "sign-up unavailable" page.
func NewOIDCLoginHandler(
	providers []service.OIDCProvider,
	loginService *service.OIDCLoginService,
	loginFlow *service.LoginFlowService,
	signupHook SignupInterstitialHandler,
	parent *Handler,
	logger zerolog.Logger,
) *OIDCLoginHandler {
	byName := make(map[string]service.OIDCProvider, len(providers))
	for _, p := range providers {
		if _, dup := byName[p.Name()]; dup {
			panic("duplicate OIDC provider name: " + p.Name())
		}
		byName[p.Name()] = p
	}
	return &OIDCLoginHandler{
		providers:    byName,
		loginService: loginService,
		loginFlow:    loginFlow,
		signupHook:   signupHook,
		parent:       parent,
		logger:       logger,
	}
}

// Start handles GET /login/oidc/{provider}/start. Generates a nonce + PKCE verifier, builds the authorization URL,
// stores the inter-request flow state server-side keyed by the OAuth state value, and 302s the browser to the IdP.
func (h *OIDCLoginHandler) Start(w http.ResponseWriter, r *http.Request) {
	providerName := chi.URLParam(r, "provider")
	provider, ok := h.providers[providerName]
	if !ok {
		h.parent.renderError(w, http.StatusNotFound, "Unknown sign-in provider.")
		return
	}

	challenge := r.URL.Query().Get("login_challenge")
	if challenge == "" {
		h.parent.renderError(w, http.StatusBadRequest,
			"This sign-in link looks incomplete. Try signing in again from the app you came from.")
		return
	}

	authURL, flow, err := h.loginService.StartFlow(r.Context(), provider, challenge)
	if err != nil {
		h.logger.Error().Err(err).Str("provider", providerName).Msg("starting oidc flow")
		h.parent.renderError(w, http.StatusInternalServerError,
			"Something went wrong on our end. Please try again in a moment.")
		return
	}

	// Per-flow log line at /start with the state value as the correlation key — pair with the matching callback
	// log to compute latency and compare browsing-context headers across the IdP redirect.
	h.logger.Info().
		Str("provider", providerName).
		Str("state", flow.State).
		Str("sec_fetch_site", r.Header.Get("Sec-Fetch-Site")).
		Str("sec_fetch_mode", r.Header.Get("Sec-Fetch-Mode")).
		Str("user_agent", r.UserAgent()).
		Msg("oidc flow started")

	http.Redirect(w, r, authURL, http.StatusFound)
}

// ServeStylesheet handles GET /static/oidc/{provider}.css by serving the stylesheet of the registered provider's login
// button. Responds 404 when the provider name is unknown, when the provider offers no login button, or when its
// stylesheet is empty (i.e. it opted out of shipping one — typically because the button template inlines its styles).
func (h *OIDCLoginHandler) ServeStylesheet(w http.ResponseWriter, r *http.Request) {
	// An unknown name yields a nil provider, which the comma-ok assertion reports as not a login button.
	button, ok := h.providers[chi.URLParam(r, "provider")].(loginButton)
	if !ok {
		http.NotFound(w, r)
		return
	}
	body := button.Stylesheet()
	if len(body) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(body)
}

// Callback handles GET /login/oidc/{provider}/callback. Consumes the server-side flow state matching the echoed
// state parameter, exchanges the authorization code, and either calls hydra.AcceptLogin (existing local user) or
// dispatches to the signup-interstitial hook (no local user).
func (h *OIDCLoginHandler) Callback(w http.ResponseWriter, r *http.Request) {
	providerName := chi.URLParam(r, "provider")
	provider, ok := h.providers[providerName]
	if !ok {
		h.parent.renderError(w, http.StatusNotFound, "Unknown sign-in provider.")
		return
	}

	state := r.URL.Query().Get("state")
	flow, err := h.loginService.ConsumeFlow(r.Context(), providerName, state)
	switch {
	case errors.Is(err, domain.ErrOIDCStateNotFound):
		h.logger.Info().
			Str("provider", providerName).
			Str("state", state).
			Str("sec_fetch_site", r.Header.Get("Sec-Fetch-Site")).
			Str("sec_fetch_mode", r.Header.Get("Sec-Fetch-Mode")).
			Str("user_agent", r.UserAgent()).
			Msg("oidc callback rejected: no in-flight flow matches the state parameter")
		h.parent.renderError(w, http.StatusBadRequest,
			"Your sign-in session expired. Please start again from the app you came from.")
		return
	case errors.Is(err, domain.ErrOIDCProviderMismatch):
		h.parent.renderError(w, http.StatusBadRequest, "Sign-in session does not match this provider.")
		return
	case err != nil:
		h.logger.Error().Err(err).Str("provider", providerName).Msg("consuming oidc flow state")
		h.parent.renderError(w, http.StatusInternalServerError,
			"Something went wrong on our end. Please try again in a moment.")
		return
	}

	// Success-path diagnostic: the start→callback latency distribution makes a TTL-expiry regression
	// distinguishable from flows that never reach the callback at all.
	h.logger.Info().
		Str("provider", providerName).
		Str("state", flow.State).
		Dur("delta", time.Since(flow.CreatedAt)).
		Str("sec_fetch_site", r.Header.Get("Sec-Fetch-Site")).
		Str("sec_fetch_mode", r.Header.Get("Sec-Fetch-Mode")).
		Msg("oidc callback received")

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		h.logger.Info().Str("provider", providerName).Str("error", errParam).Msg("oidc provider returned error")
		h.parent.renderError(w, http.StatusBadRequest,
			fmt.Sprintf("Sign-in was cancelled or denied (%s).", errParam))
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		h.parent.renderError(w, http.StatusBadRequest, "Sign-in response is missing the authorization code.")
		return
	}

	result, err := h.loginService.HandleCallback(r.Context(), provider, code, flow.CodeVerifier, flow.Nonce)
	switch {
	case errors.Is(err, domain.ErrEmailUnverified):
		h.parent.renderError(w, http.StatusForbidden,
			"Your identity provider has not verified this email address. Please verify it and try again.")
		return
	case err != nil:
		h.logger.Error().Err(err).Str("provider", providerName).Msg("handling oidc callback")
		h.parent.renderError(w, http.StatusInternalServerError,
			"Something went wrong on our end. Please try again in a moment.")
		return
	}

	if result.NeedsSignup {
		if h.signupHook == nil {
			h.parent.renderError(w, http.StatusForbidden, "Sign-up via this identity provider is not available.")
			return
		}
		signupCtx := SignupContext{
			Challenge: flow.LoginChallenge,
			Provider:  providerName,
			Subject:   result.Info.Subject,
			Email:     result.Info.Email,
			FirstName: result.Info.FirstName,
			LastName:  result.Info.LastName,
		}
		h.signupHook.RenderInterstitial(w, r, signupCtx, NewCompletionAPI(h.loginFlow, flow.LoginChallenge))
		return
	}

	redirectTo, err := h.loginFlow.CompleteLogin(r.Context(), flow.LoginChallenge, result.User.ID)
	if err != nil {
		h.logger.Error().Err(err).Msg("accepting login after oidc callback")
		h.parent.renderError(w, http.StatusInternalServerError,
			"Something went wrong on our end. Please try again in a moment.")
		return
	}

	http.Redirect(w, r, redirectTo, http.StatusFound)
}
