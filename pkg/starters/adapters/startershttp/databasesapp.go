// Package startershttp adapts the starters service ports to internal HTTP calls on the databases service, for
// deployments running the modules as separate services.
package startershttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
)

type executeSchemaRequest struct {
	UserID     string   `json:"user_id"`
	BdbID      string   `json:"bdb_id"`
	Statements []string `json:"statements"`
}

// executeDataRequest is one data statement with its positional bound values, typed the way the public
// execute endpoint types them.
type executeDataRequest struct {
	UserID string         `json:"user_id"`
	BdbID  string         `json:"bdb_id"`
	SQL    string         `json:"sql"`
	Args   []valueRequest `json:"args,omitempty"`
}

type valueRequest struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}

type createFormulaRequest struct {
	UserID              string         `json:"user_id"`
	BdbID               string         `json:"bdb_id"`
	Name                string         `json:"name"`
	Label               string         `json:"label,omitempty"`
	Description         string         `json:"description"`
	SQL                 string         `json:"sql"`
	Params              []paramRequest `json:"params,omitempty"`
	AttachedToFormulaID *string        `json:"attached_to_formula_id,omitempty"`
	Hidden              bool           `json:"hidden,omitempty"`
}

type updateFormulaRequest struct {
	UserID  string          `json:"user_id"`
	BdbID   string          `json:"bdb_id"`
	Ref     string          `json:"ref"`
	Display *displayRequest `json:"display"`
}

type displayRequest struct {
	Mode    string                 `json:"mode"`
	Columns []displayColumnRequest `json:"columns,omitempty"`
	Slots   *slotsRequest          `json:"slots,omitempty"`
	Window  *windowRequest         `json:"window,omitempty"`
}

type windowRequest struct {
	WeekStart string `json:"week_start,omitempty"`
	WeekEnd   string `json:"week_end,omitempty"`
	Start     string `json:"start,omitempty"`
	Days      int    `json:"days,omitempty"`
}

type displayColumnRequest struct {
	Column string `json:"column"`
	Role   string `json:"role,omitempty"`
	Format string `json:"format,omitempty"`
	Hidden bool   `json:"hidden,omitempty"`
}

type slotsRequest struct {
	Add        string   `json:"add,omitempty"`
	Toggle     string   `json:"toggle,omitempty"`
	Edit       string   `json:"edit,omitempty"`
	Delete     string   `json:"delete,omitempty"`
	Detail     string   `json:"detail,omitempty"`
	Reorder    string   `json:"reorder,omitempty"`
	Move       string   `json:"move,omitempty"`
	Bulk       []string `json:"bulk,omitempty"`
	RowActions []string `json:"row_actions,omitempty"`
}

type paramRequest struct {
	Name      string          `json:"name"`
	Type      string          `json:"type"`
	Source    string          `json:"source"`
	Optional  bool            `json:"optional,omitempty"`
	Lookup    *lookupRequest  `json:"lookup,omitempty"`
	Hint      string          `json:"hint,omitempty"`
	Multiline bool            `json:"multiline,omitempty"`
	Inline    bool            `json:"inline,omitempty"`
	Multiple  bool            `json:"multiple,omitempty"`
	Options   []optionRequest `json:"options,omitempty"`
	Default   string          `json:"default,omitempty"`
	Defaults  []string        `json:"defaults,omitempty"`
	When      *whenRequest    `json:"when,omitempty"`
	Fields    []paramRequest  `json:"fields,omitempty"`
}

// optionRequest is one closed-set entry, sent in the normalized {value, label} object form.
type optionRequest struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type lookupRequest struct {
	Table            string            `json:"table"`
	LabelColumn      string            `json:"label_column"`
	ValueColumn      string            `json:"value_column"`
	ExcludeSelection bool              `json:"exclude_selection,omitempty"`
	AllowNew         bool              `json:"allow_new,omitempty"`
	Scope            map[string]string `json:"scope,omitempty"`
	Fill             map[string]string `json:"fill,omitempty"`
}

type whenRequest struct {
	Param string `json:"param"`
	Op    string `json:"op,omitempty"`
	Value string `json:"value"`
}

type createFormulaResponse struct {
	ID string `json:"id"`
}

type createSkillRequest struct {
	UserID      string   `json:"user_id"`
	BdbID       string   `json:"bdb_id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Body        string   `json:"body"`
	Targets     []string `json:"targets,omitempty"`
}

type deleteDatabaseRequest struct {
	BdbID string `json:"bdb_id"`
}

// DatabasesApp seeds bdbs by calling the databases service's internal endpoints over HTTP. Used in standalone
// (microservice) mode where databases runs as a separate service.
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

// ExecuteSchema calls the databases service's internal schema-execution endpoint.
func (a *DatabasesApp) ExecuteSchema(ctx context.Context, userID, bdbID uuid.UUID, statements []string) error {
	req := executeSchemaRequest{UserID: userID.String(), BdbID: bdbID.String(), Statements: statements}
	return a.postInternal(ctx, "/internal/api/databases/v1/schema/execute", req, http.StatusNoContent, nil)
}

// InsertRows calls the databases service's internal data-execution endpoint with the seed's multi-row INSERT.
func (a *DatabasesApp) InsertRows(ctx context.Context, userID, bdbID uuid.UUID, seed domain.StarterSeed) error {
	sql, args := seed.InsertStatement()
	req := executeDataRequest{
		UserID: userID.String(), BdbID: bdbID.String(), SQL: sql, Args: make([]valueRequest, len(args)),
	}
	for i, v := range args {
		switch v := v.(type) {
		case nil:
			req.Args[i] = valueRequest{Type: "null"}
		case string:
			req.Args[i] = valueRequest{Type: "text", Value: v}
		case bool:
			n := 0
			if v {
				n = 1
			}
			req.Args[i] = valueRequest{Type: "integer", Value: n}
		case int, int64:
			req.Args[i] = valueRequest{Type: "integer", Value: v}
		case float64:
			req.Args[i] = valueRequest{Type: "float", Value: v}
		default:
			return fmt.Errorf("seed value %d: unsupported %T", i, v)
		}
	}
	return a.postInternal(ctx, "/internal/api/databases/v1/data/execute", req, http.StatusNoContent, nil)
}

// CreateFormula calls the databases service's internal create-formula endpoint and returns the new formula's
// ID. attachedToID carries the resolved attach target; the formula's AttachedTo slug is not interpreted here.
func (a *DatabasesApp) CreateFormula(
	ctx context.Context, userID, bdbID uuid.UUID,
	formula domain.StarterFormula, attachedToID *uuid.UUID,
) (uuid.UUID, error) {
	req := createFormulaRequest{
		UserID:      userID.String(),
		BdbID:       bdbID.String(),
		Name:        formula.Name,
		Label:       formula.Label,
		Description: formula.Description,
		SQL:         formula.SQL,
		Params:      toParamRequests(formula.Params),
		Hidden:      formula.Hidden,
	}
	if attachedToID != nil {
		s := attachedToID.String()
		req.AttachedToFormulaID = &s
	}

	var resp createFormulaResponse
	if err := a.postInternal(
		ctx, "/internal/api/databases/v1/formulas/create", req, http.StatusOK, &resp,
	); err != nil {
		return uuid.Nil, err
	}
	id, err := uuid.Parse(resp.ID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("parsing created formula ID %q: %w", resp.ID, err)
	}
	return id, nil
}

// UpdateFormulaDisplay calls the databases service's internal update-formula endpoint to store the formula's
// display config. Slot refs may be sibling formula names — the service resolves them where the update runs.
func (a *DatabasesApp) UpdateFormulaDisplay(
	ctx context.Context, userID, bdbID uuid.UUID, ref string, display domain.StarterFormulaDisplay,
) error {
	req := updateFormulaRequest{
		UserID: userID.String(),
		BdbID:  bdbID.String(),
		Ref:    ref,
		Display: &displayRequest{
			Mode:    display.Mode,
			Columns: toColumnRequests(display.Columns),
			Slots: &slotsRequest{
				Add:        display.Slots.Add,
				Toggle:     display.Slots.Toggle,
				Edit:       display.Slots.Edit,
				Delete:     display.Slots.Delete,
				Detail:     display.Slots.Detail,
				Reorder:    display.Slots.Reorder,
				Move:       display.Slots.Move,
				Bulk:       display.Slots.Bulk,
				RowActions: display.Slots.RowActions,
			},
		},
	}
	if w := display.Window; w != nil {
		req.Display.Window = &windowRequest{WeekStart: w.WeekStart, WeekEnd: w.WeekEnd, Start: w.Start, Days: w.Days}
	}
	return a.postInternal(ctx, "/internal/api/databases/v1/formulas/update", req, http.StatusNoContent, nil)
}

// CreateSkill calls the databases service's internal create-skill endpoint.
func (a *DatabasesApp) CreateSkill(ctx context.Context, userID, bdbID uuid.UUID, skill domain.StarterSkill) error {
	req := createSkillRequest{
		UserID:      userID.String(),
		BdbID:       bdbID.String(),
		Name:        skill.Name,
		Description: skill.Description,
		Body:        skill.Body,
		Targets:     skill.Targets,
	}
	return a.postInternal(ctx, "/internal/api/databases/v1/skills/create", req, http.StatusNoContent, nil)
}

// DeleteDatabase calls the databases service's internal endpoint that removes the per-bdb SQLite database.
// Idempotent server-side: a 204 is returned whether or not a database existed for the bdb.
func (a *DatabasesApp) DeleteDatabase(ctx context.Context, bdbID uuid.UUID) error {
	req := deleteDatabaseRequest{BdbID: bdbID.String()}
	return a.postInternal(ctx, "/internal/api/databases/v1/bdb-database/delete", req, http.StatusNoContent, nil)
}

// postInternal POSTs body to the given path with the internal secret set, expects wantStatus, and decodes the
// JSON response into out when out is non-nil.
func (a *DatabasesApp) postInternal(ctx context.Context, path string, body any, wantStatus int, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+path, bytes.NewReader(payload))
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

	if resp.StatusCode != wantStatus {
		return statusError(resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}
	return nil
}

// statusError turns an unexpected internal response into an error, carrying the server's error message when the
// body holds one.
func statusError(resp *http.Response) error {
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err == nil {
		if body.Error != "" {
			return fmt.Errorf("databases service returned status %d: %s", resp.StatusCode, body.Error)
		}
	}

	return fmt.Errorf("databases service returned status %d", resp.StatusCode)
}

// toColumnRequests converts starter display column entries into the wire payload.
func toColumnRequests(columns []domain.StarterDisplayColumn) []displayColumnRequest {
	if len(columns) == 0 {
		return nil
	}
	out := make([]displayColumnRequest, len(columns))
	for i, c := range columns {
		out[i] = displayColumnRequest(c)
	}
	return out
}

// toParamRequests converts starter formula params into the wire parameter payload.
func toParamRequests(params []domain.StarterFormulaParam) []paramRequest {
	if len(params) == 0 {
		return nil
	}
	out := make([]paramRequest, len(params))
	for i, p := range params {
		out[i] = toParamRequest(p)
	}
	return out
}

// toParamRequest converts one starter parameter — group fields included — into its wire shape.
func toParamRequest(p domain.StarterFormulaParam) paramRequest {
	param := paramRequest{
		Name: p.Name, Type: p.Type, Source: p.Source, Optional: p.Optional, Hint: p.Hint, Multiline: p.Multiline,
		Inline: p.Inline, Multiple: p.Multiple, Default: p.Default, Defaults: p.Defaults,
	}
	for _, o := range p.Options {
		param.Options = append(param.Options, optionRequest(o))
	}
	if p.Lookup != nil {
		param.Lookup = &lookupRequest{
			Table:            p.Lookup.Table,
			LabelColumn:      p.Lookup.LabelColumn,
			ValueColumn:      p.Lookup.ValueColumn,
			ExcludeSelection: p.Lookup.ExcludeSelection,
			AllowNew:         p.Lookup.AllowNew,
			Scope:            p.Lookup.Scope,
			Fill:             p.Lookup.Fill,
		}
	}
	if p.When != nil {
		param.When = &whenRequest{Param: p.When.Param, Op: p.When.Op, Value: p.When.Value}
	}
	for _, field := range p.Fields {
		param.Fields = append(param.Fields, toParamRequest(field))
	}
	return param
}
