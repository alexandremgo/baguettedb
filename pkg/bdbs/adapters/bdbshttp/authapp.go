// Package bdbshttp implements the bdbs service's ports to the auth and databases apps with calls on those services'
// internal HTTP endpoints, for deployments running the modules as separate services. Each request carries the shared
// internal API secret.
package bdbshttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
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

type createDataAPIKeyRequest struct {
	UserID string `json:"user_id"`
	BdbID  string `json:"bdb_id"`
	Name   string `json:"name"`
}

type bdbDataAPIKeysRequest struct {
	BdbID string `json:"bdb_id"`
}

type listDataAPIKeysResponse struct {
	Keys []apptypes.DataAPIKey `json:"keys"`
}

type revokeDataAPIKeyRequest struct {
	BdbID string `json:"bdb_id"`
	KeyID string `json:"key_id"`
}

type revokeMemberDataAPIKeysRequest struct {
	BdbID  string `json:"bdb_id"`
	UserID string `json:"user_id"`
}

// CreateDataAPIKey calls the auth service's internal endpoint creating a Data API key. A 422, an invalid name, is a
// domain.ErrInvalidDataAPIKeyName wrapped with what the service says is wrong with it.
func (a *AuthApp) CreateDataAPIKey(
	ctx context.Context, userID, bdbID uuid.UUID, name string,
) (apptypes.CreatedDataAPIKey, error) {
	var created apptypes.CreatedDataAPIKey
	req := createDataAPIKeyRequest{UserID: userID.String(), BdbID: bdbID.String(), Name: name}
	err := a.postInternal(ctx, "/internal/api/auth/v1/data-api-keys/create", req, http.StatusCreated, &created)
	statusErr, ok := errors.AsType[*internalStatusError](err)
	if ok && statusErr.status == http.StatusUnprocessableEntity {
		return apptypes.CreatedDataAPIKey{}, fmt.Errorf("%w: %s", domain.ErrInvalidDataAPIKeyName, statusErr.message)
	}
	if err != nil {
		return apptypes.CreatedDataAPIKey{}, err
	}
	return created, nil
}

// ListDataAPIKeys calls the auth service's internal endpoint listing a bdb's Data API keys.
func (a *AuthApp) ListDataAPIKeys(ctx context.Context, bdbID uuid.UUID) ([]apptypes.DataAPIKey, error) {
	var listed listDataAPIKeysResponse
	if err := a.postInternal(ctx, "/internal/api/auth/v1/data-api-keys",
		bdbDataAPIKeysRequest{BdbID: bdbID.String()}, http.StatusOK, &listed); err != nil {
		return nil, err
	}
	return listed.Keys, nil
}

// RevokeDataAPIKey calls the auth service's internal endpoint revoking one Data API key. A 404 is a
// domain.ErrDataAPIKeyNotFound.
func (a *AuthApp) RevokeDataAPIKey(ctx context.Context, bdbID, keyID uuid.UUID) error {
	err := a.postInternal(ctx, "/internal/api/auth/v1/data-api-keys/revoke",
		revokeDataAPIKeyRequest{BdbID: bdbID.String(), KeyID: keyID.String()}, http.StatusNoContent, nil)
	if statusErr, ok := errors.AsType[*internalStatusError](err); ok && statusErr.status == http.StatusNotFound {
		return domain.ErrDataAPIKeyNotFound
	}
	return err
}

// RevokeDataAPIKeys calls the auth service's internal endpoint revoking every Data API key of a bdb.
func (a *AuthApp) RevokeDataAPIKeys(ctx context.Context, bdbID uuid.UUID) error {
	return a.postInternal(ctx, "/internal/api/auth/v1/data-api-keys/revoke-all",
		bdbDataAPIKeysRequest{BdbID: bdbID.String()}, http.StatusNoContent, nil)
}

// RevokeMemberDataAPIKeys calls the auth service's internal endpoint revoking the Data API keys a member created for
// a bdb.
func (a *AuthApp) RevokeMemberDataAPIKeys(ctx context.Context, bdbID, userID uuid.UUID) error {
	return a.postInternal(ctx, "/internal/api/auth/v1/data-api-keys/revoke-member",
		revokeMemberDataAPIKeysRequest{BdbID: bdbID.String(), UserID: userID.String()}, http.StatusNoContent, nil)
}

// internalStatusError is an answer of the auth service's internal endpoints with another status than expected, and
// the message its {"error": …} body carried, if any.
type internalStatusError struct {
	status  int
	message string
}

func (e *internalStatusError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("auth service returned status %d", e.status)
	}
	return fmt.Sprintf("auth service returned status %d: %s", e.status, e.message)
}

// postInternal POSTs body as JSON to the auth service's internal endpoint path, with the internal API secret. An
// answer with status want is decoded into out, unless out is nil; any other is an *internalStatusError.
func (a *AuthApp) postInternal(ctx context.Context, path string, body any, want int, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	authhttp.SetInternalSecretHeader(req.Header, a.internalAPISecret)

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("calling auth service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != want {
		var answer struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&answer)
		return &internalStatusError{status: resp.StatusCode, message: answer.Error}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}
