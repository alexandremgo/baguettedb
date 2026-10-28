package embedded

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
)

// manifestDTO is the decoded form of an authored manifest file. The encoding/json tags define the file format:
// sigs.k8s.io/yaml converts the YAML to JSON before decoding, so tags (not YAML struct tags) are the contract.
type manifestDTO struct {
	Key         string       `json:"key"`
	Version     int          `json:"version"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Icon        string       `json:"icon,omitempty"`
	Tags        []string     `json:"tags,omitempty"`
	Schema      []string     `json:"schema"`
	Formulas    []formulaDTO `json:"formulas,omitempty"`
	Skills      []skillDTO   `json:"skills,omitempty"`
	Seed        []seedDTO    `json:"seed,omitempty"`
}

// seedDTO is one table's reference rows: the columns each row fills, then the rows as YAML sequences of scalars.
type seedDTO struct {
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

type formulaDTO struct {
	Name        string      `json:"name"`
	Label       string      `json:"label,omitempty"`
	Description string      `json:"description"`
	SQL         string      `json:"sql"`
	Params      []paramDTO  `json:"params,omitempty"`
	AttachedTo  string      `json:"attached_to,omitempty"`
	Display     *displayDTO `json:"display,omitempty"`
	Hidden      bool        `json:"hidden,omitempty"`
}

// displayDTO mirrors the public formula display payload; slot refs name sibling formulas of the same manifest.
type displayDTO struct {
	Mode    string             `json:"mode"`
	Columns []displayColumnDTO `json:"columns,omitempty"`
	Slots   *slotsDTO          `json:"slots,omitempty"`
	Window  *windowDTO         `json:"window,omitempty"`
}

// windowDTO mirrors a calendar's window: weekday names for a weekly window, a start day and a length for a
// fixed range.
type windowDTO struct {
	WeekStart string `json:"week_start,omitempty"`
	WeekEnd   string `json:"week_end,omitempty"`
	Start     string `json:"start,omitempty"`
	Days      int    `json:"days,omitempty"`
}

type displayColumnDTO struct {
	Column string `json:"column"`
	Role   string `json:"role,omitempty"`
	Format string `json:"format,omitempty"`
	Hidden bool   `json:"hidden,omitempty"`
}

type slotsDTO struct {
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

type paramDTO struct {
	Name      string      `json:"name"`
	Type      string      `json:"type"`
	Source    string      `json:"source"`
	Optional  bool        `json:"optional,omitempty"`
	Lookup    *lookupDTO  `json:"lookup,omitempty"`
	Hint      string      `json:"hint,omitempty"`
	Multiline bool        `json:"multiline,omitempty"`
	Inline    bool        `json:"inline,omitempty"`
	Multiple  bool        `json:"multiple,omitempty"`
	Options   []optionDTO `json:"options,omitempty"`
	Default   string      `json:"default,omitempty"`
	// Defaults is a multi-select's declared subset (`defaults: [staple]`): strings and numbers, each
	// read as its string form.
	Defaults []scalarDTO `json:"defaults,omitempty"`
	When     *whenDTO    `json:"when,omitempty"`
	Fields   []paramDTO  `json:"fields,omitempty"`
}

// scalarDTO is one authored value that may arrive as a string or a number, kept as its string form
// the way option values are.
type scalarDTO string

// UnmarshalJSON accepts a string or a number; a number reads as its literal string form.
func (v *scalarDTO) UnmarshalJSON(b []byte) error {
	s, err := optionScalar(b)
	if err != nil {
		return errors.New("a defaults entry must be a string or a number")
	}
	*v = scalarDTO(s)
	return nil
}

// optionDTO is one closed-set entry of a param's options list.
type optionDTO struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// UnmarshalJSON accepts the {value, label} object form — label optional, folding to the value — or a
// bare scalar meaning value = label, so `options: [film, tv]` and mixed lists both author. A scalar
// (and an object's value) may be a string or a number; a number reads as its literal string form.
func (o *optionDTO) UnmarshalJSON(b []byte) error {
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
		*o = optionDTO{Value: value, Label: obj.Label}
		return nil
	}
	value, err := optionScalar(trimmed)
	if err != nil {
		return err
	}
	*o = optionDTO{Value: value, Label: value}
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

type lookupDTO struct {
	Table            string            `json:"table"`
	LabelColumn      string            `json:"label_column"`
	ValueColumn      string            `json:"value_column"`
	ExcludeSelection bool              `json:"exclude_selection,omitempty"`
	AllowNew         bool              `json:"allow_new,omitempty"`
	Scope            map[string]string `json:"scope,omitempty"`
	Fill             map[string]string `json:"fill,omitempty"`
}

type whenDTO struct {
	Param string `json:"param"`
	Op    string `json:"op,omitempty"`
	Value string `json:"value"`
}

type skillDTO struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Body        string   `json:"body"`
	Targets     []string `json:"targets,omitempty"`
}

// toDomain converts the decoded file form into the storage-agnostic manifest.
func (d manifestDTO) toDomain() domain.StarterManifest {
	m := domain.StarterManifest{
		Key:         d.Key,
		Version:     d.Version,
		Name:        d.Name,
		Description: d.Description,
		Icon:        d.Icon,
		Tags:        d.Tags,
		Schema:      d.Schema,
	}
	for _, sd := range d.Seed {
		m.Seed = append(m.Seed, sd.toDomain())
	}
	for _, f := range d.Formulas {
		m.Formulas = append(m.Formulas, f.toDomain())
	}
	for _, s := range d.Skills {
		m.Skills = append(m.Skills, domain.StarterSkill(s))
	}
	return m
}

func (d seedDTO) toDomain() domain.StarterSeed {
	rows := make([][]any, len(d.Rows))
	for i, row := range d.Rows {
		rows[i] = make([]any, len(row))
		for j, v := range row {
			rows[i][j] = seedCell(v)
		}
	}
	return domain.StarterSeed{Table: d.Table, Columns: d.Columns, Rows: rows}
}

// seedCell normalizes one decoded seed value into the scalar the database binds: JSON numbers arrive as
// float64, so a whole number becomes an int64 (an INTEGER column then stores 1, not 1.0); everything else
// passes through unchanged.
func seedCell(v any) any {
	if n, ok := v.(float64); ok && n == float64(int64(n)) {
		return int64(n)
	}

	return v
}

func (d formulaDTO) toDomain() domain.StarterFormula {
	f := domain.StarterFormula{
		Name:        d.Name,
		Label:       d.Label,
		Description: d.Description,
		SQL:         d.SQL,
		AttachedTo:  d.AttachedTo,
		Hidden:      d.Hidden,
	}
	for _, p := range d.Params {
		f.Params = append(f.Params, p.toDomain())
	}
	if d.Display != nil {
		display := domain.StarterFormulaDisplay{Mode: d.Display.Mode}
		for _, c := range d.Display.Columns {
			display.Columns = append(display.Columns, domain.StarterDisplayColumn(c))
		}
		if s := d.Display.Slots; s != nil {
			display.Slots = domain.StarterDisplaySlots{
				Add:        s.Add,
				Toggle:     s.Toggle,
				Edit:       s.Edit,
				Delete:     s.Delete,
				Detail:     s.Detail,
				Reorder:    s.Reorder,
				Move:       s.Move,
				Bulk:       s.Bulk,
				RowActions: s.RowActions,
			}
		}
		if w := d.Display.Window; w != nil {
			display.Window = &domain.StarterDisplayWindow{
				WeekStart: w.WeekStart, WeekEnd: w.WeekEnd, Start: w.Start, Days: w.Days,
			}
		}
		f.Display = &display
	}
	return f
}

func (d paramDTO) toDomain() domain.StarterFormulaParam {
	p := domain.StarterFormulaParam{
		Name:      d.Name,
		Type:      d.Type,
		Source:    d.Source,
		Optional:  d.Optional,
		Hint:      d.Hint,
		Multiline: d.Multiline,
		Inline:    d.Inline,
		Multiple:  d.Multiple,
		Default:   d.Default,
	}
	for _, v := range d.Defaults {
		p.Defaults = append(p.Defaults, string(v))
	}
	for _, o := range d.Options {
		p.Options = append(p.Options, domain.StarterFormulaOption(o))
	}
	if d.Lookup != nil {
		lookup := domain.StarterLookupConfig(*d.Lookup)
		p.Lookup = &lookup
	}
	if d.When != nil {
		when := domain.StarterParamWhen(*d.When)
		p.When = &when
	}
	for _, field := range d.Fields {
		p.Fields = append(p.Fields, field.toDomain())
	}
	return p
}
