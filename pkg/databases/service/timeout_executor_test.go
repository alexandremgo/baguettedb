package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// stubExecutor lets tests observe the ctx that reached the inner executor and
// optionally block until that ctx is cancelled (or the test releases it).
type stubExecutor struct {
	gotDeadline bool
	hadDeadline bool
	block       chan struct{}
	result      *domain.QueryResult
	txResult    []*domain.QueryResult
}

func (s *stubExecutor) record(ctx context.Context) {
	_, ok := ctx.Deadline()
	s.hadDeadline = ok
	s.gotDeadline = ok
}

func (s *stubExecutor) Execute(
	ctx context.Context, _ string, _ string, _ []domain.TypedValue,
) (*domain.QueryResult, error) {
	s.record(ctx)
	if s.block != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.block:
		}
	}
	return s.result, nil
}

func (s *stubExecutor) ExecuteInternal(
	ctx context.Context, ns string, sql string, args []domain.TypedValue,
) (*domain.QueryResult, error) {
	return s.Execute(ctx, ns, sql, args)
}

func (s *stubExecutor) ExecuteWithRowCap(
	ctx context.Context, ns string, sql string, args []domain.TypedValue, _ int,
) (*domain.QueryResult, error) {
	return s.Execute(ctx, ns, sql, args)
}

func (s *stubExecutor) ExecuteTransaction(
	ctx context.Context, _ string, _ []domain.SQLStatement,
) ([]*domain.QueryResult, error) {
	s.record(ctx)
	if s.block != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.block:
		}
	}
	return s.txResult, nil
}

func (s *stubExecutor) ExecuteTransactionInternal(
	ctx context.Context, ns string, stmts []domain.SQLStatement,
) ([]*domain.QueryResult, error) {
	return s.ExecuteTransaction(ctx, ns, stmts)
}

func (s *stubExecutor) WithConn(ctx context.Context, _ string, fn func(h ParseHandle) error) error {
	s.record(ctx)
	return fn(nil)
}

func (s *stubExecutor) WithTx(ctx context.Context, _ string, _ bool, fn func(ctx context.Context, tx Tx) error) error {
	s.record(ctx)
	return fn(ctx, nil)
}

func TestBoundedExecutor_AppliesDeadline(t *testing.T) {
	stub := &stubExecutor{result: &domain.QueryResult{}}
	exec := NewBoundedExecutor(stub, 50*time.Millisecond)

	if _, err := exec.Execute(context.Background(), "ns", "SELECT 1", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !stub.hadDeadline {
		t.Error("inner executor should have received a ctx with a deadline")
	}
}

func TestBoundedExecutor_TimesOutOnSlowCall(t *testing.T) {
	stub := &stubExecutor{block: make(chan struct{})}
	exec := NewBoundedExecutor(stub, 25*time.Millisecond)
	defer close(stub.block)

	_, err := exec.Execute(context.Background(), "ns", "SELECT 1", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want DeadlineExceeded", err)
	}
}

func TestBoundedExecutor_TimesOutOnSlowTransaction(t *testing.T) {
	stub := &stubExecutor{block: make(chan struct{})}
	exec := NewBoundedExecutor(stub, 25*time.Millisecond)
	defer close(stub.block)

	_, err := exec.ExecuteTransaction(context.Background(), "ns", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want DeadlineExceeded", err)
	}
}

func TestBoundedExecutor_FastCallReturnsResult(t *testing.T) {
	want := &domain.QueryResult{AffectedRowCount: 7}
	stub := &stubExecutor{result: want}
	exec := NewBoundedExecutor(stub, time.Second)

	got, err := exec.Execute(context.Background(), "ns", "SELECT 1", nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != want {
		t.Errorf("result = %+v, want %+v", got, want)
	}
}

// TestBoundedExecutor_NonPositiveTimeoutLeavesCallsUncapped checks that a zero timeout passes calls on with no
// deadline added.
func TestBoundedExecutor_NonPositiveTimeoutLeavesCallsUncapped(t *testing.T) {
	stub := &stubExecutor{result: &domain.QueryResult{}}
	exec := NewBoundedExecutor(stub, 0)

	if _, err := exec.Execute(context.Background(), "ns", "SELECT 1", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if stub.hadDeadline {
		t.Error("inner executor should NOT have received a deadline when max=0")
	}
}

func TestBoundedExecutor_AppliesDeadlineToWithTx(t *testing.T) {
	stub := &stubExecutor{result: &domain.QueryResult{}}
	exec := NewBoundedExecutor(stub, 50*time.Millisecond)

	var fnCtxHadDeadline bool
	err := exec.WithTx(context.Background(), "ns", false, func(ctx context.Context, _ Tx) error {
		_, fnCtxHadDeadline = ctx.Deadline()
		return nil
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
	if !stub.hadDeadline {
		t.Error("inner WithTx should have received a ctx with a deadline")
	}
	if !fnCtxHadDeadline {
		t.Error("fn should have received the bounded ctx (with a deadline)")
	}
}

func TestBoundedExecutor_PreservesUpstreamShorterDeadline(t *testing.T) {
	stub := &stubExecutor{block: make(chan struct{})}
	exec := NewBoundedExecutor(stub, time.Hour)
	defer close(stub.block)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	_, err := exec.Execute(ctx, "ns", "SELECT 1", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want DeadlineExceeded from upstream ctx", err)
	}
}

// TestBoundedExecutor_AppliesDeadlineToRowCappedExecute checks that the row-capped read gets the same per-call
// ceiling as every other executor method.
func TestBoundedExecutor_AppliesDeadlineToRowCappedExecute(t *testing.T) {
	stub := &stubExecutor{result: &domain.QueryResult{}}
	exec := NewBoundedExecutor(stub, 50*time.Millisecond)

	if _, err := exec.ExecuteWithRowCap(context.Background(), "ns", "SELECT 1", nil, 10); err != nil {
		t.Fatalf("ExecuteWithRowCap: %v", err)
	}
	if !stub.hadDeadline {
		t.Error("inner executor should have received a ctx with a deadline")
	}
}
