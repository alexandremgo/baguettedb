package mcp

import (
	"fmt"
	"slices"
	"time"

	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	"github.com/alexandremgo/baguettedb/pkg/databases/appconv"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// queryResult represents a query result in MCP tool responses.
type queryResult struct {
	Columns               []column      `json:"columns"`
	Rows                  [][]value     `json:"rows"`
	AffectedRowCount      int64         `json:"affected_row_count"`
	LastInsertRowID       *int64        `json:"last_insert_row_id,omitempty"`
	BatchID               string        `json:"batch_id,omitempty"`
	AffectedRows          []affectedRow `json:"affected_rows,omitempty"`
	AffectedRowsTruncated bool          `json:"affected_rows_truncated,omitempty"`
}

type column struct {
	Name         string `json:"name"`
	DeclaredType string `json:"decl_type"`
}

type value struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}

type affectedRow struct {
	TableName string         `json:"table_name"`
	RowID     string         `json:"row_id"`
	Operation string         `json:"operation"`
	OldValues map[string]any `json:"old_values,omitempty"`
	NewValues map[string]any `json:"new_values,omitempty"`
}

type tableDescription struct {
	// Agents should treat a "virtual" / "fts5" table as an FTS5 index — query with MATCH, never write to it
	// directly — and never touch "shadow" tables.
	apptypes.TableDescription
	// Skills targeting this table, with full bodies inlined. Empty when no
	// skill targets the table. Bdb-level skills (no target tables) are
	// surfaced via describe_bdb, not here.
	Skills []skillDetail `json:"skills"`
}

// skillDetail is the full skill payload (with body) returned in describe_table
// for skills targeting that table, and in get_skill for any skill.
type skillDetail struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	Body         string    `json:"body"`
	TargetTables []string  `json:"target_tables"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type changeLogEntry struct {
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

type changeBatch struct {
	BatchID   string           `json:"batch_id"`
	Kind      string           `json:"kind"`
	Origin    string           `json:"origin"`
	ChangedBy string           `json:"changed_by"`
	ChangedAt time.Time        `json:"changed_at"`
	Entries   []changeLogEntry `json:"entries"`
}

// changeConflictResult is the body of a refused undo / redo / revert: why the reversal was refused, the entries that
// moved the state since (the blockers), and what to do about it.
type changeConflictResult struct {
	Error           string                  `json:"error"`
	ChangeLogID     int64                   `json:"change_log_id"`
	Kind            string                  `json:"kind"`
	Subtype         string                  `json:"subtype,omitempty"`
	Reason          string                  `json:"reason"`
	BlockedBy       []changeConflictBlocker `json:"blocked_by,omitempty"`
	ResolutionHints []string                `json:"resolution_hints,omitempty"`
}

// changeConflictBlocker is one _change_log entry that prevents a reversal.
type changeConflictBlocker struct {
	ID         int64     `json:"id"`
	BatchID    string    `json:"batch_id"`
	Kind       string    `json:"kind"`
	Operation  string    `json:"operation"`
	Origin     string    `json:"origin"`
	ChangedBy  string    `json:"changed_by"`
	ChangedAt  time.Time `json:"changed_at"`
	TableName  string    `json:"table_name,omitempty"`
	RowID      string    `json:"row_id,omitempty"`
	ObjectType string    `json:"object_type,omitempty"`
	ObjectName string    `json:"object_name,omitempty"`
	DDLSubtype string    `json:"ddl_subtype,omitempty"`
}

type listChanges struct {
	Batches []changeBatch `json:"batches"`
}

// --- Conversion functions ---

func toChangeConflictResult(c *domain.ChangeConflict) changeConflictResult {
	if c == nil {
		return changeConflictResult{Error: domain.ErrChangeConflict.Error()}
	}
	blockers := make([]changeConflictBlocker, len(c.BlockedBy))
	for i, b := range c.BlockedBy {
		blockers[i] = changeConflictBlocker{
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
	return changeConflictResult{
		Error:       domain.ErrChangeConflict.Error(),
		ChangeLogID: c.ChangeLogID,
		Kind:        string(c.Kind),
		Subtype:     c.Subtype,
		Reason:      c.Reason,
		BlockedBy:   blockers,
		ResolutionHints: withBlockersHint(c.ResolutionHints, c.BlockersHint(func(id string) string {
			return fmt.Sprintf("`%s(change_log_id=%q)`", mcputil.ToolRevertChange, id)
		})),
	}
}

// withBlockersHint returns hints followed by hint, unless hint is empty.
func withBlockersHint(hints []string, hint string) []string {
	if hint == "" {
		return hints
	}
	return append(slices.Clone(hints), hint)
}

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

func toQueryResult(qr *domain.QueryResult) queryResult {
	columns := make([]column, len(qr.Columns))
	for i, c := range qr.Columns {
		columns[i] = column{Name: c.Name, DeclaredType: c.DeclaredType}
	}

	rows := make([][]value, len(qr.Rows))
	for i, row := range qr.Rows {
		vals := make([]value, len(row))
		for j, v := range row {
			vals[j] = value{Type: sqlTypeToString(v.Type), Value: v.Value}
		}
		rows[i] = vals
	}

	var affected []affectedRow
	for _, r := range qr.AffectedRows {
		affected = append(affected, affectedRow{
			TableName: r.TableName,
			RowID:     r.RowID,
			Operation: r.Operation,
			OldValues: r.OldValues,
			NewValues: r.NewValues,
		})
	}

	return queryResult{
		Columns:               columns,
		Rows:                  rows,
		AffectedRowCount:      qr.AffectedRowCount,
		LastInsertRowID:       qr.LastInsertRowID,
		BatchID:               qr.BatchID,
		AffectedRows:          affected,
		AffectedRowsTruncated: qr.AffectedRowsTruncated,
	}
}

func toTableDescription(td *domain.TableDescription, skills []*domain.Skill) tableDescription {
	skillDetails := make([]skillDetail, 0, len(skills))
	for _, s := range skills {
		skillDetails = append(skillDetails, toSkillDetail(s))
	}
	return tableDescription{TableDescription: appconv.TableDescription(td), Skills: skillDetails}
}

// toSkillDetail converts a domain skill to the full MCP skill DTO (body + targets).
func toSkillDetail(s *domain.Skill) skillDetail {
	targets := s.TargetTables
	if targets == nil {
		targets = []string{}
	}
	return skillDetail{
		ID:           s.ID.String(),
		Name:         s.Name,
		Description:  s.Description,
		Body:         s.Body,
		TargetTables: targets,
		CreatedAt:    s.CreatedAt,
		UpdatedAt:    s.UpdatedAt,
	}
}

// formulaParam is one parameter entry in a formula MCP payload.
type formulaParam struct {
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
	Defaults []any             `json:"defaults,omitempty"`
	When     *formulaParamWhen `json:"when,omitempty"`
	Fields   []formulaParam    `json:"fields,omitempty"`
}

// formulaParamOption is one closed-set entry: `value` is what a run binds (the param type's string
// form), `label` what the app shows in its place. Payloads always carry both.
type formulaParamOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// formulaParamWhen is a parameter's visibility condition: the app's forms render the field only
// while the named sibling parameter's current value compares true against the given value, and a
// run requires the parameter only then.
type formulaParamWhen struct {
	Param string `json:"param"`
	Op    string `json:"op,omitempty"`
	Value string `json:"value"`
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

// toFormulaParam converts one domain parameter — group fields included — into its MCP DTO.
func toFormulaParam(p domain.FormulaParam) formulaParam {
	var lookup *formulaLookupConfig
	if p.Lookup != nil {
		lookup = &formulaLookupConfig{
			Table:            p.Lookup.Table,
			LabelColumn:      p.Lookup.LabelColumn,
			ValueColumn:      p.Lookup.ValueColumn,
			ExcludeSelection: p.Lookup.ExcludeSelection,
			AllowNew:         p.Lookup.AllowNew,
			Scope:            p.Lookup.Scope,
			Fill:             p.Lookup.Fill,
		}
	}
	var when *formulaParamWhen
	if p.When != nil {
		when = &formulaParamWhen{Param: p.When.Param, Op: p.When.Op, Value: p.When.Value}
	}
	var options []formulaParamOption
	for _, o := range p.Options {
		options = append(options, formulaParamOption{Value: o.Value, Label: o.Label})
	}
	out := formulaParam{
		Name: p.Name, Type: p.Type, Source: string(p.Source), Optional: p.Optional, Lookup: lookup, Hint: p.Hint,
		Multiline: p.Multiline,
		Inline:    p.Inline,
		Multiple:  p.Multiple,
		Options:   options,
		Default:   p.Default,
		Defaults:  domain.DefaultsResponse(p),
		When:      when,
	}
	for _, field := range p.Fields {
		out.Fields = append(out.Fields, toFormulaParam(field))
	}
	return out
}

// formulaDetail is the full formula payload returned by the formula MCP tools
// (create / update / get / list). Kind is derived server-side ("view" for a
// SELECT, "action" for a DML statement), as is StatementVerb — the statement's
// head verb (select / insert / update / delete).
type formulaDetail struct {
	ID                  string                `json:"id"`
	Name                string                `json:"name"`
	Label               string                `json:"label,omitempty"`
	Description         string                `json:"description"`
	SQL                 string                `json:"sql"`
	Kind                string                `json:"kind"`
	StatementVerb       string                `json:"statement_verb"`
	Params              []formulaParam        `json:"params"`
	Display             *formulaDisplayDetail `json:"display,omitempty"`
	Hidden              bool                  `json:"hidden,omitempty"`
	AttachedToFormulaID string                `json:"attached_to_formula_id,omitempty"`
	DisplayOrder        int                   `json:"display_order"`
	CreatedAt           time.Time             `json:"created_at"`
	UpdatedAt           time.Time             `json:"updated_at"`
}

// formulaDisplayDetail is a view's display config in MCP formula payloads; absent = the plain
// table rendering with no declared slots.
type formulaDisplayDetail struct {
	Mode    string                       `json:"mode"`
	Columns []formulaDisplayColumnDetail `json:"columns,omitempty"`
	Slots   *formulaDisplaySlotsDetail   `json:"slots,omitempty"`
	Window  *formulaDisplayWindowDetail  `json:"window,omitempty"`
}

// formulaDisplayWindowDetail is a calendar's window: weekday names for a weekly window, a start day and a
// length in days for a fixed range.
type formulaDisplayWindowDetail struct {
	WeekStart string `json:"week_start,omitempty"`
	WeekEnd   string `json:"week_end,omitempty"`
	Start     string `json:"start,omitempty"`
	Days      int    `json:"days,omitempty"`
}

// formulaDisplayColumnDetail assigns one result column a rendering role and/or a value format, or
// marks it hidden (dropped from the grid, kept in the row data).
type formulaDisplayColumnDetail struct {
	Column string `json:"column"`
	Role   string `json:"role,omitempty"`
	Format string `json:"format,omitempty"`
	Hidden bool   `json:"hidden,omitempty"`
}

// formulaDisplaySlotsDetail maps a display's interaction slots to attached-formula UUIDs. Bulk
// and RowActions are ordered lists — array order is the render order.
type formulaDisplaySlotsDetail struct {
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

// toFormulaDisplayDetail converts the domain display config to its MCP DTO; nil stays nil.
func toFormulaDisplayDetail(d *domain.FormulaDisplay) *formulaDisplayDetail {
	if d == nil {
		return nil
	}
	out := &formulaDisplayDetail{Mode: string(d.Mode)}
	for _, c := range d.Columns {
		out.Columns = append(out.Columns, formulaDisplayColumnDetail{
			Column: c.Column, Role: string(c.Role), Format: string(c.Format), Hidden: c.Hidden,
		})
	}
	if !d.Slots.IsEmpty() {
		out.Slots = &formulaDisplaySlotsDetail{
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
		out.Window = &formulaDisplayWindowDetail{
			WeekStart: string(w.WeekStart), WeekEnd: string(w.WeekEnd), Start: w.Start, Days: w.Days,
		}
	}
	return out
}

// toFormulaDetail converts a domain formula to its MCP DTO.
func toFormulaDetail(f *domain.Formula) formulaDetail {
	params := make([]formulaParam, len(f.Params))
	for i, p := range f.Params {
		params[i] = toFormulaParam(p)
	}
	var attachedTo string
	if f.AttachedToFormulaID != nil {
		attachedTo = f.AttachedToFormulaID.String()
	}
	return formulaDetail{
		ID:                  f.ID.String(),
		Name:                f.Name,
		Label:               f.Label,
		Description:         f.Description,
		SQL:                 f.SQL,
		Kind:                string(f.Kind),
		StatementVerb:       string(f.StatementVerb),
		Params:              params,
		Display:             toFormulaDisplayDetail(f.Display),
		Hidden:              f.Hidden,
		AttachedToFormulaID: attachedTo,
		DisplayOrder:        f.DisplayOrder,
		CreatedAt:           f.CreatedAt,
		UpdatedAt:           f.UpdatedAt,
	}
}

// toFormulaDetails converts a list of domain formulas to their MCP DTOs.
func toFormulaDetails(formulas []*domain.Formula) []formulaDetail {
	out := make([]formulaDetail, len(formulas))
	for i, f := range formulas {
		out[i] = toFormulaDetail(f)
	}
	return out
}

// formulaPagination tells a run_formula caller whether more rows exist beyond the page and the
// cursor to resume from (the :cursor value for the next page). NextCursor is null when the page
// is empty or the view does not select a "rowid" column.
type formulaPagination struct {
	HasMore    bool   `json:"has_more"`
	NextCursor *int64 `json:"next_cursor"`
}

// formulaRunResult is run_formula's response: the query result fields plus, for a paginated view,
// pagination metadata (omitted for non-paginated views and for actions). StatementCount says how
// many statements ran — the affected-row count sums across all of them, so a multi-statement total
// is bookkeeping, not a one-to-one user-visible change count.
type formulaRunResult struct {
	queryResult
	StatementCount int                `json:"statement_count,omitempty"`
	Pagination     *formulaPagination `json:"pagination,omitempty"`
}

// toFormulaRunResult converts a formula run outcome to its MCP DTO.
func toFormulaRunResult(rr *service.FormulaRunResult) formulaRunResult {
	out := formulaRunResult{queryResult: toQueryResult(rr.Result), StatementCount: rr.StatementCount}
	if rr.Pagination != nil {
		out.Pagination = &formulaPagination{
			HasMore:    rr.Pagination.HasMore,
			NextCursor: rr.Pagination.NextCursor,
		}
	}
	return out
}

func toListChanges(r *domain.ListChangesResult) listChanges {
	batches := make([]changeBatch, len(r.Batches))
	for i, b := range r.Batches {
		entries := make([]changeLogEntry, len(b.Entries))
		for j, e := range b.Entries {
			entries[j] = changeLogEntry{
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
		batches[i] = changeBatch{
			BatchID:   b.BatchID,
			Kind:      string(b.Kind),
			Origin:    b.Origin,
			ChangedBy: b.ChangedBy,
			ChangedAt: b.ChangedAt,
			Entries:   entries,
		}
	}
	return listChanges{Batches: batches}
}
