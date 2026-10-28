package hydrahttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
)

// PublicClient calls Hydra's public HTTP API: the token, revocation and dynamic client registration endpoints.
type PublicClient struct {
	baseURL string
	client  *http.Client
}

// NewPublicClient creates a new Hydra public API client.
func NewPublicClient(publicURL string) *PublicClient {
	return &PublicClient{
		baseURL: publicURL,
		client: &http.Client{
			Timeout:   httpTimeout,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}
}

// ExchangeToken sends tokenReq to Hydra's /oauth2/token endpoint and returns the tokens it issues.
func (c *PublicClient) ExchangeToken(ctx context.Context, tokenReq domain.TokenRequest) (*domain.TokenSet, error) {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.baseURL+"/oauth2/token", strings.NewReader(tokenForm(tokenReq).Encode()),
	)
	if err != nil {
		return nil, fmt.Errorf("creating token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling Hydra token endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading Hydra response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hydra returned %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("decoding token response: %w", err)
	}

	return tokenResp.toDomain(), nil
}

// RevokeToken invalidates a token via Hydra's /oauth2/revoke endpoint.
func (c *PublicClient) RevokeToken(ctx context.Context, token string) error {
	params := url.Values{"token": {token}}
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.baseURL+"/oauth2/revoke", strings.NewReader(params.Encode()),
	)
	if err != nil {
		return fmt.Errorf("creating revoke request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("calling Hydra revoke endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("hydra revoke returned %d", resp.StatusCode)
	}

	return nil
}

// RegisterClient performs dynamic client registration with Hydra's /oauth2/register endpoint.
// Returns the parsed response, HTTP status code, and any error.
func (c *PublicClient) RegisterClient(ctx context.Context, clientReq map[string]any) (map[string]any, int, error) {
	encoded, err := json.Marshal(clientReq)
	if err != nil {
		return nil, 0, fmt.Errorf("marshaling registration request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/oauth2/register", bytes.NewReader(encoded))
	if err != nil {
		return nil, 0, fmt.Errorf("creating registration request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("calling Hydra register endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading Hydra response: %w", err)
	}

	var respData map[string]any
	if err := json.Unmarshal(body, &respData); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("decoding registration response: %w", err)
	}

	return respData, resp.StatusCode, nil
}

// tokenResponse is the JSON structure returned by Hydra's token endpoint.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

func (r *tokenResponse) toDomain() *domain.TokenSet {
	return &domain.TokenSet{
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		TokenType:    r.TokenType,
		ExpiresIn:    r.ExpiresIn,
	}
}

// tokenForm encodes req as the form fields of a token endpoint request, leaving out the fields req leaves empty.
func tokenForm(req domain.TokenRequest) url.Values {
	form := url.Values{}
	for _, field := range []struct{ key, value string }{
		{"grant_type", req.GrantType},
		{"client_id", req.ClientID},
		{"code", req.Code},
		{"code_verifier", req.CodeVerifier},
		{"redirect_uri", req.RedirectURI},
		{"refresh_token", req.RefreshToken},
	} {
		if field.value != "" {
			form.Set(field.key, field.value)
		}
	}
	return form
}
