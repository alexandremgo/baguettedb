package domain

import "time"

// OIDCFlowState is the server-side record binding one external sign-in round-trip (start → IdP → callback) together.
// It is keyed by the random OAuth state value, which the identity provider echoes back in the callback query string,
// so the callback can recover the flow context without relying on any browser-side storage surviving the redirect
// chain. It does not itself prove the callback came from the browser that started the flow: that binding is
// re-established downstream, when the post-login redirect carrying the LoginChallenge's verifier is validated by
// Hydra against the CSRF session cookie it planted in the browser before the flow reached this record.
type OIDCFlowState struct {
	State          string
	Provider       string
	LoginChallenge string
	Nonce          string
	CodeVerifier   string
	CreatedAt      time.Time
	ExpiresAt      time.Time
}
