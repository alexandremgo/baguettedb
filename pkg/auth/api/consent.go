package api

import (
	"net/http"
)

// GetConsent handles the consent page render. Hydra redirects the browser here.
func (h *Handler) GetConsent(w http.ResponseWriter, r *http.Request) {
	challenge := r.URL.Query().Get("consent_challenge")
	if challenge == "" {
		h.renderError(w, http.StatusBadRequest,
			"This sign-in link looks incomplete. Try signing in again from the app you came from.")
		return
	}

	consentReq, redirectTo, err := h.loginFlow.StartConsent(r.Context(), challenge)
	if err != nil {
		h.logger.Error().Err(err).Msg("failed to start consent")
		h.renderError(w, http.StatusInternalServerError,
			"Something went wrong on our end. Please try again in a moment.")
		return
	}
	if redirectTo != "" {
		http.Redirect(w, r, redirectTo, http.StatusFound)
		return
	}

	h.render(w, "consent.html", consentPageData{
		Challenge:  challenge,
		ClientName: consentReq.ClientName,
		Scopes:     toScopeViews(consentReq.RequestedScopes),
	})
}

// PostConsent handles the consent form submission.
func (h *Handler) PostConsent(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderError(w, http.StatusBadRequest, "We couldn't read your submission. Please refresh and try again.")
		return
	}

	challenge := r.FormValue("challenge")
	action := r.FormValue("action")

	if challenge == "" {
		h.renderError(w, http.StatusBadRequest,
			"This sign-in link looks incomplete. Try signing in again from the app you came from.")
		return
	}

	if action == "deny" {
		redirectTo, err := h.loginFlow.RejectConsent(r.Context(), challenge)
		if err != nil {
			h.logger.Error().Err(err).Msg("failed to reject consent")
			h.renderError(w, http.StatusInternalServerError,
				"Something went wrong on our end. Please try again in a moment.")
			return
		}
		http.Redirect(w, r, redirectTo, http.StatusFound)
		return
	}

	redirectTo, err := h.loginFlow.GrantConsent(r.Context(), challenge, r.Form["grant_scope"])
	if err != nil {
		h.logger.Error().Err(err).Msg("failed to accept consent")
		h.renderError(w, http.StatusInternalServerError,
			"Something went wrong on our end. Please try again in a moment.")
		return
	}

	http.Redirect(w, r, redirectTo, http.StatusFound)
}
