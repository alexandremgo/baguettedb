package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"
)

// SignupContext carries the verified identity-provider claims into the signup-interstitial flow. The handler uses
// these to prefill its UI; the closure passed to CompletionAPI.CompleteSignup ultimately decides which fields land in
// the new user row.
type SignupContext struct {
	// Challenge is the original login_challenge that started the Hydra flow. Forwarded into AcceptLogin once the
	// signup completes.
	Challenge string
	// Provider is the Name() of the identity provider the user authenticated with (e.g. "google").
	Provider string
	// Subject is the identity provider's stable subject identifier — used as the (provider, subject) link key.
	Subject string
	// Email, FirstName, and LastName are the verified profile fields from the identity provider's ID token.
	Email     string
	FirstName string
	LastName  string
}

// SignupInterstitialHandler is the extension point invoked when an OIDC callback succeeds but no local user is linked
// or matched by email. Implementations render whatever UI / validation flow the deployment needs (invite-code form,
// terms-of-service prompt, …) and ultimately call CompletionAPI.CompleteSignup once they are ready to create the
// user.
type SignupInterstitialHandler interface {
	RenderInterstitial(w http.ResponseWriter, r *http.Request, ctx SignupContext, completion CompletionAPI)
}

// CompletionAPI is the per-request handle the signup-interstitial flow uses to finalize a new account. The closure
// passed to CompleteSignup creates the account, in whatever transaction it opens itself; on success, CompleteSignup
// calls hydra.AcceptLogin with the closure's returned user ID and writes the resulting redirect to the response.
type CompletionAPI interface {
	CompleteSignup(
		ctx context.Context, w http.ResponseWriter, r *http.Request, do func(ctx context.Context) (uuid.UUID, error),
	) error
}

// completionAPI implements CompletionAPI. Bound to a specific login_challenge at construction so the eventual
// login completion knows which login request to accept.
type completionAPI struct {
	logins    loginCompleter
	challenge string
}

// loginCompleter accepts a login request for the user a sign-up created.
type loginCompleter interface {
	// CompleteLogin accepts the login request challenge identifies for userID and returns where to send the browser.
	CompleteLogin(ctx context.Context, challenge string, userID uuid.UUID) (string, error)
}

// NewCompletionAPI returns a CompletionAPI bound to the given login_challenge. Exported so the asynchronous
// signup-completion path (where the interstitial renders an HTML form whose POST hits a separate handler) can
// reconstruct a CompletionAPI keyed to the original challenge, without the framework holding per-request state
// between the two hops.
func NewCompletionAPI(logins loginCompleter, challenge string) CompletionAPI {
	return &completionAPI{logins: logins, challenge: challenge}
}

// CompleteSignup runs the closure and then accepts the login for the user it created and redirects. A non-nil error
// from the closure skips the login entirely.
func (c *completionAPI) CompleteSignup(
	ctx context.Context, w http.ResponseWriter, r *http.Request, do func(ctx context.Context) (uuid.UUID, error),
) error {
	userID, err := do(ctx)
	if err != nil {
		return fmt.Errorf("creating the signed-up account: %w", err)
	}

	redirectTo, err := c.logins.CompleteLogin(ctx, c.challenge, userID)
	if err != nil {
		return fmt.Errorf("accepting login after signup: %w", err)
	}

	http.Redirect(w, r, redirectTo, http.StatusFound)
	return nil
}
