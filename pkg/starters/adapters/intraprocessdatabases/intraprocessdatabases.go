// Package intraprocessdatabases adapts the starters service's databases port to direct in-process calls, for
// the single-binary deployment where the databases module is co-located. One flat intraprocess* package per
// target app keeps a binary compiling only the adapters it wires.
package intraprocessdatabases

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	dbdomain "github.com/alexandremgo/baguettedb/pkg/databases/domain"
	dbservice "github.com/alexandremgo/baguettedb/pkg/databases/service"
	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
)

// DatabasesApp wraps the databases module's query, formula, skill, and database services for in-process use.
type DatabasesApp struct {
	querySvc   *dbservice.QueryService
	formulaSvc *dbservice.FormulaService
	skillSvc   *dbservice.SkillService
	dbSvc      *dbservice.DatabaseService
}

// New creates a new DatabasesApp.
func New(
	querySvc *dbservice.QueryService,
	formulaSvc *dbservice.FormulaService,
	skillSvc *dbservice.SkillService,
	dbSvc *dbservice.DatabaseService,
) *DatabasesApp {
	return &DatabasesApp{querySvc: querySvc, formulaSvc: formulaSvc, skillSvc: skillSvc, dbSvc: dbSvc}
}

// ExecuteSchema runs each statement through the same schema-intent execution path a user's execute_schema call
// takes: classification, permission checks, reserved-prefix protection, and change-log capture all apply. One
// call per statement — classification prepares against the pre-execution schema, so a statement depending on an
// earlier one (an index on a fresh table) can only be classified after that one has executed.
func (a *DatabasesApp) ExecuteSchema(ctx context.Context, userID, bdbID uuid.UUID, statements []string) error {
	for i, stmt := range statements {
		if _, err := a.querySvc.Execute(ctx, userID, bdbID, stmt, nil, dbdomain.IntentSchema); err != nil {
			return fmt.Errorf("statement %d: %w", i, err)
		}
	}
	return nil
}

// InsertRows runs the seed's multi-row INSERT through the data-intent execution path a user's execute_data
// call takes, so permission checks and change-log capture apply to seeded rows too.
func (a *DatabasesApp) InsertRows(ctx context.Context, userID, bdbID uuid.UUID, seed domain.StarterSeed) error {
	sql, args := seed.InsertStatement()
	typed := make([]dbdomain.TypedValue, len(args))
	for i, v := range args {
		switch v := v.(type) {
		case nil:
			typed[i] = dbdomain.NullValue()
		case string:
			typed[i] = dbdomain.TextValue(v)
		case bool:
			typed[i] = dbdomain.IntegerValue(boolInt(v))
		case int:
			typed[i] = dbdomain.IntegerValue(int64(v))
		case int64:
			typed[i] = dbdomain.IntegerValue(v)
		case float64:
			typed[i] = dbdomain.RealValue(v)
		default:
			return fmt.Errorf("seed value %d: unsupported %T", i, v)
		}
	}
	if _, err := a.querySvc.Execute(ctx, userID, bdbID, sql, typed, dbdomain.IntentExecute); err != nil {
		return err
	}
	return nil
}

// boolInt is SQLite's boolean storage: 1 for true, 0 for false.
func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// CreateFormula delegates to the databases module's formula service and returns the new formula's ID. The
// formula kind is derived from the SQL by the service; attachedToID carries the resolved attach target.
func (a *DatabasesApp) CreateFormula(
	ctx context.Context, userID, bdbID uuid.UUID,
	formula domain.StarterFormula, attachedToID *uuid.UUID,
) (uuid.UUID, error) {
	created, err := a.formulaSvc.CreateFormula(ctx, userID, bdbID, dbservice.FormulaInput{
		Name:                formula.Name,
		Label:               formula.Label,
		Description:         formula.Description,
		SQL:                 formula.SQL,
		Params:              toParamSchema(formula.Params),
		AttachedToFormulaID: attachedToID,
		Hidden:              formula.Hidden,
	})
	if err != nil {
		return uuid.Nil, err
	}
	return created.ID, nil
}

// UpdateFormulaDisplay stores the formula's display config through the same service update (validation, slot
// resolution to UUIDs) as the public PUT.
func (a *DatabasesApp) UpdateFormulaDisplay(
	ctx context.Context, userID, bdbID uuid.UUID, ref string, display domain.StarterFormulaDisplay,
) error {
	_, err := a.formulaSvc.UpdateFormula(ctx, userID, bdbID, ref, dbservice.FormulaPatch{
		Display: toDomainDisplay(display),
	})
	return err
}

// toDomainDisplay converts a starter display config into the databases domain shape.
func toDomainDisplay(d domain.StarterFormulaDisplay) *dbdomain.FormulaDisplay {
	out := &dbdomain.FormulaDisplay{
		Mode: dbdomain.DisplayMode(d.Mode),
		Slots: dbdomain.FormulaDisplaySlots{
			Add:        d.Slots.Add,
			Toggle:     d.Slots.Toggle,
			Edit:       d.Slots.Edit,
			Delete:     d.Slots.Delete,
			Detail:     d.Slots.Detail,
			Reorder:    d.Slots.Reorder,
			Move:       d.Slots.Move,
			Bulk:       d.Slots.Bulk,
			RowActions: d.Slots.RowActions,
		},
	}
	for _, c := range d.Columns {
		out.Columns = append(out.Columns, dbdomain.FormulaDisplayColumn{
			Column: c.Column, Role: dbdomain.DisplayColumnRole(c.Role),
			Format: dbdomain.DisplayColumnFormat(c.Format), Hidden: c.Hidden,
		})
	}
	if w := d.Window; w != nil {
		out.Window = &dbdomain.FormulaDisplayWindow{
			WeekStart: dbdomain.DisplayWeekday(w.WeekStart), WeekEnd: dbdomain.DisplayWeekday(w.WeekEnd),
			Start: w.Start, Days: w.Days,
		}
	}
	return out
}

// CreateSkill delegates to the databases module's skill service.
func (a *DatabasesApp) CreateSkill(ctx context.Context, userID, bdbID uuid.UUID, skill domain.StarterSkill) error {
	_, err := a.skillSvc.CreateSkill(ctx, userID, bdbID, dbservice.SkillInput{
		Name:         skill.Name,
		Description:  skill.Description,
		Body:         skill.Body,
		TargetTables: skill.Targets,
	})
	return err
}

// DeleteDatabase removes the bdb's database. Idempotent: returns nil if no database exists for the bdb.
func (a *DatabasesApp) DeleteDatabase(ctx context.Context, bdbID uuid.UUID) error {
	return a.dbSvc.DeleteBdbDatabase(ctx, bdbID)
}

// toParamSchema converts starter formula params into the databases parameter schema.
func toParamSchema(params []domain.StarterFormulaParam) dbdomain.ParamSchema {
	if len(params) == 0 {
		return nil
	}
	out := make(dbdomain.ParamSchema, len(params))
	for i, p := range params {
		out[i] = toFormulaParam(p)
	}
	return out
}

// toFormulaParam converts one starter parameter — group fields included — into the databases
// domain shape.
func toFormulaParam(p domain.StarterFormulaParam) dbdomain.FormulaParam {
	param := dbdomain.FormulaParam{
		Name:      p.Name,
		Type:      p.Type,
		Source:    dbdomain.ParamSource(p.Source),
		Optional:  p.Optional,
		Hint:      p.Hint,
		Multiline: p.Multiline,
		Inline:    p.Inline,
		Multiple:  p.Multiple,
		Default:   p.Default,
		Defaults:  p.Defaults,
	}
	for _, o := range p.Options {
		param.Options = append(param.Options, dbdomain.FormulaOption(o))
	}
	if p.Lookup != nil {
		param.Lookup = &dbdomain.LookupConfig{
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
		param.When = &dbdomain.ParamWhen{Param: p.When.Param, Op: p.When.Op, Value: p.When.Value}
	}
	for _, field := range p.Fields {
		param.Fields = append(param.Fields, toFormulaParam(field))
	}
	return param
}
