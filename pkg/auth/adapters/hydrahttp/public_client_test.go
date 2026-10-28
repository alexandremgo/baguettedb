package hydrahttp

import (
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
)

// TestTokenForm checks that tokenForm encodes each grant as the token endpoint's form fields: an authorization code
// exchange and a refresh carry their own fields, and a field the request leaves empty is not sent at all.
func TestTokenForm(t *testing.T) {
	tests := []struct {
		name string
		req  domain.TokenRequest
		want string
	}{
		{
			name: "authorization code exchange",
			req: domain.TokenRequest{
				GrantType: "authorization_code", ClientID: "web", Code: "abc", CodeVerifier: "v3r",
				RedirectURI: "https://app.example/callback",
			},
			want: "client_id=web&code=abc&code_verifier=v3r&grant_type=authorization_code" +
				"&redirect_uri=https%3A%2F%2Fapp.example%2Fcallback",
		},
		{
			name: "refresh",
			req:  domain.TokenRequest{GrantType: "refresh_token", ClientID: "web", RefreshToken: "r1"},
			want: "client_id=web&grant_type=refresh_token&refresh_token=r1",
		},
		{
			name: "nothing set",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokenForm(tt.req)
			if encoded := got.Encode(); encoded != tt.want {
				t.Errorf("tokenForm(%+v).Encode() = %q, want %q", tt.req, encoded, tt.want)
			}
			for key, values := range got {
				if len(values) != 1 || values[0] == "" {
					t.Errorf("field %q = %v, want exactly one non-empty value", key, values)
				}
			}
		})
	}
}
