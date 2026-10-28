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
)

type tablesWithSchemasRequest struct {
	UserID string `json:"user_id"`
	BdbID  string `json:"bdb_id"`
}

type bdbDatabaseRequest struct {
	BdbID string `json:"bdb_id"`
}

// DatabasesApp resolves table schemas by calling the databases service over HTTP.
// Used in standalone (microservice) mode where databases runs as a separate service.
type DatabasesApp struct {
	baseURL           string
	internalAPISecret string
	client            *http.Client
}

// NewDatabasesApp creates a new HTTP-based DatabasesApp targeting the given databases service base URL.
func NewDatabasesApp(baseURL, internalAPISecret string) *DatabasesApp {
	return &DatabasesApp{
		baseURL:           baseURL,
		internalAPISecret: internalAPISecret,
		client: &http.Client{
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}
}

// ListTables calls the databases service's internal tables endpoint.
func (a *DatabasesApp) ListTables(ctx context.Context, userID, bdbID uuid.UUID) ([]apptypes.TableInfo, error) {
	var infos []apptypes.TableInfo
	if err := a.postInternal(ctx, "/internal/api/databases/v1/tables", userID, bdbID, &infos); err != nil {
		return nil, err
	}
	return infos, nil
}

// ListTablesWithSchemas calls the databases service's internal tables-with-schemas endpoint.
func (a *DatabasesApp) ListTablesWithSchemas(
	ctx context.Context, userID, bdbID uuid.UUID,
) ([]apptypes.TableSchema, error) {
	var schemas []apptypes.TableSchema
	if err := a.postInternal(
		ctx, "/internal/api/databases/v1/tables-with-schemas", userID, bdbID, &schemas,
	); err != nil {
		return nil, err
	}
	return schemas, nil
}

// ListSkills calls the databases service's internal skills endpoint.
func (a *DatabasesApp) ListSkills(ctx context.Context, userID, bdbID uuid.UUID) ([]apptypes.Skill, error) {
	var skills []apptypes.Skill
	if err := a.postInternal(ctx, "/internal/api/databases/v1/skills", userID, bdbID, &skills); err != nil {
		return nil, err
	}
	return skills, nil
}

// ListFormulas calls the databases service's internal formulas endpoint.
func (a *DatabasesApp) ListFormulas(ctx context.Context, userID, bdbID uuid.UUID) ([]apptypes.Formula, error) {
	var formulas []apptypes.Formula
	if err := a.postInternal(ctx, "/internal/api/databases/v1/formulas", userID, bdbID, &formulas); err != nil {
		return nil, err
	}
	return formulas, nil
}

// DeleteDatabase calls the databases service's internal endpoint that removes the per-bdb SQLite database.
// Idempotent server-side: a 204 is returned whether or not a database existed for the bdb.
func (a *DatabasesApp) DeleteDatabase(ctx context.Context, bdbID uuid.UUID) error {
	body, err := json.Marshal(bdbDatabaseRequest{BdbID: bdbID.String()})
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	url := a.baseURL + "/internal/api/databases/v1/bdb-database/delete"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	authhttp.SetInternalSecretHeader(req.Header, a.internalAPISecret)

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("calling databases service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("databases service returned status %d", resp.StatusCode)
	}
	return nil
}

// postInternal POSTs the standard {user_id, bdb_id} body to the given path
// and decodes the JSON response into out.
func (a *DatabasesApp) postInternal(ctx context.Context, path string, userID, bdbID uuid.UUID, out any) error {
	body, err := json.Marshal(tablesWithSchemasRequest{
		UserID: userID.String(),
		BdbID:  bdbID.String(),
	})
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	url := a.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	authhttp.SetInternalSecretHeader(req.Header, a.internalAPISecret)

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("calling databases service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("databases service returned status %d", resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}
