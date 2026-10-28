package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
)

// formulaRunner runs a bdb's formulas in the databases app.
type formulaRunner interface {
	// RunFormula runs the formula ref (a name or an ID) of the bdb bdbID with params, each value a string the
	// databases app converts to the parameter's declared type. An unknown formula wraps domain.ErrFormulaNotFound.
	RunFormula(ctx context.Context, bdbID uuid.UUID, ref string, params map[string]string) (domain.FormulaRun, error)
}

// formulaReader reads one of a bdb's formulas in the databases app.
type formulaReader interface {
	// GetFormula returns the formula ref (a name or an ID) of the bdb bdbID with its parameters and SQL. An unknown
	// formula wraps domain.ErrFormulaNotFound.
	GetFormula(ctx context.Context, bdbID uuid.UUID, ref string) (domain.FormulaDetail, error)
}

// FormulaService runs the formula use cases: listing, showing and running.
type FormulaService struct {
	bdbs     bdbResolver
	formulas formulaLister
	reader   formulaReader
	runner   formulaRunner
}

// NewFormulaService creates a FormulaService resolving bdbs with bdbs and listing, reading and running formulas in
// the databases app.
func NewFormulaService(
	bdbs bdbResolver, formulas formulaLister, reader formulaReader, runner formulaRunner,
) *FormulaService {
	return &FormulaService{bdbs: bdbs, formulas: formulas, reader: reader, runner: runner}
}

// List returns the formulas of the bdb ref names, hidden ones included.
func (s *FormulaService) List(ctx context.Context, ref string) (domain.Bdb, []apptypes.Formula, error) {
	b, err := s.bdbs.Resolve(ctx, ref)
	if err != nil {
		return domain.Bdb{}, nil, err
	}
	formulas, err := s.formulas.ListFormulas(ctx, b.ID)
	if err != nil {
		return b, nil, fmt.Errorf("in %s: listing formulas: %w", b.Name, err)
	}
	return b, formulas, nil
}

// Show returns the formula name of the bdb ref names, with its parameters and SQL.
func (s *FormulaService) Show(ctx context.Context, ref, name string) (domain.Bdb, domain.FormulaDetail, error) {
	b, err := s.bdbs.Resolve(ctx, ref)
	if err != nil {
		return domain.Bdb{}, domain.FormulaDetail{}, err
	}
	f, err := s.reader.GetFormula(ctx, b.ID, name)
	if err != nil {
		return b, domain.FormulaDetail{}, fmt.Errorf("in %s: %w", b.Name, err)
	}
	return b, f, nil
}

// Run runs the formula name of the bdb ref names with params.
func (s *FormulaService) Run(
	ctx context.Context, ref, name string, params map[string]string,
) (domain.Bdb, domain.FormulaRun, error) {
	b, err := s.bdbs.Resolve(ctx, ref)
	if err != nil {
		return domain.Bdb{}, domain.FormulaRun{}, err
	}
	run, err := s.runner.RunFormula(ctx, b.ID, name, params)
	if err != nil {
		return b, domain.FormulaRun{}, fmt.Errorf("in %s: %w", b.Name, err)
	}
	return b, run, nil
}
