package dataapi

import (
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// formulaListResponse holds the formulas a Data API key may run.
type formulaListResponse struct {
	Formulas []formulaResponse `json:"formulas"`
}

// formulaResponse is a formula as the Data API describes it: what an app needs to call it, never its SQL.
type formulaResponse struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Label       string          `json:"label,omitempty"`
	Description string          `json:"description"`
	Kind        string          `json:"kind"`
	Params      []paramResponse `json:"params"`

	// Columns are a view's result columns, the keys of each of its rows; an action has none.
	Columns []string `json:"columns,omitempty"`
}

// paramResponse is a parameter of a formula. Required is false for a parameter the run supplies when omitted, such as
// a pagination control; Fields are the fields of each row of a group parameter.
type paramResponse struct {
	Name     string           `json:"name"`
	Type     string           `json:"type"`
	Source   string           `json:"source"`
	Required bool             `json:"required"`
	Multiple bool             `json:"multiple,omitempty"`
	Default  string           `json:"default,omitempty"`
	Defaults []string         `json:"defaults,omitempty"`
	Options  []optionResponse `json:"options,omitempty"`
	Hint     string           `json:"hint,omitempty"`
	Fields   []paramResponse  `json:"fields,omitempty"`
}

// optionResponse is one of the values a parameter accepts, with the label to show for it.
type optionResponse struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// runRequest holds the parameter values of a run, keyed by parameter name.
type runRequest struct {
	Params map[string]any `json:"params"`
}

// viewResponse is a view's run: its rows, each an object of its column values, and the cursor of the next page for a
// paginated view. A row keeps one value per column name, the last when a name repeats; a blob is a base64 string.
type viewResponse struct {
	Rows       []map[string]any    `json:"rows"`
	Pagination *paginationResponse `json:"pagination,omitempty"`
}

// paginationResponse tells whether more rows exist, and the cursor to pass to fetch them.
type paginationResponse struct {
	HasMore    bool   `json:"has_more"`
	NextCursor *int64 `json:"next_cursor"`
}

// actionResponse is an action's run: how many rows it changed, and the rowid of the last row it inserted, if any.
type actionResponse struct {
	AffectedRowCount int64  `json:"affected_row_count"`
	LastInsertRowID  *int64 `json:"last_insert_row_id,omitempty"`
}

// toFormulaResponse converts a formula, a view with its result columns, to its Data API shape.
func toFormulaResponse(f *domain.Formula, columns []string) formulaResponse {
	return formulaResponse{
		ID:          f.ID.String(),
		Name:        f.Name,
		Label:       f.Label,
		Description: f.Description,
		Kind:        string(f.Kind),
		Params:      toParamResponses(f.Params),
		Columns:     columns,
	}
}

// toParamResponses converts parameters to their Data API shape, none being an empty list.
func toParamResponses(params []domain.FormulaParam) []paramResponse {
	out := make([]paramResponse, len(params))
	for i, p := range params {
		options := make([]optionResponse, len(p.Options))
		for j, o := range p.Options {
			options[j] = optionResponse{Value: o.Value, Label: o.Label}
		}
		var fields []paramResponse
		if len(p.Fields) > 0 {
			fields = toParamResponses(p.Fields)
		}
		out[i] = paramResponse{
			Name:     p.Name,
			Type:     p.Type,
			Source:   string(p.Source),
			Required: !p.Optional && p.Source != domain.ParamSourcePagination,
			Multiple: p.Multiple,
			Default:  p.Default,
			Defaults: p.Defaults,
			Options:  options,
			Hint:     p.Hint,
			Fields:   fields,
		}
	}
	return out
}

// toViewResponse converts a view's run to its Data API shape, no row being an empty list.
func toViewResponse(rr *service.FormulaRunResult) viewResponse {
	resp := viewResponse{Rows: make([]map[string]any, len(rr.Result.Rows))}
	for i, values := range rr.Result.Rows {
		row := make(map[string]any, len(values))
		for j, v := range values {
			row[rr.Result.Columns[j].Name] = v.Value
		}
		resp.Rows[i] = row
	}
	if rr.Pagination != nil {
		resp.Pagination = &paginationResponse{HasMore: rr.Pagination.HasMore, NextCursor: rr.Pagination.NextCursor}
	}
	return resp
}

// toActionResponse converts an action's run to its Data API shape.
func toActionResponse(rr *service.FormulaRunResult) actionResponse {
	return actionResponse{AffectedRowCount: rr.Result.AffectedRowCount, LastInsertRowID: rr.Result.LastInsertRowID}
}
