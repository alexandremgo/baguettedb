package intraprocess

import (
	"context"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/databases/appconv"
	dbservice "github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// DatabasesApp adapts the databases module's query, skill, formula, and database services to the bdbs
// DatabasesApp port for in-process use, converting their domain results into the cross-app transfer shapes.
type DatabasesApp struct {
	querySvc   *dbservice.QueryService
	skillSvc   *dbservice.SkillService
	formulaSvc *dbservice.FormulaService
	dbSvc      *dbservice.DatabaseService
}

// NewDatabasesApp creates a new DatabasesApp.
func NewDatabasesApp(
	querySvc *dbservice.QueryService,
	skillSvc *dbservice.SkillService,
	formulaSvc *dbservice.FormulaService,
	dbSvc *dbservice.DatabaseService,
) *DatabasesApp {
	return &DatabasesApp{querySvc: querySvc, skillSvc: skillSvc, formulaSvc: formulaSvc, dbSvc: dbSvc}
}

// DeleteDatabase removes the per-bdb SQLite database (file + record). Idempotent: returns nil if no database exists
// for the bdb.
func (a *DatabasesApp) DeleteDatabase(ctx context.Context, bdbID uuid.UUID) error {
	return a.dbSvc.DeleteBdbDatabase(ctx, bdbID)
}

// ListTables returns the lightweight table list for the given bdb.
func (a *DatabasesApp) ListTables(ctx context.Context, userID, bdbID uuid.UUID) ([]apptypes.TableInfo, error) {
	infos, err := a.querySvc.ListTablesInfo(ctx, userID, bdbID)
	if err != nil {
		return nil, err
	}
	return appconv.TableInfos(infos), nil
}

// ListTablesWithSchemas returns all tables with their full schema for the given bdb.
func (a *DatabasesApp) ListTablesWithSchemas(
	ctx context.Context, userID, bdbID uuid.UUID,
) ([]apptypes.TableSchema, error) {
	schemas, err := a.querySvc.ListTablesWithSchemas(ctx, userID, bdbID)
	if err != nil {
		return nil, err
	}
	return appconv.TableSchemas(schemas), nil
}

// ListSkills returns all skills in the given bdb, each with its full body and target tables.
func (a *DatabasesApp) ListSkills(ctx context.Context, userID, bdbID uuid.UUID) ([]apptypes.Skill, error) {
	skills, err := a.skillSvc.ListSkills(ctx, userID, bdbID)
	if err != nil {
		return nil, err
	}
	return appconv.Skills(skills), nil
}

// ListFormulas returns the bdb's formulas as lightweight summaries, hidden ones included.
func (a *DatabasesApp) ListFormulas(ctx context.Context, userID, bdbID uuid.UUID) ([]apptypes.Formula, error) {
	formulas, err := a.formulaSvc.ListFormulasIncludingHidden(ctx, userID, bdbID)
	if err != nil {
		return nil, err
	}
	return appconv.Formulas(formulas), nil
}
