//go:build integration

package integrationtest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/databases/dataapi"
)

// countingKeyChecker stands for auth's Data API key check: it accepts any key as one of the bdb bdbID's, and counts
// the checks it answers.
type countingKeyChecker struct {
	bdbID uuid.UUID
	calls atomic.Int32
}

// check answers every key as one of the checker's bdb, counting the call.
func (c *countingKeyChecker) check(_ context.Context, _ string) (authhttp.DataAPIKeyIdentity, error) {
	c.calls.Add(1)
	return authhttp.DataAPIKeyIdentity{
		UserID: uuid.New(), Roles: []authz.Role{authz.RoleUser}, KeyID: uuid.New(), BdbID: c.bdbID,
	}, nil
}

// TestShardRouting_DataAPIAnsweredByTheOwner checks that a Data API request reaching a pod that doesn't own its bdb is
// forwarded before anything else, so the owner alone answers it: the answer allows any origin once, not once from each
// pod, which a browser would refuse, and the key is checked once, by the owner's checker.
func TestShardRouting_DataAPIAnsweredByTheOwner(t *testing.T) {
	truncateAll(t)
	kek := newTestKEKProvider(t)
	bdbID := uuid.New()
	owner, receiver := &countingKeyChecker{bdbID: bdbID}, &countingKeyChecker{bdbID: bdbID}

	// pod1 owns shard 1, where the bdb lives; pod0 forwards shard-1 traffic to it.
	pod1 := newShardPod(t, 0, 1, kek, func(int16, int16) string { return "http://unused" })
	dataapi.Register(pod1.mux, pod1.formulaSvc, owner.check, pod1.shardRouter.Middleware, 1024*1024, testLogger)
	pod1Server := httptest.NewServer(pod1.router)
	t.Cleanup(pod1Server.Close)
	pod0 := newShardPod(t, 0, 0, kek, func(_, ordinal int16) string {
		if ordinal == 1 {
			return pod1Server.URL
		}
		return "http://unused"
	})
	dataapi.Register(pod0.mux, pod0.formulaSvc, receiver.check, pod0.shardRouter.Middleware, 1024*1024, testLogger)
	if _, err := pod1.dbSvc.CreateDatabase(context.Background(), bdbID); err != nil {
		t.Fatalf("creating the database on pod1: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/data/v1/bdbs/"+bdbID.String()+"/formulas", nil)
	req.Header.Set("Authorization", "Bearer bdbdk_test")
	req.Header.Set("Origin", "http://localhost:5180")
	rec := httptest.NewRecorder()
	pod0.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("the forwarded request = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if origins := rec.Header().Values("Access-Control-Allow-Origin"); len(origins) != 1 || origins[0] != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want one *", origins)
	}
	if owner.calls.Load() != 1 || receiver.calls.Load() != 0 {
		t.Errorf("key checks: owner %d, receiving pod %d; want 1 and 0", owner.calls.Load(), receiver.calls.Load())
	}
}

// TestShardRouting_DataAPIAnswersForAnUnreachableOwner checks a Data API request reaching a pod that doesn't own its
// bdb while the owner can't be reached: the receiving pod answers a 503 an app can act on, with the unavailable code,
// a Retry-After header and any origin allowed once so a page can read it, without checking the key; and a preflight
// is still answered with a 204 allowing any origin.
func TestShardRouting_DataAPIAnswersForAnUnreachableOwner(t *testing.T) {
	truncateAll(t)
	kek := newTestKEKProvider(t)
	bdbID := uuid.New()
	receiver := &countingKeyChecker{bdbID: bdbID}

	// pod1 owns shard 1, where the bdb lives, and is down: pod0 forwards shard-1 traffic to an address nothing
	// listens on.
	pod1 := newShardPod(t, 0, 1, kek, func(int16, int16) string { return "http://unused" })
	if _, err := pod1.dbSvc.CreateDatabase(context.Background(), bdbID); err != nil {
		t.Fatalf("creating the database on pod1: %v", err)
	}
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close()
	pod0 := newShardPod(t, 0, 0, kek, func(int16, int16) string { return downURL })
	dataapi.Register(pod0.mux, pod0.formulaSvc, receiver.check,
		pod0.shardRouter.MiddlewareAnswering(dataapi.OwnerUnreachable), 1024*1024, testLogger)
	path := "/api/data/v1/bdbs/" + bdbID.String() + "/formulas"

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer bdbdk_test")
	req.Header.Set("Origin", "http://localhost:5180")
	rec := httptest.NewRecorder()
	pod0.router.ServeHTTP(rec, req)

	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding the answer %d %q: %v", rec.Code, rec.Body.String(), err)
	}
	if rec.Code != http.StatusServiceUnavailable || body.Code != "unavailable" || body.Error == "" {
		t.Errorf("the request = %d %+v, want 503 with the unavailable code and a message", rec.Code, body)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Error("the answer has no Retry-After header")
	}
	if origins := rec.Header().Values("Access-Control-Allow-Origin"); len(origins) != 1 || origins[0] != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want one *", origins)
	}
	if receiver.calls.Load() != 0 {
		t.Errorf("the receiving pod checked the key %d times, want 0", receiver.calls.Load())
	}

	preflight := httptest.NewRequest(http.MethodOptions, path, nil)
	preflight.Header.Set("Origin", "http://localhost:5180")
	preflight.Header.Set("Access-Control-Request-Method", "GET")
	preflight.Header.Set("Access-Control-Request-Headers", "authorization")
	rec = httptest.NewRecorder()
	pod0.router.ServeHTTP(rec, preflight)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("the preflight = %d %v, want 204 allowing any origin", rec.Code, rec.Header())
	}
}
