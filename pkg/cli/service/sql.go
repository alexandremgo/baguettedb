package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// bdbResolver finds the account's bdb a reference names.
type bdbResolver interface {
	// Resolve returns the bdb ref names, wrapping domain.ErrBdbNotFound for no match and returning a
	// domain.AmbiguousBdbError for several.
	Resolve(ctx context.Context, ref string) (domain.Bdb, error)
}

// statementExecutor runs SQL in a bdb in the databases app.
type statementExecutor interface {
	// Execute runs sql in the bdb bdbID and returns its result. params holds the values of the SQL's named
	// placeholders, keyed by name and bound as text. The databases app refuses a statement whose kind is not intent
	// (*domain.StatementKindError) and a placeholder left without a value (*domain.ParametersError), applies the same
	// caps as every other surface, and records changes in the change log.
	Execute(
		ctx context.Context, bdbID uuid.UUID, sql string, intent domain.Intent, params map[string]string,
	) (domain.QueryResult, error)

	// ExecuteJSON is Execute with the values given as a JSON object keyed by placeholder name, each bound as JSON
	// types it.
	ExecuteJSON(
		ctx context.Context, bdbID uuid.UUID, sql string, intent domain.Intent, params []byte,
	) (domain.QueryResult, error)
}

// SQLService runs SQL statements in the account's bdbs.
type SQLService struct {
	bdbs       bdbResolver
	statements statementExecutor
}

// NewSQLService creates a SQLService resolving bdbs with bdbs and running statements in the databases app.
func NewSQLService(bdbs bdbResolver, statements statementExecutor) *SQLService {
	return &SQLService{bdbs: bdbs, statements: statements}
}

// Run runs sql, of the kind intent, in the bdb ref names, and returns the bdb with the result. params holds the values
// of the SQL's named placeholders, keyed by name and bound as text.
func (s *SQLService) Run(
	ctx context.Context, ref, sql string, intent domain.Intent, params map[string]string,
) (domain.Bdb, domain.QueryResult, error) {
	b, err := s.bdbs.Resolve(ctx, ref)
	if err != nil {
		return domain.Bdb{}, domain.QueryResult{}, err
	}
	result, err := s.statements.Execute(ctx, b.ID, sql, intent, params)
	if err != nil {
		return b, domain.QueryResult{}, fmt.Errorf("in %s: %w", b.Name, err)
	}
	return b, result, nil
}

// RunJSON is Run with the values given as params, a JSON object keyed by placeholder name, each bound as JSON types
// it.
func (s *SQLService) RunJSON(
	ctx context.Context, ref, sql string, intent domain.Intent, params []byte,
) (domain.Bdb, domain.QueryResult, error) {
	b, err := s.bdbs.Resolve(ctx, ref)
	if err != nil {
		return domain.Bdb{}, domain.QueryResult{}, err
	}
	result, err := s.statements.ExecuteJSON(ctx, b.ID, sql, intent, params)
	if err != nil {
		return b, domain.QueryResult{}, fmt.Errorf("in %s: %w", b.Name, err)
	}
	return b, result, nil
}
