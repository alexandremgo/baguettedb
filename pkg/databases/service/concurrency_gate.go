package service

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// slotHeldKey marks a ctx whose caller already holds one of the user's in-flight slots, so a nested execute call runs
// under that slot instead of taking a second one.
type slotHeldKey struct{}

// acquireSlot takes one of userID's in-flight slots for the rest of the request and returns the ctx to run it under
// plus the release; a ctx that already carries a slot is returned as-is with a no-op release. At the user's cap it
// returns a QueryRateLimitedError with nothing to release.
func (s *QueryService) acquireSlot(ctx context.Context, userID uuid.UUID) (context.Context, func(), error) {
	if held, _ := ctx.Value(slotHeldKey{}).(bool); held {
		return ctx, func() {}, nil
	}
	release, ok := s.gate.acquire(userID, s.Limits(ctx).MaxConcurrentQueries)
	if !ok {
		return ctx, nil, &domain.QueryRateLimitedError{RetryAfter: time.Second}
	}
	return context.WithValue(ctx, slotHeldKey{}, true), release, nil
}

// inFlightGate counts the SQL requests each user has in flight in this process and refuses new ones past a per-user
// limit, so one caller cannot queue unbounded work on the per-database connection slots.
type inFlightGate struct {
	mu     sync.Mutex
	counts map[uuid.UUID]int
}

func newInFlightGate() *inFlightGate {
	return &inFlightGate{counts: make(map[uuid.UUID]int)}
}

// acquire takes a slot for userID when fewer than limit are in flight and returns the release that frees it; ok is
// false when the user is already at the limit, with nothing to release. A non-positive limit always admits.
func (g *inFlightGate) acquire(userID uuid.UUID, limit int) (release func(), ok bool) {
	if limit <= 0 {
		return func() {}, true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.counts[userID] >= limit {
		return nil, false
	}
	g.counts[userID]++
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.counts[userID] <= 1 {
			// Drops the entry at zero so the map only ever holds users with work in flight.
			delete(g.counts, userID)
			return
		}
		g.counts[userID]--
	}, true
}
