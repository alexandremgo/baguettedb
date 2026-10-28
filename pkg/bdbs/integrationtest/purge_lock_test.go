//go:build integration

package integrationtest

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/bdbs/service"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
)

// newPurgeService builds a BdbService wired with a real purge lock and a no-op databases app, so PurgeExpired
// runs the full sweep (the databases app must be set or it short-circuits before taking the lock).
func newPurgeService(t *testing.T) *service.BdbService {
	t.Helper()
	svc := newBdbService(&testAuthApp{}, quota.Fixed(quota.Limits{MaxBdbs: 1000, MaxMembersPerBdb: 100}))
	svc.SetDatabasesApp(&mockDatabasesApp{})
	svc.SetPurgeLock(newPurgeLock())
	return svc
}

// TestPurgeLock_MutualExclusion checks the purge lock primitive of the store the suite runs on: while one holder
// owns it a second lock's TryAcquire fails without blocking, and once released the lock is takeable again.
func TestPurgeLock_MutualExclusion(t *testing.T) {
	ctx := t.Context()
	lock, other := newPurgeLock(), newPurgeLock()

	acquired1, release1, err := lock.TryAcquire(ctx)
	if err != nil {
		t.Fatalf("first TryAcquire: %v", err)
	}
	if !acquired1 {
		t.Fatal("expected the first TryAcquire to succeed")
	}

	acquired2, release2, err := other.TryAcquire(ctx)
	if err != nil {
		t.Fatalf("second TryAcquire: %v", err)
	}
	if acquired2 {
		release2(ctx)
		t.Fatal("expected the second TryAcquire to fail while the lock is held")
	}

	release1(ctx)

	acquired3, release3, err := other.TryAcquire(ctx)
	if err != nil {
		t.Fatalf("third TryAcquire: %v", err)
	}
	if !acquired3 {
		t.Fatal("expected TryAcquire to succeed again after release")
	}
	release3(ctx)
}

// TestPurgeExpired_ConcurrentSweepsSingleFlight checks the lock makes the sweep single-flight across replicas: two
// BdbServices (standing in for two pods) sweep the same expired bdbs at once, yet exactly one does the work
// — the other no-ops with zero scanned and zero failures, and every purgeable bdb is removed exactly once.
func TestPurgeExpired_ConcurrentSweepsSingleFlight(t *testing.T) {
	truncateAll(t)
	ctx := t.Context()

	owner := uuid.MustParse(seedUser(t, "purge-owner@test.com"))
	roles := []authz.Role{authz.RoleUser}

	const n = 10
	creator := newPurgeService(t)
	for i := range n {
		c, err := creator.CreateBdb(ctx, owner, roles, fmt.Sprintf("Purge-%d", i), "")
		if err != nil {
			t.Fatalf("creating bdb: %v", err)
		}
		if err := creator.DeleteBdb(ctx, owner, c.ID); err != nil {
			t.Fatalf("soft-deleting bdb: %v", err)
		}
	}

	// A "now" past the grace period so every soft-deleted bdb above is purgeable this sweep.
	future := time.Now().UTC().Add(domain.GracePeriod + time.Hour)

	svcA, svcB := newPurgeService(t), newPurgeService(t)
	var statsA, statsB service.PurgeStats
	var errA, errB error
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); <-start; statsA, errA = svcA.PurgeExpired(ctx, future) }()
	go func() { defer wg.Done(); <-start; statsB, errB = svcB.PurgeExpired(ctx, future) }()
	close(start)
	wg.Wait()

	if errA != nil || errB != nil {
		t.Fatalf("sweep errors: A=%v B=%v", errA, errB)
	}

	// One sweep won the lock and did everything; the other found the lock held and no-opped.
	ran, skipped := statsA, statsB
	if ran.Scanned == 0 {
		ran, skipped = statsB, statsA
	}
	if skipped.Scanned != 0 {
		t.Fatalf("expected the losing sweep to no-op, got scanned=%d purged=%d", skipped.Scanned, skipped.Purged)
	}
	if ran.Scanned != n || ran.Purged != n || ran.Failed != 0 {
		t.Fatalf("expected the winning sweep to purge all cleanly: scanned=%d purged=%d failed=%d",
			ran.Scanned, ran.Purged, ran.Failed)
	}

	var remaining int
	if err := testDB.QueryRow(ctx, "SELECT COUNT(*) FROM bdbs").Scan(&remaining); err != nil {
		t.Fatalf("counting bdbs after purge: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("expected all %d bdbs purged, %d remain", n, remaining)
	}
}
