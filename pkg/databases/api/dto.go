package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// executeRequest holds the JSON body for query/mutation execution. Args carry positional bound
// values in the same {type, value} wire shape responses use for row cells.
type executeRequest struct {
	SQL  string         `json:"sql"`
	Args []valueRequest `json:"args,omitempty"`
}

// valueRequest is a typed bound value in API requests, mirroring valueResponse.
type valueRequest struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}

// accountStorageResponse represents a user's account-wide storage usage in API responses: the
// tracked total across every bdb they own against their aggregate cap (0 = unlimited).
type accountStorageResponse struct {
	TotalStorageBytesUsed  int64 `json:"total_storage_bytes_used"`
	TotalStorageBytesLimit int64 `json:"total_storage_bytes_limit"`
}

// databaseStatsResponse represents a bdb database's on-disk usage against its role-resolved size cap
// (0 = unlimited) in API responses.
type databaseStatsResponse struct {
	StorageBytesUsed  uint64  `json:"storage_bytes_used"`
	StorageBytesLimit int64   `json:"storage_bytes_limit"`
	StorageUsagePct   float64 `json:"storage_usage_percent"`
}

// toDatabaseStatsResponse builds the stats DTO, deriving usage percent against the limit (0 when the
// limit is unlimited).
func toDatabaseStatsResponse(usedBytes uint64, limitBytes int64) databaseStatsResponse {
	var pct float64
	if limitBytes > 0 {
		pct = float64(usedBytes) / float64(limitBytes) * 100
	}
	return databaseStatsResponse{
		StorageBytesUsed:  usedBytes,
		StorageBytesLimit: limitBytes,
		StorageUsagePct:   pct,
	}
}

// databaseResponse represents a database in API responses.
type databaseResponse struct {
	ID        uuid.UUID `json:"id"`
	BdbID     uuid.UUID `json:"bdb_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// queryResultResponse represents a query result in API responses.
type queryResultResponse struct {
	Columns               []columnResponse      `json:"columns"`
	Rows                  [][]valueResponse     `json:"rows"`
	AffectedRowCount      int64                 `json:"affected_row_count"`
	LastInsertRowID       *int64                `json:"last_insert_row_id,omitempty"`
	BatchID               string                `json:"batch_id,omitempty"`
	AffectedRows          []affectedRowResponse `json:"affected_rows,omitempty"`
	AffectedRowsTruncated bool                  `json:"affected_rows_truncated,omitempty"`
}

// columnResponse represents a column in API responses.
type columnResponse struct {
	Name         string `json:"name"`
	DeclaredType string `json:"decl_type"`
}

// valueResponse represents a typed value in API responses.
type valueResponse struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}

// errorResponse represents an error in API responses.
type errorResponse struct {
	Error string `json:"error"`
}

// affectedRowResponse represents a row affected by a mutation.
type affectedRowResponse struct {
	TableName string         `json:"table_name"`
	RowID     string         `json:"row_id"`
	Operation string         `json:"operation"`
	OldValues map[string]any `json:"old_values,omitempty"`
	NewValues map[string]any `json:"new_values,omitempty"`
}

// changeLogEntryResponse represents a single _change_log entry in API responses. DML and DDL share this shape: DML
// rows populate TableName / RowID / OldValues / NewValues; DDL rows populate ObjectType / ObjectName / DDLSubtype /
// DDLStatement / DDLMetadata. The Kind discriminator tells the client which subset to read.
type changeLogEntryResponse struct {
	ID           int64          `json:"id"`
	BatchID      string         `json:"batch_id"`
	Kind         string         `json:"kind"`
	Operation    string         `json:"operation"`
	Origin       string         `json:"origin"`
	ChangedBy    string         `json:"changed_by"`
	ChangedAt    time.Time      `json:"changed_at"`
	TableName    string         `json:"table_name,omitempty"`
	RowID        string         `json:"row_id,omitempty"`
	OldValues    map[string]any `json:"old_values,omitempty"`
	NewValues    map[string]any `json:"new_values,omitempty"`
	ObjectType   string         `json:"object_type,omitempty"`
	ObjectName   string         `json:"object_name,omitempty"`
	DDLSubtype   string         `json:"ddl_subtype,omitempty"`
	DDLStatement string         `json:"ddl_statement,omitempty"`
	DDLMetadata  any            `json:"ddl_metadata,omitempty"`
}

// changeBatchResponse groups change-log entries that share a batch_id.
type changeBatchResponse struct {
	BatchID   string                   `json:"batch_id"`
	Kind      string                   `json:"kind"`
	Origin    string                   `json:"origin"`
	ChangedBy string                   `json:"changed_by"`
	ChangedAt time.Time                `json:"changed_at"`
	Entries   []changeLogEntryResponse `json:"entries"`
}

// listChangesResponse is the body returned by GET .../database/changes.
type listChangesResponse struct {
	Batches []changeBatchResponse `json:"batches"`
}

// --- Conversion functions ---

// toDatabaseResponse converts a domain Database to an API response.
func toDatabaseResponse(db *domain.Database) databaseResponse {
	return databaseResponse{
		ID:        db.ID,
		BdbID:     db.BdbID,
		CreatedAt: db.CreatedAt,
		UpdatedAt: db.UpdatedAt,
	}
}

// toTypedValues converts request-side bound values into domain typed values, coercing JSON's
// float64 numbers into the integer storage class where declared. Blob args have no JSON encoding
// and are rejected.
func toTypedValues(args []valueRequest) ([]domain.TypedValue, error) {
	if len(args) == 0 {
		return nil, nil
	}
	out := make([]domain.TypedValue, len(args))
	for i, a := range args {
		switch a.Type {
		case "null":
			out[i] = domain.NullValue()
		case "integer":
			n, ok := a.Value.(float64)
			if !ok || n != float64(int64(n)) {
				return nil, fmt.Errorf("arg %d: %v is not an integer", i, a.Value)
			}
			out[i] = domain.IntegerValue(int64(n))
		case "float":
			n, ok := a.Value.(float64)
			if !ok {
				return nil, fmt.Errorf("arg %d: %v is not a number", i, a.Value)
			}
			out[i] = domain.RealValue(n)
		case "text":
			s, ok := a.Value.(string)
			if !ok {
				return nil, fmt.Errorf("arg %d: %v is not a string", i, a.Value)
			}
			out[i] = domain.TextValue(s)
		default:
			return nil, fmt.Errorf("arg %d: unsupported type %q", i, a.Type)
		}
	}
	return out, nil
}

// sqlTypeToString maps a domain SQLType to its API wire string.
func sqlTypeToString(t domain.SQLType) string {
	switch t {
	case domain.SQLNull:
		return "null"
	case domain.SQLInteger:
		return "integer"
	case domain.SQLReal:
		return "float"
	case domain.SQLText:
		return "text"
	case domain.SQLBlob:
		return "blob"
	default:
		return "text"
	}
}

// toQueryResultResponse converts a domain QueryResult to an API response.
func toQueryResultResponse(qr *domain.QueryResult) queryResultResponse {
	columns := make([]columnResponse, len(qr.Columns))
	for i, c := range qr.Columns {
		columns[i] = columnResponse{Name: c.Name, DeclaredType: c.DeclaredType}
	}

	rows := make([][]valueResponse, len(qr.Rows))
	for i, row := range qr.Rows {
		vals := make([]valueResponse, len(row))
		for j, v := range row {
			vals[j] = valueResponse{Type: sqlTypeToString(v.Type), Value: v.Value}
		}
		rows[i] = vals
	}

	return queryResultResponse{
		Columns:               columns,
		Rows:                  rows,
		AffectedRowCount:      qr.AffectedRowCount,
		LastInsertRowID:       qr.LastInsertRowID,
		BatchID:               qr.BatchID,
		AffectedRows:          toAffectedRowResponses(qr.AffectedRows),
		AffectedRowsTruncated: qr.AffectedRowsTruncated,
	}
}

func toAffectedRowResponses(rows []domain.AffectedRow) []affectedRowResponse {
	if len(rows) == 0 {
		return nil
	}
	result := make([]affectedRowResponse, len(rows))
	for i, r := range rows {
		result[i] = affectedRowResponse{
			TableName: r.TableName,
			RowID:     r.RowID,
			Operation: r.Operation,
			OldValues: r.OldValues,
			NewValues: r.NewValues,
		}
	}
	return result
}

// skillResponse is the user-facing skill payload returned by the public REST API.
type skillResponse struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	Body         string    `json:"body"`
	TargetTables []string  `json:"target_tables"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// createSkillRequest is the JSON body for POST /skills.
type createSkillRequest struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Body         string   `json:"body"`
	TargetTables []string `json:"target_tables"`
}

// updateSkillRequest is the JSON body for PUT /skills/{ref}. Pointer fields
// distinguish "not set" from "set to empty"; pointer to a slice for target
// tables likewise lets clients explicitly clear the list.
type updateSkillRequest struct {
	Name         *string   `json:"name,omitempty"`
	Description  *string   `json:"description,omitempty"`
	Body         *string   `json:"body,omitempty"`
	TargetTables *[]string `json:"target_tables,omitempty"`
}

func toSkillResponse(s *domain.Skill) skillResponse {
	targets := s.TargetTables
	if targets == nil {
		targets = []string{}
	}
	return skillResponse{
		ID:           s.ID,
		Name:         s.Name,
		Description:  s.Description,
		Body:         s.Body,
		TargetTables: targets,
		CreatedAt:    s.CreatedAt,
		UpdatedAt:    s.UpdatedAt,
	}
}

func toSkillResponses(skills []*domain.Skill) []skillResponse {
	result := make([]skillResponse, len(skills))
	for i, s := range skills {
		result[i] = toSkillResponse(s)
	}
	return result
}

// changeConflictResponse is the structured 409 body returned when an
// undo / redo / revert is refused because state has moved.
type changeConflictResponse struct {
	Error           string                       `json:"error"`
	ChangeLogID     int64                        `json:"change_log_id"`
	Kind            string                       `json:"kind"`
	Subtype         string                       `json:"subtype,omitempty"`
	Reason          string                       `json:"reason"`
	BlockedBy       []changeConflictBlockerEntry `json:"blocked_by,omitempty"`
	ResolutionHints []string                     `json:"resolution_hints,omitempty"`
}

// changeConflictBlockerEntry is one entry in the blocker list — the
// _change_log row that prevents reversal.
type changeConflictBlockerEntry struct {
	ID         int64     `json:"id"`
	BatchID    string    `json:"batch_id"`
	Kind       string    `json:"kind"`
	Operation  string    `json:"operation"`
	Origin     string    `json:"origin"`
	ChangedBy  string    `json:"changed_by"`
	ChangedAt  time.Time `json:"changed_at,omitempty"`
	TableName  string    `json:"table_name,omitempty"`
	RowID      string    `json:"row_id,omitempty"`
	ObjectType string    `json:"object_type,omitempty"`
	ObjectName string    `json:"object_name,omitempty"`
	DDLSubtype string    `json:"ddl_subtype,omitempty"`
}

func toChangeConflictResponse(c *domain.ChangeConflict) changeConflictResponse {
	if c == nil {
		return changeConflictResponse{Error: domain.ErrChangeConflict.Error()}
	}
	blockers := make([]changeConflictBlockerEntry, len(c.BlockedBy))
	for i, b := range c.BlockedBy {
		blockers[i] = changeConflictBlockerEntry{
			ID:         b.ID,
			BatchID:    b.BatchID,
			Kind:       string(b.Kind),
			Operation:  b.Operation,
			Origin:     b.Origin,
			ChangedBy:  b.ChangedBy,
			ChangedAt:  b.ChangedAt,
			TableName:  b.TableName,
			RowID:      b.RowID,
			ObjectType: b.ObjectType,
			ObjectName: b.ObjectName,
			DDLSubtype: b.DDLSubtype,
		}
	}
	return changeConflictResponse{
		Error:           domain.ErrChangeConflict.Error(),
		ChangeLogID:     c.ChangeLogID,
		Kind:            string(c.Kind),
		Subtype:         c.Subtype,
		Reason:          c.Reason,
		BlockedBy:       blockers,
		ResolutionHints: withBlockersHint(c.ResolutionHints, c.BlockersHint(nil)),
	}
}

// withBlockersHint returns hints followed by hint, unless hint is empty.
func withBlockersHint(hints []string, hint string) []string {
	if hint == "" {
		return hints
	}
	return append(slices.Clone(hints), hint)
}

func toListChangesResponse(r *domain.ListChangesResult) listChangesResponse {
	batches := make([]changeBatchResponse, len(r.Batches))
	for i, b := range r.Batches {
		entries := make([]changeLogEntryResponse, len(b.Entries))
		for j, e := range b.Entries {
			entries[j] = changeLogEntryResponse{
				ID:           e.ID,
				BatchID:      e.BatchID,
				Kind:         string(e.Kind),
				Operation:    e.Operation,
				Origin:       e.Origin,
				ChangedBy:    e.ChangedBy,
				ChangedAt:    e.ChangedAt,
				TableName:    e.TableName,
				RowID:        e.RowID,
				OldValues:    e.OldValues,
				NewValues:    e.NewValues,
				ObjectType:   e.ObjectType,
				ObjectName:   e.ObjectName,
				DDLSubtype:   e.DDLSubtype,
				DDLStatement: e.DDLStatement,
			}
			if e.DDLMetadata != nil {
				entries[j].DDLMetadata = e.DDLMetadata
			}
		}
		batches[i] = changeBatchResponse{
			BatchID:   b.BatchID,
			Kind:      string(b.Kind),
			Origin:    b.Origin,
			ChangedBy: b.ChangedBy,
			ChangedAt: b.ChangedAt,
			Entries:   entries,
		}
	}
	return listChangesResponse{Batches: batches}
}

// formulaLookupConfig names the table and columns a lookup parameter's options come from:
// LabelColumn is shown in the picker, ValueColumn supplies the bound value. ExcludeSelection drops
// the acting row from the picker's options (display-only).
type formulaLookupConfig struct {
	Table            string `json:"table"`
	LabelColumn      string `json:"label_column"`
	ValueColumn      string `json:"value_column"`
	ExcludeSelection bool   `json:"exclude_selection,omitempty"`
	// AllowNew lets the picker also submit text typed past the option list — the SQL should
	// resolve or create it; text params only.
	AllowNew bool `json:"allow_new,omitempty"`
	// Scope narrows the picker's options by sibling parameters' current values: sibling param name →
	// column of the lookup table compared against it. Display-only.
	Scope map[string]string `json:"scope,omitempty"`
	// Fill copies columns of the picked row into sibling parameters: sibling param name → column of
	// the lookup table whose value the pick writes there. Display-only.
	Fill map[string]string `json:"fill,omitempty"`
}

// formulaParamWhen is a parameter's visibility condition: the form renders the field only while
// the named sibling parameter's current value compares true against the given value, and a run
// requires the parameter only then.
type formulaParamWhen struct {
	Param string `json:"param"`
	Op    string `json:"op,omitempty"`
	Value string `json:"value"`
}

// formulaParamRequest is one parameter entry in a create/update formula request body.
// Lookup is set only for source "lookup" parameters; an optional parameter may be omitted
// at run time (it binds SQL NULL).
type formulaParamRequest struct {
	Name      string               `json:"name"`
	Type      string               `json:"type"`
	Source    string               `json:"source"`
	Optional  bool                 `json:"optional,omitempty"`
	Lookup    *formulaLookupConfig `json:"lookup,omitempty"`
	Hint      string               `json:"hint,omitempty"`
	Multiline bool                 `json:"multiline,omitempty"`
	Inline    bool                 `json:"inline,omitempty"`
	Multiple  bool                 `json:"multiple,omitempty"`
	Options   []formulaParamOption `json:"options,omitempty"`
	Default   string               `json:"default,omitempty"`
	// Defaults is a multi-select's declared subset: strings and numbers, each read as its string form.
	Defaults []formulaParamScalar  `json:"defaults,omitempty"`
	When     *formulaParamWhen     `json:"when,omitempty"`
	Fields   []formulaParamRequest `json:"fields,omitempty"`
}

// formulaParamScalar is one authored value that may arrive as a JSON string or number, kept as its
// string form the way option values are.
type formulaParamScalar string

// UnmarshalJSON accepts a string or a number; a number reads as its literal string form.
func (v *formulaParamScalar) UnmarshalJSON(b []byte) error {
	s, err := optionScalar(b)
	if err != nil {
		return errors.New("a defaults entry must be a string or a number")
	}
	*v = formulaParamScalar(s)
	return nil
}

// formulaParamOption is one closed-set entry: `value` is what a run binds (the param type's string
// form), `label` what the app shows in its place. Responses always carry both.
type formulaParamOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// UnmarshalJSON accepts the {value, label} object form — label optional, folding to the value — or a
// bare scalar meaning value = label, so `["film", "tv"]` and mixed lists read. A scalar (and an
// object's value) may be a string or a number; a number reads as its literal string form.
func (o *formulaParamOption) UnmarshalJSON(b []byte) error {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var obj struct {
			// Value stays undecoded so optionScalar can accept a string or a number below.
			Value json.RawMessage `json:"value"`
			Label string          `json:"label"`
		}
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return err
		}
		value, err := optionScalar(obj.Value)
		if err != nil {
			return err
		}
		if obj.Label == "" {
			obj.Label = value
		}
		*o = formulaParamOption{Value: value, Label: obj.Label}
		return nil
	}
	value, err := optionScalar(trimmed)
	if err != nil {
		return err
	}
	*o = formulaParamOption{Value: value, Label: value}
	return nil
}

// optionScalar reads a JSON string or number token as its literal string form.
func optionScalar(b []byte) (string, error) {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		return s, nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		return n.String(), nil
	}
	return "", errors.New("an option must be a string, a number, or a {value, label} object")
}

// formulaDisplaySlotsDTO maps a display's interaction slots to attached-formula refs. Requests
// may name a formula by name or UUID; responses always carry the stored UUID. Bulk and RowActions
// are ordered lists — array order is the render order.
type formulaDisplaySlotsDTO struct {
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

// formulaDisplayDTO is the wire shape of a view's display config; a null/absent display means the
// default table rendering.
type formulaDisplayDTO struct {
	Mode    string                    `json:"mode"`
	Columns []formulaDisplayColumnDTO `json:"columns,omitempty"`
	Slots   *formulaDisplaySlotsDTO   `json:"slots,omitempty"`
	Window  *formulaDisplayWindowDTO  `json:"window,omitempty"`
}

// formulaDisplayWindowDTO is a calendar's window: either weekday names (a weekly window, repeating every
// 7 days) or a start day and a length in days (a fixed range, stepping by its length). Calendar mode only.
type formulaDisplayWindowDTO struct {
	WeekStart string `json:"week_start,omitempty"`
	WeekEnd   string `json:"week_end,omitempty"`
	Start     string `json:"start,omitempty"`
	Days      int    `json:"days,omitempty"`
}

// formulaDisplayColumnDTO assigns one result column a rendering role (title / body / check) and/or
// a value format (markdown), or marks it hidden (dropped from the grid, kept in the row data).
type formulaDisplayColumnDTO struct {
	Column string `json:"column"`
	Role   string `json:"role,omitempty"`
	Format string `json:"format,omitempty"`
	Hidden bool   `json:"hidden,omitempty"`
}

// createFormulaRequest is the JSON body for POST /formulas.
type createFormulaRequest struct {
	Name                string                `json:"name"`
	Label               string                `json:"label,omitempty"`
	Description         string                `json:"description"`
	SQL                 string                `json:"sql"`
	Params              []formulaParamRequest `json:"params"`
	AttachedToFormulaID *string               `json:"attached_to_formula_id,omitempty"`
	DisplayOrder        *int                  `json:"display_order,omitempty"`
	Display             *formulaDisplayDTO    `json:"display,omitempty"`
	Hidden              bool                  `json:"hidden,omitempty"`
}

// updateFormulaRequest is the JSON body for PUT /formulas/{ref}. Pointer fields
// distinguish "not set" from "set"; a non-nil params pointer replaces the schema.
// Display is raw to distinguish three states: absent = unchanged, JSON null = clear
// (back to table rendering), object = replace.
type updateFormulaRequest struct {
	Name         *string                `json:"name,omitempty"`
	Label        *string                `json:"label,omitempty"`
	Description  *string                `json:"description,omitempty"`
	SQL          *string                `json:"sql,omitempty"`
	Params       *[]formulaParamRequest `json:"params,omitempty"`
	DisplayOrder *int                   `json:"display_order,omitempty"`
	Display      json.RawMessage        `json:"display,omitempty"`
	Hidden       *bool                  `json:"hidden,omitempty"`
}

// runFormulaRequest is the JSON body for POST /formulas/{ref}/run. Params carries every declared
// parameter value keyed by name: scalars for prompt / selection-single / pagination parameters, and
// an array of row IDs for the bulk (:rowids) selection.
type runFormulaRequest struct {
	Params map[string]any `json:"params,omitempty"`
}

// formulaRunResponse is the run result: the query result fields plus, for a paginated view,
// pagination metadata (omitted otherwise).
// StatementCount says how many statements ran — the affected-row count sums across all of them, so a multi-statement
// total is bookkeeping, not a one-to-one user-visible change count.
type formulaRunResponse struct {
	queryResultResponse
	StatementCount int                        `json:"statement_count,omitempty"`
	Pagination     *formulaPaginationResponse `json:"pagination,omitempty"`
}

// formulaPaginationResponse tells the caller whether more rows exist and the cursor to resume from.
type formulaPaginationResponse struct {
	HasMore    bool   `json:"has_more"`
	NextCursor *int64 `json:"next_cursor"`
}

func toFormulaRunResponse(rr *service.FormulaRunResult) formulaRunResponse {
	resp := formulaRunResponse{
		queryResultResponse: toQueryResultResponse(rr.Result),
		StatementCount:      rr.StatementCount,
	}
	if rr.Pagination != nil {
		resp.Pagination = &formulaPaginationResponse{
			HasMore:    rr.Pagination.HasMore,
			NextCursor: rr.Pagination.NextCursor,
		}
	}
	return resp
}

// formulaParamResponse is one parameter entry in a formula response.
type formulaParamResponse struct {
	Name      string               `json:"name"`
	Type      string               `json:"type"`
	Source    string               `json:"source"`
	Optional  bool                 `json:"optional,omitempty"`
	Lookup    *formulaLookupConfig `json:"lookup,omitempty"`
	Hint      string               `json:"hint,omitempty"`
	Multiline bool                 `json:"multiline,omitempty"`
	Inline    bool                 `json:"inline,omitempty"`
	Multiple  bool                 `json:"multiple,omitempty"`
	Options   []formulaParamOption `json:"options,omitempty"`
	Default   string               `json:"default,omitempty"`
	// Defaults reads a multi-select's declared subset back the way it was authored: integer and
	// float values as numbers, everything else as text.
	Defaults []any                  `json:"defaults,omitempty"`
	When     *formulaParamWhen      `json:"when,omitempty"`
	Fields   []formulaParamResponse `json:"fields,omitempty"`
}

// formulaResponse is the user-facing formula payload returned by the REST API.
// StatementVerb is the statement's head verb (select / insert / update / delete), derived server-side.
type formulaResponse struct {
	ID                  uuid.UUID              `json:"id"`
	Name                string                 `json:"name"`
	Label               string                 `json:"label,omitempty"`
	Description         string                 `json:"description"`
	SQL                 string                 `json:"sql"`
	Kind                string                 `json:"kind"`
	StatementVerb       string                 `json:"statement_verb"`
	Params              []formulaParamResponse `json:"params"`
	Display             *formulaDisplayDTO     `json:"display"`
	Hidden              bool                   `json:"hidden"`
	AttachedToFormulaID *string                `json:"attached_to_formula_id"`
	DisplayOrder        int                    `json:"display_order"`
	CreatedAt           time.Time              `json:"created_at"`
	UpdatedAt           time.Time              `json:"updated_at"`
}

// toParamSchema converts request params into the domain parameter schema.
func toParamSchema(in []formulaParamRequest) domain.ParamSchema {
	out := make(domain.ParamSchema, len(in))
	for i, p := range in {
		out[i] = toDomainParam(p)
	}
	return out
}

// toDomainParam converts one request parameter — group fields included — into its domain shape.
func toDomainParam(p formulaParamRequest) domain.FormulaParam {
	var lookup *domain.LookupConfig
	if p.Lookup != nil {
		lookup = &domain.LookupConfig{
			Table:            p.Lookup.Table,
			LabelColumn:      p.Lookup.LabelColumn,
			ValueColumn:      p.Lookup.ValueColumn,
			ExcludeSelection: p.Lookup.ExcludeSelection,
			AllowNew:         p.Lookup.AllowNew,
			Scope:            p.Lookup.Scope,
			Fill:             p.Lookup.Fill,
		}
	}
	var when *domain.ParamWhen
	if p.When != nil {
		when = &domain.ParamWhen{Param: p.When.Param, Op: p.When.Op, Value: p.When.Value}
	}
	fp := domain.FormulaParam{
		Name: p.Name, Type: p.Type, Source: domain.ParamSource(p.Source), Optional: p.Optional, Lookup: lookup,
		Hint: p.Hint, Multiline: p.Multiline, Inline: p.Inline, Multiple: p.Multiple,
		Options: toDomainOptions(p.Options), Default: p.Default, Defaults: toDomainDefaults(p.Defaults), When: when,
	}
	for _, field := range p.Fields {
		fp.Fields = append(fp.Fields, toDomainParam(field))
	}
	return fp
}

// toDomainDefaults converts a request's defaults list into the domain's string forms; nil when empty.
func toDomainDefaults(values []formulaParamScalar) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

// toDomainOptions converts normalized request options into their domain entries.
func toDomainOptions(options []formulaParamOption) []domain.FormulaOption {
	if len(options) == 0 {
		return nil
	}
	out := make([]domain.FormulaOption, len(options))
	for i, o := range options {
		out[i] = domain.FormulaOption{Value: o.Value, Label: o.Label}
	}
	return out
}

// toOptionsResponse converts a domain option set into its response entries.
func toOptionsResponse(options []domain.FormulaOption) []formulaParamOption {
	if len(options) == 0 {
		return nil
	}
	out := make([]formulaParamOption, len(options))
	for i, o := range options {
		out[i] = formulaParamOption{Value: o.Value, Label: o.Label}
	}
	return out
}

// toParamResponse converts one domain parameter — group fields included — into its response DTO.
func toParamResponse(p domain.FormulaParam) formulaParamResponse {
	out := formulaParamResponse{
		Name:      p.Name,
		Type:      p.Type,
		Source:    string(p.Source),
		Optional:  p.Optional,
		Lookup:    toLookupConfigResponse(p.Lookup),
		Hint:      p.Hint,
		Multiline: p.Multiline,
		Inline:    p.Inline,
		Multiple:  p.Multiple,
		Options:   toOptionsResponse(p.Options),
		Default:   p.Default,
		Defaults:  domain.DefaultsResponse(p),
		When:      toWhenResponse(p.When),
	}
	for _, field := range p.Fields {
		out.Fields = append(out.Fields, toParamResponse(field))
	}
	return out
}

// toWhenResponse converts an optional domain visibility condition into its DTO.
func toWhenResponse(w *domain.ParamWhen) *formulaParamWhen {
	if w == nil {
		return nil
	}
	return &formulaParamWhen{Param: w.Param, Op: w.Op, Value: w.Value}
}

// toLookupConfigResponse converts an optional domain lookup config into its DTO.
func toLookupConfigResponse(l *domain.LookupConfig) *formulaLookupConfig {
	if l == nil {
		return nil
	}
	return &formulaLookupConfig{
		Table: l.Table, LabelColumn: l.LabelColumn, ValueColumn: l.ValueColumn,
		ExcludeSelection: l.ExcludeSelection, AllowNew: l.AllowNew, Scope: l.Scope, Fill: l.Fill,
	}
}

// toDomainDisplay converts a display DTO into the domain config; nil stays nil (table mode).
func toDomainDisplay(d *formulaDisplayDTO) *domain.FormulaDisplay {
	if d == nil {
		return nil
	}
	out := &domain.FormulaDisplay{Mode: domain.DisplayMode(d.Mode)}
	for _, c := range d.Columns {
		out.Columns = append(out.Columns, domain.FormulaDisplayColumn{
			Column: c.Column, Role: domain.DisplayColumnRole(c.Role),
			Format: domain.DisplayColumnFormat(c.Format), Hidden: c.Hidden,
		})
	}
	if d.Slots != nil {
		out.Slots = domain.FormulaDisplaySlots{
			Add:        d.Slots.Add,
			Toggle:     d.Slots.Toggle,
			Edit:       d.Slots.Edit,
			Delete:     d.Slots.Delete,
			Detail:     d.Slots.Detail,
			Reorder:    d.Slots.Reorder,
			Move:       d.Slots.Move,
			Bulk:       d.Slots.Bulk,
			RowActions: d.Slots.RowActions,
		}
	}
	if w := d.Window; w != nil {
		out.Window = &domain.FormulaDisplayWindow{
			WeekStart: domain.DisplayWeekday(w.WeekStart), WeekEnd: domain.DisplayWeekday(w.WeekEnd),
			Start: w.Start, Days: w.Days,
		}
	}
	return out
}

// toDisplayResponse converts the domain display config into its DTO; nil stays nil (table mode).
func toDisplayResponse(d *domain.FormulaDisplay) *formulaDisplayDTO {
	if d == nil {
		return nil
	}
	out := &formulaDisplayDTO{Mode: string(d.Mode)}
	for _, c := range d.Columns {
		out.Columns = append(out.Columns, formulaDisplayColumnDTO{
			Column: c.Column, Role: string(c.Role), Format: string(c.Format), Hidden: c.Hidden,
		})
	}
	if !d.Slots.IsEmpty() {
		out.Slots = &formulaDisplaySlotsDTO{
			Add:        d.Slots.Add,
			Toggle:     d.Slots.Toggle,
			Edit:       d.Slots.Edit,
			Delete:     d.Slots.Delete,
			Detail:     d.Slots.Detail,
			Reorder:    d.Slots.Reorder,
			Move:       d.Slots.Move,
			Bulk:       d.Slots.Bulk,
			RowActions: d.Slots.RowActions,
		}
	}
	if w := d.Window; w != nil {
		out.Window = &formulaDisplayWindowDTO{
			WeekStart: string(w.WeekStart), WeekEnd: string(w.WeekEnd), Start: w.Start, Days: w.Days,
		}
	}
	return out
}

// parseAttachedID parses an optional attach reference; nil/empty yields a nil pointer.
func parseAttachedID(s *string) (*uuid.UUID, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	id, err := uuid.Parse(*s)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func toFormulaResponse(f *domain.Formula) formulaResponse {
	params := make([]formulaParamResponse, len(f.Params))
	for i, p := range f.Params {
		params[i] = toParamResponse(p)
	}
	var attachedTo *string
	if f.AttachedToFormulaID != nil {
		s := f.AttachedToFormulaID.String()
		attachedTo = &s
	}
	return formulaResponse{
		ID:                  f.ID,
		Name:                f.Name,
		Label:               f.Label,
		Description:         f.Description,
		SQL:                 f.SQL,
		Kind:                string(f.Kind),
		StatementVerb:       string(f.StatementVerb),
		Params:              params,
		Display:             toDisplayResponse(f.Display),
		Hidden:              f.Hidden,
		AttachedToFormulaID: attachedTo,
		DisplayOrder:        f.DisplayOrder,
		CreatedAt:           f.CreatedAt,
		UpdatedAt:           f.UpdatedAt,
	}
}

func toFormulaResponses(formulas []*domain.Formula) []formulaResponse {
	result := make([]formulaResponse, len(formulas))
	for i, f := range formulas {
		result[i] = toFormulaResponse(f)
	}
	return result
}

// exportStatusResponse describes the caller's export allowances for a bdb.
type exportStatusResponse struct {
	DatabaseExport databaseExportStatusResponse `json:"database_export"`
	FormulaExport  formulaExportStatusResponse  `json:"formula_export"`
}

// databaseExportStatusResponse is the rolling-window full-database export quota state. MaxPerWindow <= 0
// means unlimited; NextAvailableAt is set only while the window is exhausted.
type databaseExportStatusResponse struct {
	Allowed         bool       `json:"allowed"`
	MaxPerWindow    int        `json:"max_per_window"`
	WindowSeconds   int64      `json:"window_seconds"`
	UsedInWindow    int        `json:"used_in_window"`
	NextAvailableAt *time.Time `json:"next_available_at"`
}

// formulaExportStatusResponse is the formula CSV export allowance. MaxRows <= 0 means uncapped.
type formulaExportStatusResponse struct {
	MaxRows int `json:"max_rows"`
}

func toExportStatusResponse(s *service.ExportStatus) exportStatusResponse {
	return exportStatusResponse{
		DatabaseExport: databaseExportStatusResponse{
			Allowed:         s.DatabaseExportAllowed,
			MaxPerWindow:    s.MaxPerWindow,
			WindowSeconds:   int64(s.Window / time.Second),
			UsedInWindow:    s.UsedInWindow,
			NextAvailableAt: s.NextAvailableAt,
		},
		FormulaExport: formulaExportStatusResponse{MaxRows: s.MaxFormulaRows},
	}
}
