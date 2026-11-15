package databaseshttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

type checkDataAPIKeyRequest struct {
	Key string `json:"key"`
}

type checkDataAPIKeyResponse struct {
	UserID uuid.UUID `json:"user_id"`
	Roles  []string  `json:"roles"`
	KeyID  uuid.UUID `json:"key_id"`
	BdbID  uuid.UUID `json:"bdb_id"`
}

// AuthApp checks Data API keys by calling the auth service over HTTP.
type AuthApp struct {
	baseURL           string
	internalAPISecret string
	client            *http.Client
}

// NewAuthApp creates a new HTTP-based AuthApp targeting the given auth service base URL.
func NewAuthApp(baseURL, internalAPISecret string) *AuthApp {
	return &AuthApp{
		baseURL:           baseURL,
		internalAPISecret: internalAPISecret,
		client: &http.Client{
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}
}

// CheckDataAPIKey calls the auth service's internal endpoint checking a Data API key. A 401, a key that authenticates
// nothing, is authhttp.ErrInvalidToken.
func (a *AuthApp) CheckDataAPIKey(ctx context.Context, key string) (authhttp.DataAPIKeyIdentity, error) {
	body, err := json.Marshal(checkDataAPIKeyRequest{Key: key})
	if err != nil {
		return authhttp.DataAPIKeyIdentity{}, fmt.Errorf("marshaling request: %w", err)
	}

	url := a.baseURL + "/internal/api/auth/v1/data-api-keys/check"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return authhttp.DataAPIKeyIdentity{}, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	authhttp.SetInternalSecretHeader(req.Header, a.internalAPISecret)

	resp, err := a.client.Do(req)
	if err != nil {
		return authhttp.DataAPIKeyIdentity{}, fmt.Errorf("calling auth service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return authhttp.DataAPIKeyIdentity{}, authhttp.ErrInvalidToken
	default:
		return authhttp.DataAPIKeyIdentity{}, fmt.Errorf("auth service returned status %d", resp.StatusCode)
	}

	var result checkDataAPIKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return authhttp.DataAPIKeyIdentity{}, fmt.Errorf("decoding response: %w", err)
	}
	roles := make([]authz.Role, len(result.Roles))
	for i, role := range result.Roles {
		roles[i] = authz.Role(role)
	}
	return authhttp.DataAPIKeyIdentity{
		UserID: result.UserID, Roles: roles, KeyID: result.KeyID, BdbID: result.BdbID,
	}, nil
}
