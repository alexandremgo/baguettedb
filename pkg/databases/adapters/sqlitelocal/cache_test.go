//go:build cgo

package sqlitelocal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// fixedDEKResolver returns the same key for every database and counts calls.
// Each test that needs a fresh resolver instantiates one — the call counter is
// the primary observability handle.
type fixedDEKResolver struct {
	key   []byte
	calls atomic.Int64
}

func newFixedDEKResolver(t *testing.T) *fixedDEKResolver {
	return &fixedDEKResolver{key: newKey(t)}
}

func (r *fixedDEKResolver) Resolve(_ context.Context, _ string) ([]byte, error) {
	r.calls.Add(1)
	return r.key, nil
}

func (r *fixedDEKResolver) Calls() int64 { return r.calls.Load() }

// provisioningResolver wraps resolve so that a database's file exists in dataDir by the time the cache opens it: the
// cache only opens existing files, and a test's databases have none until their first miss creates one, empty, with
// the DEK resolve returns.
func provisioningResolver(dataDir string, resolve DEKResolver) DEKResolver {
	return func(ctx context.Context, databaseID string) ([]byte, error) {
		dek, err := resolve(ctx, databaseID)
		if err != nil || len(dek) != KeySize {
			return dek, err
		}
		path := filepath.Join(dataDir, databaseID+".db")
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			return dek, err
		}
		conn, err := Open(path, dek, 0)
		if err != nil {
			return nil, err
		}
		return dek, conn.Close()
	}
}

// newTestCache builds a connectorCache with sane test defaults. The caller is
// responsible for Close (registered via t.Cleanup).
func newTestCache(t *testing.T, resolver DEKResolver, opts ...func(*cacheConfig)) *connectorCache {
	t.Helper()
	cfg := cacheConfig{
		dataDir:     t.TempDir(),
		ttl:         100 * time.Millisecond,
		maxEntries:  4,
		sweepEvery:  time.Hour, // tests drive sweepOnce manually
		dekResolver: resolver,
	}
	for _, o := range opts {
		o(&cfg)
	}
	cfg.dekResolver = provisioningResolver(cfg.dataDir, cfg.dekResolver)
	c, err := newConnectorCache(cfg)
	if err != nil {
		t.Fatalf("newConnectorCache: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("cache Close: %v", err)
		}
	})
	return c
}

// useExec runs a trivial Exec inside Use to confirm the connection works.
func useExec(t *testing.T, c *connectorCache, ns string) {
	t.Helper()
	err := c.Use(context.Background(), ns, func(conn *Conn) error {
		_, err := conn.Exec(context.Background(), "CREATE TABLE IF NOT EXISTS t (x INTEGER)")
		return err
	})
	if err != nil {
		t.Fatalf("Use(%s): %v", ns, err)
	}
}

func TestCache_HitOpensOnce(t *testing.T) {
	r := newFixedDEKResolver(t)
	c := newTestCache(t, r.Resolve)

	useExec(t, c, "ns1")
	useExec(t, c, "ns1")
	useExec(t, c, "ns1")

	if got := r.Calls(); got != 1 {
		t.Errorf("dekResolver calls = %d, want 1", got)
	}
}

func TestCache_TTLEviction(t *testing.T) {
	r := newFixedDEKResolver(t)
	c := newTestCache(t, r.Resolve)

	useExec(t, c, "ns1")
	if got := r.Calls(); got != 1 {
		t.Fatalf("after first Use, calls = %d, want 1", got)
	}

	// Mark the entry as idle in the past, then drive a sweep manually.
	c.mu.Lock()
	c.entries["ns1"].lastUsed = time.Now().Add(-time.Hour)
	c.mu.Unlock()
	c.sweepOnce(time.Now())

	c.mu.Lock()
	_, stillCached := c.entries["ns1"]
	c.mu.Unlock()
	if stillCached {
		t.Fatal("entry should have been evicted by sweeper")
	}

	useExec(t, c, "ns1")
	if got := r.Calls(); got != 2 {
		t.Errorf("after re-open, calls = %d, want 2", got)
	}
}

func TestCache_LRUEvictionOnInsertOverCap(t *testing.T) {
	r := newFixedDEKResolver(t)
	c := newTestCache(t, r.Resolve, func(cfg *cacheConfig) { cfg.maxEntries = 2 })

	useExec(t, c, "ns1") // [ns1]
	useExec(t, c, "ns2") // [ns2, ns1]
	useExec(t, c, "ns1") // bump ns1 -> [ns1, ns2]
	useExec(t, c, "ns3") // exceed cap; evict ns2 (LRU) -> [ns3, ns1]

	c.mu.Lock()
	_, hasNS1 := c.entries["ns1"]
	_, hasNS2 := c.entries["ns2"]
	_, hasNS3 := c.entries["ns3"]
	c.mu.Unlock()

	if !hasNS1 {
		t.Error("ns1 should still be cached (recently used)")
	}
	if hasNS2 {
		t.Error("ns2 should have been evicted (LRU)")
	}
	if !hasNS3 {
		t.Error("ns3 should be cached (just inserted)")
	}

	useExec(t, c, "ns2")
	if got := r.Calls(); got != 4 {
		t.Errorf("after re-opening evicted ns2, calls = %d, want 4", got)
	}
}

// TestCache_ConcurrentMissDedup verifies that 50 concurrent Use calls for the
// same cold database trigger exactly one dekResolver invocation.
func TestCache_ConcurrentMissDedup(t *testing.T) {
	r := newFixedDEKResolver(t)
	c := newTestCache(t, r.Resolve)

	const N = 50
	var wg sync.WaitGroup
	start := make(chan struct{})

	for range N {
		wg.Go(func() {
			<-start
			err := c.Use(context.Background(), "ns1", func(conn *Conn) error {
				_, err := conn.Exec(context.Background(), "SELECT 1")
				return err
			})
			if err != nil {
				t.Errorf("concurrent Use: %v", err)
			}
		})
	}
	close(start)
	wg.Wait()

	if got := r.Calls(); got != 1 {
		t.Errorf("dekResolver calls = %d, want 1 (singleflight)", got)
	}
}

// TestCache_EvictWaitsForInFlightUse verifies the sweeper cannot close a
// connection while a Use callback is still running.
func TestCache_EvictWaitsForInFlightUse(t *testing.T) {
	r := newFixedDEKResolver(t)
	c := newTestCache(t, r.Resolve)

	// Prime cache.
	useExec(t, c, "ns1")

	// Start a slow Use; it will hold entryMu.RLock while sleeping.
	useStarted := make(chan struct{})
	useDone := make(chan struct{})
	releaseUse := make(chan struct{})

	go func() {
		err := c.Use(context.Background(), "ns1", func(conn *Conn) error {
			close(useStarted)
			<-releaseUse
			_, err := conn.Exec(context.Background(), "CREATE TABLE IF NOT EXISTS late (x INTEGER)")
			return err
		})
		if err != nil {
			t.Errorf("slow Use: %v", err)
		}
		close(useDone)
	}()
	<-useStarted

	// Force the entry past TTL and trigger eviction in a goroutine; it must
	// block until the slow Use releases.
	c.mu.Lock()
	c.entries["ns1"].lastUsed = time.Now().Add(-time.Hour)
	c.mu.Unlock()

	sweepDone := make(chan struct{})
	go func() {
		c.sweepOnce(time.Now())
		close(sweepDone)
	}()

	select {
	case <-sweepDone:
		t.Fatal("sweep returned before in-flight Use released its RLock")
	case <-time.After(50 * time.Millisecond):
		// Expected: sweep is blocked on entryMu.Lock.
	}

	close(releaseUse)
	<-useDone

	select {
	case <-sweepDone:
	case <-time.After(time.Second):
		t.Fatal("sweep did not complete after Use released")
	}
}

func TestCache_EvictRemovesAndCloses(t *testing.T) {
	r := newFixedDEKResolver(t)
	c := newTestCache(t, r.Resolve)

	useExec(t, c, "ns1")

	if err := c.Evict("ns1"); err != nil {
		t.Fatalf("Evict: %v", err)
	}

	c.mu.Lock()
	_, present := c.entries["ns1"]
	c.mu.Unlock()
	if present {
		t.Fatal("entry should be gone after Evict")
	}

	// Evicting an unknown database is a no-op.
	if err := c.Evict("does-not-exist"); err != nil {
		t.Errorf("Evict missing: %v", err)
	}

	useExec(t, c, "ns1")
	if got := r.Calls(); got != 2 {
		t.Errorf("after re-opening evicted ns1, calls = %d, want 2", got)
	}
}

func TestCache_InsertSeedsHit(t *testing.T) {
	r := newFixedDEKResolver(t)
	c := newTestCache(t, r.Resolve)

	conn, err := Open(t.TempDir()+"/seed.db", r.key, 0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := c.Insert("ns1", conn); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	useExec(t, c, "ns1")
	if got := r.Calls(); got != 0 {
		t.Errorf("dekResolver calls = %d, want 0 (entry was seeded)", got)
	}
}

func TestCache_InsertDuplicateClosesArg(t *testing.T) {
	r := newFixedDEKResolver(t)
	c := newTestCache(t, r.Resolve)

	useExec(t, c, "ns1")

	dup, err := Open(t.TempDir()+"/dup.db", r.key, 0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	err = c.Insert("ns1", dup)
	if err == nil {
		t.Fatal("expected Insert to fail on duplicate database")
	}

	// dup must have been closed by Insert; calling Close again is a no-op.
	if cerr := dup.Close(); cerr != nil {
		t.Errorf("dup.Close after Insert reject: %v", cerr)
	}
}

func TestCache_LoadErrorRemovesEntry(t *testing.T) {
	failErr := errors.New("resolver kaput")
	resolver := func(_ context.Context, _ string) ([]byte, error) {
		return nil, failErr
	}
	c := newTestCache(t, resolver)

	err := c.Use(context.Background(), "ns1", func(*Conn) error { return nil })
	if !errors.Is(err, failErr) {
		t.Fatalf("Use err = %v, want resolver error", err)
	}

	c.mu.Lock()
	_, present := c.entries["ns1"]
	c.mu.Unlock()
	if present {
		t.Fatal("failed-load entry should be removed so the next call retries")
	}
}

func TestCache_LoadShortDEKErrors(t *testing.T) {
	resolver := func(_ context.Context, _ string) ([]byte, error) {
		return []byte("too short"), nil
	}
	c := newTestCache(t, resolver)

	err := c.Use(context.Background(), "ns1", func(*Conn) error { return nil })
	if err == nil {
		t.Fatal("expected error for short DEK")
	}
}

func TestCache_CloseAfterCloseIsNoop(t *testing.T) {
	r := newFixedDEKResolver(t)
	c := newTestCache(t, r.Resolve)

	useExec(t, c, "ns1")
	useExec(t, c, "ns2")

	// First Close runs via t.Cleanup; we trigger an extra one here and a
	// post-Close Use to ensure both are graceful.
	if err := c.Close(); err != nil {
		t.Fatalf("first manual Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	err := c.Use(context.Background(), "ns3", func(*Conn) error { return nil })
	if !errors.Is(err, errCacheClosed) {
		t.Errorf("Use after Close = %v, want errCacheClosed", err)
	}
}

// TestCache_InsertDuplicateLogsWarn verifies the duplicate-insert path emits
// a WARN line carrying the database ID, so operators can spot the bug.
func TestCache_InsertDuplicateLogsWarn(t *testing.T) {
	r := newFixedDEKResolver(t)
	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	c := newTestCache(t, r.Resolve, func(cfg *cacheConfig) { cfg.logger = logger })

	useExec(t, c, "ns1")

	dup, err := Open(t.TempDir()+"/dup.db", r.key, 0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := c.Insert("ns1", dup); err == nil {
		t.Fatal("expected Insert to fail on duplicate")
	}

	out := buf.String()
	if !strings.Contains(out, `"level":"warn"`) {
		t.Errorf("expected a WARN line, got: %s", out)
	}
	if !strings.Contains(out, `"database_id":"ns1"`) {
		t.Errorf("expected database_id=ns1 in log, got: %s", out)
	}
	if !strings.Contains(out, "already cached") {
		t.Errorf("expected duplicate-cache message, got: %s", out)
	}
}

// TestCache_ConcurrentDifferentDatabases stresses the cache with many
// goroutines hitting distinct databases; each should open exactly once.
func TestCache_ConcurrentDifferentDatabases(t *testing.T) {
	r := newFixedDEKResolver(t)
	c := newTestCache(t, r.Resolve, func(cfg *cacheConfig) { cfg.maxEntries = 100 })

	const NS = 20
	const PerNS = 10

	var wg sync.WaitGroup
	for i := range NS {
		ns := fmt.Sprintf("ns%d", i)
		for range PerNS {
			wg.Go(func() {
				err := c.Use(context.Background(), ns, func(conn *Conn) error {
					_, err := conn.Exec(context.Background(), "SELECT 1")
					return err
				})
				if err != nil {
					t.Errorf("Use(%s): %v", ns, err)
				}
			})
		}
	}
	wg.Wait()

	if got := r.Calls(); got != int64(NS) {
		t.Errorf("dekResolver calls = %d, want %d", got, NS)
	}
}

// TestCache_UseHonorsDeadlineWhileConnBusy: a caller queued behind another goroutine's in-flight fn must give up when
// its ctx ends instead of waiting for the connection, and the slot must be free again once the holder returns.
func TestCache_UseHonorsDeadlineWhileConnBusy(t *testing.T) {
	r := newFixedDEKResolver(t)
	c := newTestCache(t, r.Resolve)
	useExec(t, c, "busy")

	holding := make(chan struct{})
	holdRelease := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		_ = c.Use(context.Background(), "busy", func(*Conn) error {
			// Signals that the slot is taken, then holds it until released, or 300ms so a waiter that ignores its
			// deadline still gets the slot and the test fails instead of hanging.
			close(holding)
			select {
			case <-holdRelease:
			case <-time.After(300 * time.Millisecond):
			}
			return nil
		})
	})
	<-holding

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	ran := false
	start := time.Now()
	err := c.Use(ctx, "busy", func(*Conn) error {
		ran = true
		return nil
	})
	elapsed := time.Since(start)
	close(holdRelease)
	wg.Wait()

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Use while the connection is busy: err = %v, want context.DeadlineExceeded", err)
	}
	if ran {
		t.Fatal("fn ran although the ctx had already ended")
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("Use waited %v for the connection instead of honoring its 50ms deadline", elapsed)
	}

	// A failed acquire must not cost the slot: a later caller gets the connection promptly rather than blocking on a
	// token the waiter left behind.
	afterCtx, afterCancel := context.WithTimeout(context.Background(), time.Second)
	defer afterCancel()
	if err := c.Use(afterCtx, "busy", func(*Conn) error { return nil }); err != nil {
		t.Fatalf("Use after the waiter gave up: %v, want the slot to be free again", err)
	}
}
