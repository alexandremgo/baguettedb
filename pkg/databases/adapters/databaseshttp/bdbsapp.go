// Package databaseshttp adapts the databases service's ports to internal HTTP calls on the other services, for
// deployments running the modules as separate services: the bdbs port (member permission checks, bdb ownership
// lookups, and the formulas of Data API keys) on the bdbs service, and the Data API key check on the auth service.
package databaseshttp

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

type checkPermissionRequest struct {
	UserID     string `json:"user_id"`
	BdbID      string `json:"bdb_id"`
	Permission string `json:"permission"`
}

type checkPermissionResponse struct {
	Allowed bool `json:"allowed"`
}

type bdbOwnerRequest struct {
	BdbID string `json:"bdb_id"`
}

type bdbOwnerResponse struct {
	OwnerUserID string   `json:"owner_user_id"`
	Roles       []string `json:"roles"`
}

type ownedBdbIDsRequest struct {
	UserID string `json:"user_id"`
}

type ownedBdbIDsResponse struct {
	BdbIDs []string `json:"bdb_ids"`
}

type checkDataAPIKeyFormulaRequest struct {
	KeyID     string `json:"key_id"`
	BdbID     string `json:"bdb_id"`
	FormulaID string `json:"formula_id"`
}

type dataAPIKeyFormulasRequest struct {
	KeyID string `json:"key_id"`
	BdbID string `json:"bdb_id"`
}

type dataAPIKeyFormulasResponse struct {
	FormulaIDs []string `json:"formula_ids"`
}

type formulaDataAPIKeysRequest struct {
	UserID string `json:"user_id"`
	BdbID  string `json:"bdb_id"`
}

type formulaDataAPIKeysResponse struct {
	Formulas map[string][]apptypes.DataAPIKey `json:"formulas"`
}

type forgetDataAPIKeyFormulaRequest struct {
	BdbID     string `json:"bdb_id"`
	FormulaID string `json:"formula_id"`
}

// BdbsApp implements BdbsApp by calling the bdbs service over HTTP.
// Used in standalone (microservice) mode where bdbs runs as a separate service.
type BdbsApp struct {
	baseURL           string
	internalAPISecret string
	client            *http.Client
}

// NewBdbsApp creates a new HTTP-based BdbsApp targeting the given bdbs service base URL.
func NewBdbsApp(baseURL, internalAPISecret string) *BdbsApp {
	return &BdbsApp{
		baseURL:           baseURL,
		internalAPISecret: internalAPISecret,
		client: &http.Client{
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}
}

// CheckMemberPermission calls the bdbs service's internal permission-check endpoint.
func (a *BdbsApp) CheckMemberPermission(
	ctx context.Context, userID, bdbID uuid.UUID, perm authz.DatabasePermission,
) (bool, error) {
	body, err := json.Marshal(checkPermissionRequest{
		UserID:     userID.String(),
		BdbID:      bdbID.String(),
		Permission: string(perm),
	})
	if err != nil {
		return false, fmt.Errorf("marshaling request: %w", err)
	}

	url := a.baseURL + "/internal/api/bdbs/v1/check-permission"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	authhttp.SetInternalSecretHeader(req.Header, a.internalAPISecret)

	resp, err := a.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("calling bdbs service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("bdbs service returned status %d", resp.StatusCode)
	}

	var result checkPermissionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("decoding response: %w", err)
	}

	return result.Allowed, nil
}

// GetBdbOwner calls the bdbs service's internal bdb-owner endpoint.
func (a *BdbsApp) GetBdbOwner(ctx context.Context, bdbID uuid.UUID) (uuid.UUID, []authz.Role, error) {
	body, err := json.Marshal(bdbOwnerRequest{
		BdbID: bdbID.String(),
	})
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("marshaling request: %w", err)
	}

	url := a.baseURL + "/internal/api/bdbs/v1/bdb-owner"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	authhttp.SetInternalSecretHeader(req.Header, a.internalAPISecret)

	resp, err := a.client.Do(req)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("calling bdbs service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return uuid.Nil, nil, fmt.Errorf("bdbs service returned status %d", resp.StatusCode)
	}

	var result bdbOwnerResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return uuid.Nil, nil, fmt.Errorf("decoding response: %w", err)
	}

	ownerID, err := uuid.Parse(result.OwnerUserID)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("parsing owner user id %q: %w", result.OwnerUserID, err)
	}

	roles := make([]authz.Role, len(result.Roles))
	for i, r := range result.Roles {
		roles[i] = authz.Role(r)
	}

	return ownerID, roles, nil
}

// GetOwnedBdbIDs calls the bdbs service's internal owned-bdb-ids endpoint.
func (a *BdbsApp) GetOwnedBdbIDs(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	body, err := json.Marshal(ownedBdbIDsRequest{
		UserID: userID.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	url := a.baseURL + "/internal/api/bdbs/v1/owned-bdb-ids"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	authhttp.SetInternalSecretHeader(req.Header, a.internalAPISecret)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling bdbs service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bdbs service returned status %d", resp.StatusCode)
	}

	var result ownedBdbIDsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	ids := make([]uuid.UUID, len(result.BdbIDs))
	for i, raw := range result.BdbIDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("parsing bdb id %q: %w", raw, err)
		}
		ids[i] = id
	}

	return ids, nil
}

// CheckDataAPIKeyFormula calls the bdbs service's internal endpoint checking that a Data API key may run a formula.
func (a *BdbsApp) CheckDataAPIKeyFormula(
	ctx context.Context, keyID, bdbID uuid.UUID, formulaID string,
) (bool, error) {
	var result checkPermissionResponse
	err := a.postInternal(ctx, "/internal/api/bdbs/v1/data-api-key-formulas/check",
		checkDataAPIKeyFormulaRequest{KeyID: keyID.String(), BdbID: bdbID.String(), FormulaID: formulaID},
		http.StatusOK, &result)
	if err != nil {
		return false, err
	}
	return result.Allowed, nil
}

// ListDataAPIKeyFormulaIDs calls the bdbs service's internal endpoint listing the formulas a Data API key may run.
func (a *BdbsApp) ListDataAPIKeyFormulaIDs(ctx context.Context, keyID, bdbID uuid.UUID) ([]string, error) {
	var result dataAPIKeyFormulasResponse
	if err := a.postInternal(ctx, "/internal/api/bdbs/v1/data-api-key-formulas",
		dataAPIKeyFormulasRequest{KeyID: keyID.String(), BdbID: bdbID.String()}, http.StatusOK, &result); err != nil {
		return nil, err
	}
	return result.FormulaIDs, nil
}

// ForgetDataAPIKeyFormula calls the bdbs service's internal endpoint forgetting a formula of every Data API key.
func (a *BdbsApp) ForgetDataAPIKeyFormula(ctx context.Context, bdbID uuid.UUID, formulaID string) error {
	return a.postInternal(ctx, "/internal/api/bdbs/v1/data-api-key-formulas/forget",
		forgetDataAPIKeyFormulaRequest{BdbID: bdbID.String(), FormulaID: formulaID}, http.StatusNoContent, nil)
}

// ListFormulaDataAPIKeys calls the bdbs service's internal endpoint listing a bdb's Data API keys by formula, as the
// member userID may see them.
func (a *BdbsApp) ListFormulaDataAPIKeys(
	ctx context.Context, userID, bdbID uuid.UUID,
) (map[string][]apptypes.DataAPIKey, error) {
	var result formulaDataAPIKeysResponse
	if err := a.postInternal(ctx, "/internal/api/bdbs/v1/formula-data-api-keys",
		formulaDataAPIKeysRequest{UserID: userID.String(), BdbID: bdbID.String()}, http.StatusOK, &result); err != nil {
		return nil, err
	}
	return result.Formulas, nil
}

// postInternal POSTs body as JSON to the bdbs service's internal endpoint path, with the internal API secret. An
// answer with status want is decoded into out, unless out is nil; any other is an error naming the status.
func (a *BdbsApp) postInternal(ctx context.Context, path string, body any, want int, out any) error {
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
		return fmt.Errorf("calling bdbs service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != want {
		return fmt.Errorf("bdbs service returned status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}
