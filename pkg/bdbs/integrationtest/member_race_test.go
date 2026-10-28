//go:build integration

package integrationtest

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
)

// TestMemberChanges_ConcurrentChangesAreAllKept checks that concurrent member changes on one bdb never undo each
// other: many simultaneous AddMember calls for distinct users and a RemoveMember of an existing member all take
// effect, so the bdb ends with the owner and every added user, and without the removed one. A change saved by making
// storage equal to a snapshot read before the transaction drops the members others added in between.
func TestMemberChanges_ConcurrentChangesAreAllKept(t *testing.T) {
	truncateAll(t)
	ctx := t.Context()
	svc := newBdbService(&testAuthApp{}, quota.Fixed(quota.Limits{MaxBdbs: 10, MaxMembersPerBdb: 100}))
	roles := []authz.Role{authz.RoleUser}

	ownerID := uuid.MustParse(seedUser(t, "owner@test.com"))
	bdb, err := svc.CreateBdb(ctx, ownerID, roles, "Busy Bdb", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}
	leaver := uuid.MustParse(seedUser(t, "leaver@test.com"))
	if _, err := svc.AddMember(ctx, ownerID, bdb.ID, leaver, authz.BdbRoleViewer, roles); err != nil {
		t.Fatalf("adding the member to remove: %v", err)
	}

	const n = 16
	joiners := make([]uuid.UUID, n)
	for i := range joiners {
		joiners[i] = uuid.MustParse(seedUser(t, fmt.Sprintf("joiner-%d@test.com", i)))
	}

	errs := make([]error, n+1)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start
			_, errs[i] = svc.AddMember(ctx, ownerID, bdb.ID, joiners[i], authz.BdbRoleViewer, roles)
		})
	}
	wg.Go(func() {
		<-start
		_, errs[n] = svc.RemoveMember(ctx, ownerID, bdb.ID, leaver)
	})
	close(start)
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Errorf("change %d: %v", i, e)
		}
	}
	got, err := svc.GetBdb(ctx, ownerID, bdb.ID)
	if err != nil {
		t.Fatalf("reading the bdb back: %v", err)
	}
	if len(got.Members) != 1+n {
		t.Errorf("members = %d, want %d (the owner and every added user)", len(got.Members), 1+n)
	}
	for _, id := range joiners {
		if !got.IsMember(id) {
			t.Errorf("added user %s is missing", id)
		}
	}
	if got.IsMember(leaver) {
		t.Error("removed member is still there")
	}
}

// TestAddMember_ConcurrentAtMemberCapAdmitsUpToCap checks that the member cap holds when owners add members
// directly and concurrently: with a cap of 3 (the owner and 2 seats), many simultaneous AddMember calls admit exactly
// two users and refuse the rest with domain.MemberLimitReachedError. A cap checked against a snapshot read before the
// transaction lets several calls see the same free seat.
func TestAddMember_ConcurrentAtMemberCapAdmitsUpToCap(t *testing.T) {
	truncateAll(t)
	ctx := t.Context()
	const maxMembers = 3
	svc := newBdbService(&testAuthApp{}, quota.Fixed(quota.Limits{MaxBdbs: 10, MaxMembersPerBdb: maxMembers}))
	roles := []authz.Role{authz.RoleUser}

	ownerID := uuid.MustParse(seedUser(t, "owner@test.com"))
	bdb, err := svc.CreateBdb(ctx, ownerID, roles, "Small Bdb", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}

	const n = 8
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		joiner := uuid.MustParse(seedUser(t, fmt.Sprintf("joiner-%d@test.com", i)))
		wg.Go(func() {
			<-start
			_, errs[i] = svc.AddMember(ctx, ownerID, bdb.ID, joiner, authz.BdbRoleViewer, roles)
		})
	}
	close(start)
	wg.Wait()

	var admitted, limited int
	for _, e := range errs {
		_, atCap := errors.AsType[domain.MemberLimitReachedError](e)
		switch {
		case e == nil:
			admitted++
		case atCap:
			limited++
		default:
			t.Errorf("unexpected error: %v", e)
		}
	}
	if admitted != maxMembers-1 || limited != n-(maxMembers-1) {
		t.Errorf("admitted %d and limited %d, want %d and %d", admitted, limited, maxMembers-1, n-(maxMembers-1))
	}
	var members int
	err = testDB.QueryRow(ctx, "SELECT COUNT(*) FROM bdb_members WHERE bdb_id = $1", bdb.ID).Scan(&members)
	if err != nil {
		t.Fatalf("counting members: %v", err)
	}
	if members != maxMembers {
		t.Errorf("members stored = %d, want %d", members, maxMembers)
	}
}
