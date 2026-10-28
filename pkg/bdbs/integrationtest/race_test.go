//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
)

// These race tests run at the service / repository level (against the real Postgres testcontainer) rather than
// through HTTP: the read-then-write races they target are timing-sensitive, and the extra HTTP/auth latency staggers
// the goroutines enough to hide the race. Driving the contended path directly keeps the contention tight and the
// regressions reliable.

// TestCreateBdb_ConcurrentAtLimitAdmitsOne checks the per-user bdb quota holds under concurrency: with a limit
// of 1, many simultaneous CreateBdb calls by the same user yield exactly one bdb. The per-owner advisory lock
// serializes the count-check-insert so the rest see the limit reached. Without the lock several read a count of 0
// and all succeed.
func TestCreateBdb_ConcurrentAtLimitAdmitsOne(t *testing.T) {
	truncateAll(t)
	ctx := t.Context()

	svc := newBdbService(&testAuthApp{}, quota.Fixed(quota.Limits{MaxBdbs: 1, MaxMembersPerBdb: 100}))

	userID := uuid.MustParse(seedUser(t, "creator@test.com"))
	roles := []authz.Role{authz.RoleUser}

	// High concurrency widens the count-then-insert window so the race is reliably exercised when the lock is
	// absent (a lucky all-serialize outcome becomes vanishingly unlikely); with the lock it stays deterministic.
	const n = 24
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			_, errs[idx] = svc.CreateBdb(ctx, userID, roles, fmt.Sprintf("Race-%d", idx), "")
		}(i)
	}
	close(start)
	wg.Wait()

	var wins, limited int
	for _, e := range errs {
		var limitErr domain.BdbLimitReachedError
		switch {
		case e == nil:
			wins++
		case errors.As(e, &limitErr):
			limited++
		default:
			t.Errorf("unexpected error: %v", e)
		}
	}
	if wins != 1 {
		t.Fatalf("expected exactly 1 create to win at limit 1, got %d (limited=%d)", wins, limited)
	}
	if limited != n-1 {
		t.Fatalf("expected %d limit errors, got %d", n-1, limited)
	}

	var owned int
	if err := testDB.QueryRow(ctx,
		"SELECT COUNT(*) FROM bdb_members WHERE user_id = $1 AND role = 'owner'", userID).Scan(&owned); err != nil {
		t.Fatalf("counting owned bdbs: %v", err)
	}
	if owned != 1 {
		t.Fatalf("expected exactly 1 owned bdb after the race, got %d", owned)
	}
}

// TestAcceptInvite_ConcurrentSameInviteAdmitsOne checks that a single-use invite admits exactly one user when many
// accept it at once: one accept joins the bdb and the rest get domain.ErrInviteAlreadyUsed, whether the in-memory
// check or the stored invite's `status = 'pending'` guard stops them.
func TestAcceptInvite_ConcurrentSameInviteAdmitsOne(t *testing.T) {
	truncateAll(t)
	ctx := t.Context()
	limits := quota.Fixed(quota.Limits{MaxBdbs: 10, MaxMembersPerBdb: 100})
	bdbSvc, inviteSvc := newBdbService(&testAuthApp{}, limits), newInviteService(&testAuthApp{}, limits)

	ownerID := uuid.MustParse(seedUser(t, "owner@test.com"))
	bdb, err := bdbSvc.CreateBdb(ctx, ownerID, []authz.Role{authz.RoleUser}, "Guard Bdb", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}
	invite := seedInvite(t, bdb.ID, ownerID)

	const n = 8
	joiners := make([]uuid.UUID, n)
	for i := range joiners {
		joiners[i] = uuid.MustParse(seedUser(t, fmt.Sprintf("joiner-%d@test.com", i)))
	}
	errs := acceptConcurrently(t, inviteSvc, func(i int) (string, uuid.UUID) { return invite.Token, joiners[i] }, n)

	var wins, alreadyUsed int
	for _, e := range errs {
		switch {
		case e == nil:
			wins++
		case errors.Is(e, domain.ErrInviteAlreadyUsed):
			alreadyUsed++
		default:
			t.Errorf("unexpected error: %v", e)
		}
	}
	if wins != 1 || alreadyUsed != n-1 {
		t.Errorf("wins %d and already-used %d, want 1 and %d", wins, alreadyUsed, n-1)
	}
	if got := countMembers(t, bdb.ID); got != 2 {
		t.Errorf("members stored = %d, want 2 (the owner and one joiner)", got)
	}
}

// TestAcceptInvite_ConcurrentAtMemberCapAdmitsUpToCap checks that the member cap holds when distinct invites are
// accepted at once on a near-full bdb: with a cap of 3 (the owner and 2 seats), exactly two joiners get in and the
// rest get domain.MemberLimitReachedError.
func TestAcceptInvite_ConcurrentAtMemberCapAdmitsUpToCap(t *testing.T) {
	truncateAll(t)
	ctx := t.Context()
	const maxMembers = 3
	limits := quota.Fixed(quota.Limits{MaxBdbs: 10, MaxMembersPerBdb: maxMembers})
	bdbSvc, inviteSvc := newBdbService(&testAuthApp{}, limits), newInviteService(&testAuthApp{}, limits)

	ownerID := uuid.MustParse(seedUser(t, "owner@test.com"))
	bdb, err := bdbSvc.CreateBdb(ctx, ownerID, []authz.Role{authz.RoleUser}, "Cap Bdb", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}

	const n = 8
	tokens := make([]string, n)
	joiners := make([]uuid.UUID, n)
	for i := range n {
		tokens[i] = seedInvite(t, bdb.ID, ownerID).Token
		joiners[i] = uuid.MustParse(seedUser(t, fmt.Sprintf("joiner-%d@test.com", i)))
	}
	errs := acceptConcurrently(t, inviteSvc, func(i int) (string, uuid.UUID) { return tokens[i], joiners[i] }, n)

	admitted, limited := countAdmittedAndLimited(t, errs, maxMembers)
	if admitted != maxMembers-1 || limited != n-(maxMembers-1) {
		t.Errorf("admitted %d and limited %d, want %d and %d", admitted, limited, maxMembers-1, n-(maxMembers-1))
	}
	if got := countMembers(t, bdb.ID); got != maxMembers {
		t.Errorf("members stored = %d, want %d", got, maxMembers)
	}
}

// TestMemberCap_DirectAddsAndInvitesShareTheCap checks that direct adds by the owner and invite accepts racing on the
// same near-full bdb share one cap: with a cap of 3, exactly two of them get in, whichever path they take.
func TestMemberCap_DirectAddsAndInvitesShareTheCap(t *testing.T) {
	truncateAll(t)
	ctx := t.Context()
	const maxMembers = 3
	limits := quota.Fixed(quota.Limits{MaxBdbs: 10, MaxMembersPerBdb: maxMembers})
	bdbSvc, inviteSvc := newBdbService(&testAuthApp{}, limits), newInviteService(&testAuthApp{}, limits)
	roles := []authz.Role{authz.RoleUser}

	ownerID := uuid.MustParse(seedUser(t, "owner@test.com"))
	bdb, err := bdbSvc.CreateBdb(ctx, ownerID, roles, "Shared Cap Bdb", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}

	const perPath = 4
	tokens := make([]string, perPath)
	invited := make([]uuid.UUID, perPath)
	added := make([]uuid.UUID, perPath)
	for i := range perPath {
		tokens[i] = seedInvite(t, bdb.ID, ownerID).Token
		invited[i] = uuid.MustParse(seedUser(t, fmt.Sprintf("invited-%d@test.com", i)))
		added[i] = uuid.MustParse(seedUser(t, fmt.Sprintf("added-%d@test.com", i)))
	}

	errs := make([]error, 2*perPath)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range perPath {
		wg.Go(func() {
			<-start
			_, errs[i] = inviteSvc.AcceptInvite(ctx, tokens[i], invited[i])
		})
		wg.Go(func() {
			<-start
			_, errs[perPath+i] = bdbSvc.AddMember(ctx, ownerID, bdb.ID, added[i], authz.BdbRoleViewer, roles)
		})
	}
	close(start)
	wg.Wait()

	admitted, limited := countAdmittedAndLimited(t, errs, maxMembers)
	if admitted != maxMembers-1 || limited != 2*perPath-(maxMembers-1) {
		t.Errorf("admitted %d and limited %d, want %d and %d",
			admitted, limited, maxMembers-1, 2*perPath-(maxMembers-1))
	}
	if got := countMembers(t, bdb.ID); got != maxMembers {
		t.Errorf("members stored = %d, want %d", got, maxMembers)
	}
}

// seedInvite stores a pending editor invite to the bdb bdbID, created by ownerID and valid for an hour.
func seedInvite(t *testing.T, bdbID, ownerID uuid.UUID) *domain.Invite {
	t.Helper()
	invite, err := domain.NewInvite(bdbID, ownerID, authz.BdbRoleEditor, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("new invite: %v", err)
	}
	if err := repos().invites.Create(t.Context(), invite); err != nil {
		t.Fatalf("creating invite: %v", err)
	}
	return invite
}

// acceptConcurrently makes n accepts through svc at once, the i-th one by the user and with the token accept(i)
// gives, and returns each one's error.
func acceptConcurrently(
	t *testing.T, svc interface {
		AcceptInvite(ctx context.Context, token string, userID uuid.UUID) (*domain.Bdb, error)
	}, accept func(i int) (string, uuid.UUID), n int,
) []error {
	t.Helper()
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		token, userID := accept(i)
		wg.Go(func() {
			<-start
			_, errs[i] = svc.AcceptInvite(t.Context(), token, userID)
		})
	}
	close(start)
	wg.Wait()
	return errs
}

// countAdmittedAndLimited counts the nil errors in errs and the domain.MemberLimitReachedError ones, reporting any
// other error and any limit error whose Max is not maxMembers.
func countAdmittedAndLimited(t *testing.T, errs []error, maxMembers int) (admitted, limited int) {
	t.Helper()
	for _, e := range errs {
		if e == nil {
			admitted++
			continue
		}
		limitErr, ok := errors.AsType[domain.MemberLimitReachedError](e)
		if !ok {
			t.Errorf("unexpected error: %v", e)
			continue
		}
		if limitErr.Max != maxMembers {
			t.Errorf("limit error carries Max=%d, want %d", limitErr.Max, maxMembers)
		}
		limited++
	}
	return admitted, limited
}

// countMembers returns how many members the bdb bdbID has stored.
func countMembers(t *testing.T, bdbID uuid.UUID) int {
	t.Helper()
	var n int
	err := testDB.QueryRow(t.Context(), "SELECT COUNT(*) FROM bdb_members WHERE bdb_id = $1", bdbID).Scan(&n)
	if err != nil {
		t.Fatalf("counting members: %v", err)
	}
	return n
}
