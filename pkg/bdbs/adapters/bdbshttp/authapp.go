// Package bdbshttp implements the bdbs service's ports to the auth and databases apps with calls on those services'
// internal HTTP endpoints, for deployments running the modules as separate services. Each request carries the shared
// internal API secret.
package bdbshttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

type getUserRolesRequest struct {
	UserID string `json:"user_id"`
}

type getUserRolesResponse struct {
	Roles []string `json:"roles"`
}

type getUserProfilesRequest struct {
	UserIDs []string `json:"user_ids"`
}

type getUserProfilesResponse struct {
	Profiles []apptypes.UserProfile `json:"profiles"`
}

// AuthApp resolves user roles by calling the auth service over HTTP.
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

// GetUserProfiles calls the auth service's internal user-profiles endpoint and
// returns a map keyed by user ID. Unknown IDs are absent from the result.
func (a *AuthApp) GetUserProfiles(
	ctx context.Context, userIDs []uuid.UUID,
) (map[uuid.UUID]*apptypes.UserProfile, error) {
	if len(userIDs) == 0 {
		return map[uuid.UUID]*apptypes.UserProfile{}, nil
	}

	idStrings := make([]string, len(userIDs))
	for i, id := range userIDs {
		idStrings[i] = id.String()
	}

	body, err := json.Marshal(getUserProfilesRequest{UserIDs: idStrings})
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	url := a.baseURL + "/internal/api/auth/v1/user-profiles"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	authhttp.SetInternalSecretHeader(req.Header, a.internalAPISecret)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling auth service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth service returned status %d", resp.StatusCode)
	}

	var result getUserProfilesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	out := make(map[uuid.UUID]*apptypes.UserProfile, len(result.Profiles))
	for i := range result.Profiles {
		out[result.Profiles[i].UserID] = &result.Profiles[i]
	}
	return out, nil
}

// GetUserRoles calls the auth service's internal user-roles endpoint.
func (a *AuthApp) GetUserRoles(ctx context.Context, userID uuid.UUID) ([]authz.Role, error) {
	body, err := json.Marshal(getUserRolesRequest{
		UserID: userID.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	url := a.baseURL + "/internal/api/auth/v1/user-roles"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	authhttp.SetInternalSecretHeader(req.Header, a.internalAPISecret)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling auth service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth service returned status %d", resp.StatusCode)
	}

	var result getUserRolesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	roles := make([]authz.Role, len(result.Roles))
	for i, r := range result.Roles {
		roles[i] = authz.Role(r)
	}

	return roles, nil
}
