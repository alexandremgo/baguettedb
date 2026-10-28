package service

import (
	"testing"

	"github.com/google/uuid"
)

// TestInFlightGate covers the per-user slot accounting: a user is refused past the limit, users do not share
// slots, a release frees one, the map holds no idle users, and a non-positive limit admits everyone.
func TestInFlightGate(t *testing.T) {
	g := newInFlightGate()
	alice, bob := uuid.New(), uuid.New()

	release1, ok := g.acquire(alice, 2)
	if !ok {
		t.Fatal("first acquire refused")
	}
	release2, ok := g.acquire(alice, 2)
	if !ok {
		t.Fatal("second acquire refused under a limit of 2")
	}
	if _, ok := g.acquire(alice, 2); ok {
		t.Fatal("third acquire admitted past a limit of 2")
	}
	releaseBob, ok := g.acquire(bob, 2)
	if !ok {
		t.Fatal("another user's acquire refused while alice is at her limit")
	}

	release1()
	release3, ok := g.acquire(alice, 2)
	if !ok {
		t.Fatal("acquire refused after a release freed a slot")
	}
	release2()
	release3()
	releaseBob()
	g.mu.Lock()
	idle := len(g.counts)
	g.mu.Unlock()
	if idle != 0 {
		t.Errorf("gate keeps %d users with nothing in flight, want 0", idle)
	}

	release, ok := g.acquire(alice, 0)
	if !ok {
		t.Fatal("a non-positive limit must admit")
	}
	release()
}
