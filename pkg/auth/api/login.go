package api

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
)

// loginButton is the login-page presentation an OIDC provider may offer on top of its sign-in routes. A provider
// without it gets no button, while its start route still serves direct links.
type loginButton interface {
	// DisplayName is the human-readable provider label the button template receives as ButtonData.DisplayName.
	// Templates that bake their own label may ignore it.
	DisplayName() string

	// ButtonTemplate returns the html/template rendering the provider's login button. It executes with ButtonData and
	// must produce a complete clickable element (typically <a href="{{.Href}}">...</a>). A nil template renders no
	// button.
	ButtonTemplate() *template.Template

	// Stylesheet returns the CSS served at /static/oidc/{name}.css and linked from the login page <head>. Nil serves
	// none, for a template that inlines its own styles or needs no styling.
	Stylesheet() []byte
}

// ButtonData carries the values a provider's button template renders with.
type ButtonData struct {
	// Href is the provider's sign-in start URL, login_challenge query parameter included.
	Href string
	// DisplayName is the provider's human-readable label.
	DisplayName string
}

// GetLogin handles the login page render. Hydra redirects the browser here.
func (h *Handler) GetLogin(w http.ResponseWriter, r *http.Request) {
	challenge := r.URL.Query().Get("login_challenge")
	if challenge == "" {
		h.renderError(w, http.StatusBadRequest,
			"This sign-in link looks incomplete. Try signing in again from the app you came from.")
		return
	}

	loginReq, redirectTo, err := h.loginFlow.StartLogin(r.Context(), challenge)
	if err != nil {
		h.logger.Error().Err(err).Msg("failed to start login")
		h.renderError(w, http.StatusInternalServerError,
			"Something went wrong on our end. Please try again in a moment.")
		return
	}
	if redirectTo != "" {
		http.Redirect(w, r, redirectTo, http.StatusFound)
		return
	}

	h.render(w, "login.html", loginPageData{
		Challenge:    challenge,
		ClientName:   loginReq.ClientName,
		IsFirstParty: h.isFirstPartyClient(loginReq.ClientID),
		Providers:    h.providerButtons(challenge),
	})
}

// providerButtons projects the registered OIDC providers offering a login button into pre-rendered button HTML for the
// login template. Returns nil when no providers are registered so the template renders only the email+password form.
//
// For each provider, ButtonTemplate is executed once against ButtonData{Href, DisplayName}; the captured output is
// passed through as template.HTML so the outer template inlines it without re-escaping. A provider without a login
// button is skipped; one that returns a nil template or whose template execution errors is logged and skipped. The rest
// of the page still renders.
func (h *Handler) providerButtons(challenge string) []providerButton {
	if len(h.oidcProviders) == 0 {
		return nil
	}
	out := make([]providerButton, 0, len(h.oidcProviders))
	for _, p := range h.oidcProviders {
		button, ok := p.(loginButton)
		if !ok {
			continue
		}

		tpl := button.ButtonTemplate()
		if tpl == nil {
			h.logger.Warn().Str("provider", p.Name()).Msg("OIDC provider has no button template; skipping")
			continue
		}
		href := fmt.Sprintf("/login/oidc/%s/start?login_challenge=%s", p.Name(), url.QueryEscape(challenge))
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, ButtonData{Href: href, DisplayName: button.DisplayName()}); err != nil {
			h.logger.Error().Err(err).Str("provider", p.Name()).Msg("rendering OIDC provider button")
			continue
		}
		out = append(out, providerButton{
			Name:          p.Name(),
			RenderedHTML:  template.HTML(buf.String()),
			HasStylesheet: len(button.Stylesheet()) > 0,
		})
	}
	return out
}

// PostLogin handles the login form submission.
func (h *Handler) PostLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderError(w, http.StatusBadRequest, "We couldn't read your submission. Please refresh and try again.")
		return
	}

	challenge := r.FormValue("challenge")
	email := r.FormValue("email")
	password := r.FormValue("password")

	if challenge == "" {
		h.renderError(w, http.StatusBadRequest,
			"This sign-in link looks incomplete. Try signing in again from the app you came from.")
		return
	}

	user, err := h.authentication.Authenticate(r.Context(), email, password)
	if err != nil {
		// Re-renders the login form with an error message.
		loginReq, reqErr := h.loginFlow.LoginRequest(r.Context(), challenge)
		clientName := ""
		isFirstParty := false
		if reqErr == nil {
			clientName = loginReq.ClientName
			isFirstParty = h.isFirstPartyClient(loginReq.ClientID)
		}

		w.WriteHeader(http.StatusUnauthorized)
		h.render(w, "login.html", loginPageData{
			Challenge:    challenge,
			ClientName:   clientName,
			IsFirstParty: isFirstParty,
			Error:        "Invalid email or password",
			Providers:    h.providerButtons(challenge),
		})
		return
	}

	redirectTo, err := h.loginFlow.CompleteLogin(r.Context(), challenge, user.ID)
	if err != nil {
		h.logger.Error().Err(err).Msg("failed to accept login")
		h.renderError(w, http.StatusInternalServerError,
			"Something went wrong on our end. Please try again in a moment.")
		return
	}

	http.Redirect(w, r, redirectTo, http.StatusFound)
}
