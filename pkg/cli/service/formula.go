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

// formulaWriter creates, changes and deletes a bdb's formulas in the databases app.
type formulaWriter interface {
	// CreateFormula creates a formula in the bdb bdbID from spec, the formula as a JSON object, and returns it.
	CreateFormula(ctx context.Context, bdbID uuid.UUID, spec []byte) (domain.FormulaDetail, error)

	// UpdateFormula changes the formula ref (a name or an ID) of the bdb bdbID from spec, a JSON object of the fields
	// to change, and returns it. An unknown formula wraps domain.ErrFormulaNotFound.
	UpdateFormula(ctx context.Context, bdbID uuid.UUID, ref string, spec []byte) (domain.FormulaDetail, error)

	// DeleteFormula deletes the formula ref (a name or an ID) of the bdb bdbID. An unknown formula wraps
	// domain.ErrFormulaNotFound.
	DeleteFormula(ctx context.Context, bdbID uuid.UUID, ref string) error
}

// FormulaService runs the formula use cases: listing, showing, running, creating, changing and deleting.
type FormulaService struct {
	bdbs     bdbResolver
	formulas formulaLister
	reader   formulaReader
	runner   formulaRunner
	writer   formulaWriter
}

// NewFormulaService creates a FormulaService resolving bdbs with bdbs and listing, reading, running and writing
// formulas in the databases app.
func NewFormulaService(
	bdbs bdbResolver, formulas formulaLister, reader formulaReader, runner formulaRunner, writer formulaWriter,
) *FormulaService {
	return &FormulaService{bdbs: bdbs, formulas: formulas, reader: reader, runner: runner, writer: writer}
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

// Create creates a formula in the bdb ref names from spec, the formula as a JSON object.
func (s *FormulaService) Create(
	ctx context.Context, ref string, spec []byte,
) (domain.Bdb, domain.FormulaDetail, error) {
	b, err := s.bdbs.Resolve(ctx, ref)
	if err != nil {
		return domain.Bdb{}, domain.FormulaDetail{}, err
	}
	f, err := s.writer.CreateFormula(ctx, b.ID, spec)
	if err != nil {
		return b, domain.FormulaDetail{}, fmt.Errorf("in %s: %w", b.Name, err)
	}
	return b, f, nil
}

// Update changes the formula name of the bdb ref names from spec, a JSON object of the fields to change.
func (s *FormulaService) Update(
	ctx context.Context, ref, name string, spec []byte,
) (domain.Bdb, domain.FormulaDetail, error) {
	b, err := s.bdbs.Resolve(ctx, ref)
	if err != nil {
		return domain.Bdb{}, domain.FormulaDetail{}, err
	}
	f, err := s.writer.UpdateFormula(ctx, b.ID, name, spec)
	if err != nil {
		return b, domain.FormulaDetail{}, fmt.Errorf("in %s: %w", b.Name, err)
	}
	return b, f, nil
}

// Delete deletes the formula name of the bdb ref names.
func (s *FormulaService) Delete(ctx context.Context, ref, name string) (domain.Bdb, error) {
	b, err := s.bdbs.Resolve(ctx, ref)
	if err != nil {
		return domain.Bdb{}, err
	}
	if err := s.writer.DeleteFormula(ctx, b.ID, name); err != nil {
		return b, fmt.Errorf("in %s: %w", b.Name, err)
	}
	return b, nil
}
