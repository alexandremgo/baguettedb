package service

import (
	"context"
	"slices"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// programmableExecutor is a queryExecutor whose ExecuteInternal can be scripted: each call pops the next canned
// result. WithTx yields the double itself as the Tx handle, so statements issued inside a transaction go through the
// same scripting and call log, tagged in txDepths with the transaction depth they ran at. Execute and the
// ExecuteTransaction variants are no-ops.
//
// matchFn, when non-nil, takes precedence over the results slice: the canned result is selected by pattern-matching
// on the SQL, which keeps tests independent of map-iteration order on the caller side.
type programmableExecutor struct {
	results  []*domain.QueryResult
	matchFn  func(sql string) *domain.QueryResult
	calls    []string
	args     [][]domain.TypedValue
	txDepth  int
	txDepths []int
}

func (p *programmableExecutor) Execute(
	_ context.Context, _ string, _ string, _ []domain.TypedValue,
) (*domain.QueryResult, error) {
	return &domain.QueryResult{}, nil
}

func (p *programmableExecutor) ExecuteWithRowCap(
	ctx context.Context, ns string, sql string, args []domain.TypedValue, _ int,
) (*domain.QueryResult, error) {
	return p.Execute(ctx, ns, sql, args)
}

func (p *programmableExecutor) ExecuteInternal(
	_ context.Context, _ string, sql string, args []domain.TypedValue,
) (*domain.QueryResult, error) {
	p.calls = append(p.calls, sql)
	p.args = append(p.args, args)
	p.txDepths = append(p.txDepths, p.txDepth)
	if p.matchFn != nil {
		if r := p.matchFn(sql); r != nil {
			return r, nil
		}
		return &domain.QueryResult{}, nil
	}
	if len(p.results) == 0 {
		return &domain.QueryResult{}, nil
	}
	r := p.results[0]
	p.results = p.results[1:]
	return r, nil
}

func (p *programmableExecutor) ExecuteTransaction(
	_ context.Context, _ string, _ []domain.SQLStatement,
) ([]*domain.QueryResult, error) {
	return nil, nil
}

func (p *programmableExecutor) ExecuteTransactionInternal(
	_ context.Context, _ string, _ []domain.SQLStatement,
) ([]*domain.QueryResult, error) {
	return nil, nil
}

func (p *programmableExecutor) WithConn(_ context.Context, _ string, fn func(ParseHandle) error) error {
	return fn(nil)
}

func (p *programmableExecutor) WithTx(ctx context.Context, _ string, _ bool, fn func(context.Context, Tx) error) error {
	p.txDepth++
	defer func() { p.txDepth-- }()
	return fn(ctx, p)
}

// Exec satisfies Tx so the double can stand in as a transaction handle. A handle is bound to one database, so the
// statement is recorded like any other call without a database id.
func (p *programmableExecutor) Exec(
	ctx context.Context, sql string, args []domain.TypedValue,
) (*domain.QueryResult, error) {
	return p.ExecuteInternal(ctx, "", sql, args)
}

// ExecInternal satisfies Tx; the double has no authorizer, so it records the statement like Exec does.
func (p *programmableExecutor) ExecInternal(
	ctx context.Context, sql string, args []domain.TypedValue,
) (*domain.QueryResult, error) {
	return p.Exec(ctx, sql, args)
}

// Parse satisfies Tx; the double has no schema to resolve against, so it yields an empty parse result.
func (p *programmableExecutor) Parse(_ string) (domain.ParseResult, error) {
	return domain.ParseResult{}, nil
}

// TestProgrammableExecutor_TagsCallsInsideWithTx pins the double's transaction awareness: WithTx yields a handle whose
// statements are recorded like any other call, tagged with the transaction depth they ran at.
func TestProgrammableExecutor_TagsCallsInsideWithTx(t *testing.T) {
	ctx := context.Background()
	p := &programmableExecutor{}

	if _, err := p.ExecuteInternal(ctx, "ns", "SELECT 1", nil); err != nil {
		t.Fatalf("ExecuteInternal: %v", err)
	}
	err := p.WithTx(ctx, "ns", true, func(ctx context.Context, tx Tx) error {
		if tx == nil {
			t.Fatal("WithTx must yield a Tx handle")
		}
		_, err := tx.Exec(ctx, "SELECT 2", nil)
		return err
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
	if _, err := p.ExecuteInternal(ctx, "ns", "SELECT 3", nil); err != nil {
		t.Fatalf("ExecuteInternal: %v", err)
	}

	if want := []string{"SELECT 1", "SELECT 2", "SELECT 3"}; !slices.Equal(p.calls, want) {
		t.Errorf("calls = %v, want %v", p.calls, want)
	}
	if want := []int{0, 1, 0}; !slices.Equal(p.txDepths, want) {
		t.Errorf("txDepths = %v, want %v", p.txDepths, want)
	}
}
