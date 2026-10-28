package service

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// fixedCapResolver returns limits carrying only the given undo-stack cap, whatever the roles, so a test pins
// cleanupStack's resolver wiring without depending on quota.Fixed's role handling.
func fixedCapResolver(undoCap int) quota.Resolver {
	return func(_ []authz.Role) quota.Limits {
		return quota.Limits{UndoStackMaxSize: undoCap}
	}
}

// capArgFromCall returns the integer cap arg from a cleanup-cap call.
// The cap SQL is the per-user DELETE that takes (userID, userID, cap)
// as bound args; the orphan-prune call passes no args.
func capArgFromCall(args []domain.TypedValue) (int64, bool) {
	if len(args) != 3 {
		return 0, false
	}
	v, ok := args[2].Value.(int64)
	return v, ok
}

func TestCleanupStack_UsesRoleResolvedCap_DefaultLimits(t *testing.T) {
	exec := &programmableExecutor{}
	limits := quota.Fixed(quota.Limits{UndoStackMaxSize: 50})
	s := &QueryService{
		executor:      exec,
		quotaResolver: limits,
		dbSvc:         newTestDatabaseService(exec),
	}

	s.cleanupStack(context.Background(), "ns", domain.UndoStackTable, uuid.New())

	if len(exec.calls) != 2 {
		t.Fatalf("expected 2 SQL calls (cap + orphan-prune), got %d: %v", len(exec.calls), exec.calls)
	}
	if !strings.Contains(exec.calls[0], "user_id = ?") {
		t.Errorf("cap SQL should filter by user_id, got: %s", exec.calls[0])
	}
	capArg, ok := capArgFromCall(exec.args[0])
	if !ok || capArg != 50 {
		t.Errorf("cap arg = %d (ok=%v), want 50", capArg, ok)
	}
}

func TestCleanupStack_UsesRoleResolvedCap_ElevatedLimits(t *testing.T) {
	exec := &programmableExecutor{}
	s := &QueryService{
		executor:      exec,
		quotaResolver: fixedCapResolver(200),
		dbSvc:         newTestDatabaseService(exec),
	}

	s.cleanupStack(context.Background(), "ns", domain.UndoStackTable, uuid.New())

	capArg, ok := capArgFromCall(exec.args[0])
	if !ok || capArg != 200 {
		t.Errorf("cap arg = %d (ok=%v), want 200 (resolver-supplied, not default)", capArg, ok)
	}
}

func TestCleanupStack_FallsBackOnZeroOrNegativeCap(t *testing.T) {
	exec := &programmableExecutor{}
	s := &QueryService{
		executor:      exec,
		quotaResolver: fixedCapResolver(0),
		dbSvc:         newTestDatabaseService(exec),
	}

	s.cleanupStack(context.Background(), "ns", domain.UndoStackTable, uuid.New())

	capArg, ok := capArgFromCall(exec.args[0])
	if !ok || capArg != int64(domain.DefaultUndoStackMaxSize) {
		t.Errorf("cap arg = %d (ok=%v), want %d (fallback default)", capArg, ok, domain.DefaultUndoStackMaxSize)
	}
}

func TestCleanupStack_RedoStackAlsoCapped(t *testing.T) {
	exec := &programmableExecutor{}
	s := &QueryService{
		executor:      exec,
		quotaResolver: fixedCapResolver(25),
		dbSvc:         newTestDatabaseService(exec),
	}

	s.cleanupStack(context.Background(), "ns", domain.RedoStackTable, uuid.New())

	if len(exec.calls) != 2 {
		t.Fatalf("expected 2 SQL calls, got %d", len(exec.calls))
	}
	for i, sql := range exec.calls {
		if !strings.Contains(sql, domain.RedoStackTable) {
			t.Errorf("call %d SQL should target %q, got: %s", i, domain.RedoStackTable, sql)
		}
		if strings.Contains(sql, domain.UndoStackTable) {
			t.Errorf("call %d SQL should not mention %q, got: %s", i, domain.UndoStackTable, sql)
		}
	}
}

func TestCleanupStack_PerUserBudget_AliceAndBobIndependent(t *testing.T) {
	// Pins that the per-user filter is wired: each cleanupStack call
	// scopes the cap DELETE to the supplied userID, so one user's
	// invocation can't evict another user's entries.
	exec := &programmableExecutor{}
	s := &QueryService{
		executor:      exec,
		quotaResolver: fixedCapResolver(10),
		dbSvc:         newTestDatabaseService(exec),
	}

	alice := uuid.New()
	bob := uuid.New()

	s.cleanupStack(context.Background(), "ns", domain.UndoStackTable, alice)
	s.cleanupStack(context.Background(), "ns", domain.UndoStackTable, bob)

	if len(exec.args) != 4 {
		t.Fatalf("expected 4 calls (2 per user × cap + orphan-prune), got %d", len(exec.args))
	}
	// First call is alice's cap: args = (aliceID, aliceID, 10).
	if got := exec.args[0][0].Value.(string); got != alice.String() {
		t.Errorf("alice cap user_id = %s, want %s", got, alice.String())
	}
	// Third call is bob's cap.
	if got := exec.args[2][0].Value.(string); got != bob.String() {
		t.Errorf("bob cap user_id = %s, want %s", got, bob.String())
	}
}
