// Package api serves the auth app over HTTP: the profile, admin, personal access token and browser session routes, the
// internal service-to-service routes, and the OAuth2 sign-in in front of Hydra — the server-rendered login and consent
// pages with their embedded templates and assets, external identity provider sign-in and its sign-up interstitial
// extension point, the web frontend's cookie-held tokens, the discovery documents and the proxied Hydra endpoints.
package api

import (
	"context"
	"embed"
	"html/template"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/auth/service"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// UserResponseEnricher adds extra fields to the /api/auth/v1/me response.
// Called with the base response map after the standard fields are set.
type UserResponseEnricher func(ctx context.Context, userID uuid.UUID, resp map[string]any) error

// FooterLink is a navigation link rendered in the footer of the server-rendered auth pages, next to the copyright
// notice — typically a privacy policy or terms of service page.
type FooterLink struct {
	Label string
	URL   string
}

// Handler holds dependencies for all auth HTTP handlers.
type Handler struct {
	authentication       *service.AuthenticationService
	user                 *service.UserService
	role                 *service.RoleService
	loginFlow            *service.LoginFlowService
	templates            *template.Template
	logger               zerolog.Logger
	userResponseEnricher UserResponseEnricher
	// webClientID is the OAuth2 client ID of the first-party SPA. When the
	// incoming login/consent challenge targets this client, the templates can
	// omit redundant "to continue to …" hints. Empty disables the check.
	webClientID string
	// publicURL is the public base URL of this service (e.g. http://localhost:8080).
	// Used by the landing page to display the MCP endpoint URL.
	publicURL string
	// oidcProviders is the list of registered external identity providers the login template renders buttons for,
	// when they offer one. Empty when no providers are configured — the template falls back to the plain email+password
	// form.
	oidcProviders []service.OIDCProvider
	// footerLinks is the list of links the auth pages render in their footer next to the copyright notice.
	// Empty renders the copyright notice alone.
	footerLinks []FooterLink
}

// SetUserResponseEnricher sets an optional enricher that adds extra fields
// to the /api/auth/v1/me response.
func (h *Handler) SetUserResponseEnricher(enricher UserResponseEnricher) {
	h.userResponseEnricher = enricher
}

// SetOIDCProviders registers the external identity providers whose buttons should appear on the login page. A provider
// that offers no login button gets none.
func (h *Handler) SetOIDCProviders(providers []service.OIDCProvider) {
	h.oidcProviders = providers
}

// SetFooterLinks registers the links the auth pages render in their footer (e.g. privacy policy, terms of service).
func (h *Handler) SetFooterLinks(links []FooterLink) {
	h.footerLinks = links
}

// NewHandler creates a new auth Handler, parsing embedded templates.
// webClientID is the OAuth2 client ID of the first-party SPA; pass "" to
// disable the first-party check. publicURL is the public base URL of this
// service (e.g. http://localhost:8080).
func NewHandler(
	authentication *service.AuthenticationService,
	user *service.UserService,
	role *service.RoleService,
	loginFlow *service.LoginFlowService,
	webClientID string,
	publicURL string,
	logger zerolog.Logger,
) *Handler {
	h := &Handler{
		authentication: authentication,
		user:           user,
		role:           role,
		loginFlow:      loginFlow,
		logger:         logger,
		webClientID:    webClientID,
		publicURL:      publicURL,
	}

	// The template funcs close over the handler so values registered after construction (footer links) are read at
	// render time, mirroring how oidcProviders flows into the login template.
	h.templates = template.Must(template.New("").Funcs(template.FuncMap{
		"currentYear": func() int { return time.Now().Year() },
		"footerLinks": func() []FooterLink { return h.footerLinks },
	}).ParseFS(templateFS, "templates/*.html"))

	return h
}

// isFirstPartyClient returns true when the given OAuth client ID matches the
// registered first-party SPA. Always returns false when webClientID is unset.
func (h *Handler) isFirstPartyClient(clientID string) bool {
	return h.webClientID != "" && clientID == h.webClientID
}

// renderError renders the error page with the given HTTP status and message.
func (h *Handler) renderError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	h.render(w, "error.html", errorPageData{Message: message})
}

// render executes a named template, logging on failure.
func (h *Handler) render(w http.ResponseWriter, name string, data any) {
	if err := h.templates.ExecuteTemplate(w, name, data); err != nil {
		h.logger.Error().Err(err).Str("template", name).Msg("failed to render template")
	}
}
