package service

import (
	"cmp"
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// slotHeldKey marks a ctx whose caller already holds one of the user's in-flight slots, so a nested execute call runs
// under that slot instead of taking a second one.
type slotHeldKey struct{}

// dataAPIKeyRunKey marks a ctx whose request runs under a Data API key, holding the key's ID.
type dataAPIKeyRunKey struct{}

// withDataAPIKeyRun returns ctx marking its request as run under the Data API key keyID, whose in-flight slots are its
// own rather than its creator's.
func withDataAPIKeyRun(ctx context.Context, keyID uuid.UUID) context.Context {
	return context.WithValue(ctx, dataAPIKeyRunKey{}, keyID)
}

// acquireSlot takes one of the in-flight slots of userID, or of the Data API key ctx's request runs under, for the rest
// of the request, and returns the ctx to run it under plus the release; a ctx that already carries a slot is returned
// as-is with a no-op release. At the cap, the same for a key as for its creator, it returns a QueryRateLimitedError
// with nothing to release.
func (s *QueryService) acquireSlot(ctx context.Context, userID uuid.UUID) (context.Context, func(), error) {
	if held, _ := ctx.Value(slotHeldKey{}).(bool); held {
		return ctx, func() {}, nil
	}
	// A run under a Data API key takes the key's slots, so an app can't use up its owner's; keyID is uuid.Nil, the zero
	// value cmp.Or skips, for any other request.
	keyID, _ := ctx.Value(dataAPIKeyRunKey{}).(uuid.UUID)
	release, ok := s.gate.acquire(cmp.Or(keyID, userID), s.Limits(ctx).MaxConcurrentQueries)
	if !ok {
		return ctx, nil, &domain.QueryRateLimitedError{RetryAfter: time.Second}
	}
	return context.WithValue(ctx, slotHeldKey{}, true), release, nil
}

// inFlightGate counts the SQL requests each user, or Data API key, has in flight in this process and refuses new ones
// past a limit for each, so one caller cannot queue unbounded work on the per-database connection slots.
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
