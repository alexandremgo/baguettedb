// Package appinput holds the request DTOs of the databases app's use cases: the JSON objects a REST body, an MCP tool
// call or a command-line file carries, decoded with encoding/json and converted into service inputs. It is the inbound
// counterpart of appconv, shared by every entry point, so an input shape is decoded once.
package appinput

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// CreateFormula is the input that creates a formula. AttachedToFormulaID is nil for a top-level formula, and a nil
// Display means the default table rendering.
type CreateFormula struct {
	Name                string          `json:"name"`
	Label               string          `json:"label,omitempty"`
	Description         string          `json:"description"`
	SQL                 string          `json:"sql"`
	Params              []FormulaParam  `json:"params"`
	AttachedToFormulaID *string         `json:"attached_to_formula_id,omitempty"`
	DisplayOrder        *int            `json:"display_order,omitempty"`
	Display             *FormulaDisplay `json:"display,omitempty"`
	Hidden              bool            `json:"hidden,omitempty"`
}

// Input converts the request into the service's creation input. An attachment that is not a UUID is an error.
func (r CreateFormula) Input() (service.FormulaInput, error) {
	attachedID, err := parseAttachedID(r.AttachedToFormulaID)
	if err != nil {
		return service.FormulaInput{}, err
	}
	return service.FormulaInput{
		Name:                r.Name,
		Label:               r.Label,
		Description:         r.Description,
		SQL:                 r.SQL,
		Params:              toParamSchema(r.Params),
		AttachedToFormulaID: attachedID,
		DisplayOrder:        r.DisplayOrder,
		Display:             r.Display.ToDomain(),
		Hidden:              r.Hidden,
	}, nil
}

// UpdateFormula is the input that changes a formula: a field present in the JSON is set, an absent one left as it is,
// and params, when present, replace the whole schema. Display stays raw to tell three states apart: absent leaves the
// display unchanged, null clears it back to table rendering, and an object replaces it.
type UpdateFormula struct {
	Name         *string         `json:"name,omitempty"`
	Label        *string         `json:"label,omitempty"`
	Description  *string         `json:"description,omitempty"`
	SQL          *string         `json:"sql,omitempty"`
	Params       *[]FormulaParam `json:"params,omitempty"`
	DisplayOrder *int            `json:"display_order,omitempty"`
	Display      json.RawMessage `json:"display,omitempty"`
	Hidden       *bool           `json:"hidden,omitempty"`
}

// Patch converts the request into the service's patch. A display that is neither an object nor null is an error.
func (r UpdateFormula) Patch() (service.FormulaPatch, error) {
	patch := service.FormulaPatch{
		Name:         r.Name,
		Label:        r.Label,
		Description:  r.Description,
		SQL:          r.SQL,
		DisplayOrder: r.DisplayOrder,
		Hidden:       r.Hidden,
	}
	if r.Params != nil {
		params := toParamSchema(*r.Params)
		patch.Params = &params
	}
	if len(r.Display) > 0 {
		if string(r.Display) == "null" {
			patch.ClearDisplay = true
		} else {
			var display FormulaDisplay
			if err := json.Unmarshal(r.Display, &display); err != nil {
				return service.FormulaPatch{}, errors.New("display must be an object or null")
			}
			patch.Display = display.ToDomain()
		}
	}
	return patch, nil
}

// FormulaParam is one parameter of a formula. Lookup is set only for a "lookup" source; an optional parameter may be
// omitted at run time (it binds SQL NULL); Fields are a group's sub-parameters.
type FormulaParam struct {
	Name      string          `json:"name"`
	Type      string          `json:"type"`
	Source    string          `json:"source"`
	Optional  bool            `json:"optional,omitempty"`
	Lookup    *FormulaLookup  `json:"lookup,omitempty"`
	Hint      string          `json:"hint,omitempty"`
	Multiline bool            `json:"multiline,omitempty"`
	Inline    bool            `json:"inline,omitempty"`
	Multiple  bool            `json:"multiple,omitempty"`
	Options   []FormulaOption `json:"options,omitempty"`
	Default   FormulaDefault  `json:"default,omitempty"`
	// Defaults is a multi-select's declared subset: strings and numbers, each read as its string form.
	Defaults []FormulaScalar `json:"defaults,omitempty"`
	When     *FormulaWhen    `json:"when,omitempty"`
	Fields   []FormulaParam  `json:"fields,omitempty"`
}

// FormulaDefault is a parameter's default value: a scalar default, or the "all" literal.
type FormulaDefault string

// UnmarshalJSON accepts a string or null.
func (d *FormulaDefault) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*d = FormulaDefault(s)
		return nil
	}
	return errors.New("default must be a string - a multi-select's subset goes in defaults")
}

// FormulaScalar is one authored value that may arrive as a JSON string or number, kept as its string form the way
// option values are.
type FormulaScalar string

// UnmarshalJSON accepts a string or a number; a number reads as its literal string form.
func (v *FormulaScalar) UnmarshalJSON(b []byte) error {
	s, err := optionScalar(b)
	if err != nil {
		return errors.New("a defaults entry must be a string or a number")
	}
	*v = FormulaScalar(s)
	return nil
}

// FormulaLookup names the table and columns a lookup parameter's options come from: LabelColumn is shown in the
// picker, ValueColumn supplies the bound value. ExcludeSelection drops the acting row from the picker's options
// (display-only).
type FormulaLookup struct {
	Table            string `json:"table"`
	LabelColumn      string `json:"label_column"`
	ValueColumn      string `json:"value_column"`
	ExcludeSelection bool   `json:"exclude_selection,omitempty"`
	// AllowNew lets the picker also submit text typed past the option list — the SQL should resolve or create it;
	// text params only.
	AllowNew bool `json:"allow_new,omitempty"`
	// Scope narrows the picker's options by sibling parameters' current values: sibling param name → column of the
	// lookup table compared against it. Display-only.
	Scope map[string]string `json:"scope,omitempty"`
	// Fill copies columns of the picked row into sibling parameters: sibling param name → column of the lookup table
	// whose value the pick writes there. Display-only.
	Fill map[string]string `json:"fill,omitempty"`
}

// FormulaWhen is a parameter's visibility condition: the form renders the field only while the named sibling
// parameter's current value compares true against the given value, and a run requires the parameter only then.
type FormulaWhen struct {
	Param string `json:"param"`
	Op    string `json:"op,omitempty"`
	Value string `json:"value"`
}

// FormulaOption is one closed-set entry: Value is what a run binds (the param type's string form), Label what the app
// shows in its place.
type FormulaOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// UnmarshalJSON accepts the {value, label} object form — label optional, folding to the value — or a bare scalar
// meaning value = label, so `["film", "tv"]` and mixed lists read. A scalar (and an object's value) may be a string or
// a number; a number reads as its literal string form.
func (o *FormulaOption) UnmarshalJSON(b []byte) error {
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
		*o = FormulaOption{Value: value, Label: obj.Label}
		return nil
	}
	value, err := optionScalar(trimmed)
	if err != nil {
		return err
	}
	*o = FormulaOption{Value: value, Label: value}
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

// FormulaDisplay is a view's display config; a nil display means the default table rendering.
type FormulaDisplay struct {
	Mode    string                 `json:"mode"`
	Columns []FormulaDisplayColumn `json:"columns,omitempty"`
	Slots   *FormulaDisplaySlots   `json:"slots,omitempty"`
	Window  *FormulaDisplayWindow  `json:"window,omitempty"`
}

// FormulaDisplayColumn assigns one result column a rendering role (title / body / check) and/or a value format
// (markdown), or marks it hidden (dropped from the grid, kept in the row data).
type FormulaDisplayColumn struct {
	Column string `json:"column"`
	Role   string `json:"role,omitempty"`
	Format string `json:"format,omitempty"`
	Hidden bool   `json:"hidden,omitempty"`
}

// FormulaDisplaySlots maps a display's interaction slots to attached formulas, each named by name or UUID. Bulk and
// RowActions are ordered lists: array order is the render order.
type FormulaDisplaySlots struct {
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

// FormulaDisplayWindow is a calendar's window: either weekday names (a weekly window, repeating every 7 days) or a
// start day and a length in days (a fixed range, stepping by its length). Calendar mode only.
type FormulaDisplayWindow struct {
	WeekStart string `json:"week_start,omitempty"`
	WeekEnd   string `json:"week_end,omitempty"`
	Start     string `json:"start,omitempty"`
	Days      int    `json:"days,omitempty"`
}

// ToDomain converts the display config into its domain shape; a nil display stays nil (table rendering).
func (d *FormulaDisplay) ToDomain() *domain.FormulaDisplay {
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

// toParamSchema converts the request's parameters into the domain parameter schema.
func toParamSchema(in []FormulaParam) domain.ParamSchema {
	out := make(domain.ParamSchema, len(in))
	for i, p := range in {
		out[i] = toDomainParam(p)
	}
	return out
}

// toDomainParam converts one request parameter — group fields included — into its domain shape.
func toDomainParam(p FormulaParam) domain.FormulaParam {
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
		Options: toDomainOptions(p.Options), Default: string(p.Default), Defaults: toDomainDefaults(p.Defaults),
		When: when,
	}
	for _, field := range p.Fields {
		fp.Fields = append(fp.Fields, toDomainParam(field))
	}
	return fp
}

// toDomainOptions converts normalized request options into their domain entries.
func toDomainOptions(options []FormulaOption) []domain.FormulaOption {
	if len(options) == 0 {
		return nil
	}
	out := make([]domain.FormulaOption, len(options))
	for i, o := range options {
		out[i] = domain.FormulaOption{Value: o.Value, Label: o.Label}
	}
	return out
}

// toDomainDefaults converts a request's defaults list into the domain's string forms; nil when empty.
func toDomainDefaults(values []FormulaScalar) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

// parseAttachedID parses an optional attachment reference; nil or empty yields a nil pointer, anything else must be
// a UUID.
func parseAttachedID(s *string) (*uuid.UUID, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	id, err := uuid.Parse(*s)
	if err != nil {
		return nil, errors.New("invalid attached_to_formula_id")
	}
	return &id, nil
}
