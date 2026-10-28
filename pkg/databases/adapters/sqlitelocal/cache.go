//go:build cgo

package sqlitelocal

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// DEKResolver fetches the 32-byte raw data-encryption key for the given
// database. The connector cache invokes it on a miss to open the SQLCipher
// file. Implementations typically read the wrapped DEK from a metadata store
// and unwrap via a KEKProvider.
type DEKResolver func(ctx context.Context, databaseID string) ([]byte, error)

// errCacheClosed is returned by Use after the cache has been Closed.
var errCacheClosed = errors.New("sqlitelocal: connector cache is closed")

// cacheConfig captures the tunables for connectorCache. Kept as a struct so
// the constructor signature stays small as we add knobs.
type cacheConfig struct {
	// dataDir holds the per-database SQLCipher files (`<dataDir>/<databaseID>.db`).
	dataDir string

	// ttl is the idle duration after which a cached connection is closed by
	// the sweeper. lastUsed is bumped on every Use hit.
	ttl time.Duration

	// maxEntries is the hard cap on cached open connections. Above this
	// count, the LRU entry is evicted on insertion.
	maxEntries int

	// sweepEvery controls how often the background sweeper wakes up to
	// close idle entries. Typically a fraction of ttl.
	sweepEvery time.Duration

	// dekResolver is called on cache miss to obtain the raw DEK for a
	// database. Required.
	dekResolver DEKResolver

	// busyTimeout is how long a statement on a connection waits for a lock another connection holds, from its open on.
	busyTimeout time.Duration

	// Zero value is a no-op writer — safe to leave unset in tests.
	logger zerolog.Logger
}

// cacheEntry tracks one cached open connection.
//
// Lifecycle: created in Loading state with ready open. The inserter calls
// load, which fills conn (or loadErr) and closes ready. Concurrent Use calls
// for the same database block on ready and then proceed.
//
// Eviction (sweeper, LRU, explicit Evict, Close) removes the entry from the
// parent cache's map+LRU under cache.mu, then takes entryMu (write) to close
// the connection. Use holds entryMu (read) for the duration of fn, blocking
// eviction until fn returns.
type cacheEntry struct {
	// Immutable after construction.
	databaseID string
	ready      chan struct{}

	// Set by load before close(ready); read-only afterwards.
	conn    *Conn
	loadErr error

	// Guarded by parent cache.mu.
	lastUsed time.Time
	lruElem  *list.Element

	// entryMu coordinates eviction vs in-flight queries:
	//   - RLock: held by Use for the duration of fn
	//   - Lock:  held by an evictor before calling conn.Close
	// closed is set to true under Lock once Close has run, so racing
	// evictors can skip a double-close (though the public surface doesn't
	// expose that race today).
	entryMu sync.RWMutex
	closed  bool

	// connSem serialises fn invocations on conn so callers see correct per-connection transaction semantics:
	// SQLite is internally thread-safe (SQLITE_THREADSAFE=1), but BEGIN/COMMIT spans multiple statements, and
	// without app-level serialisation a SELECT could observe another goroutine's uncommitted writes on the same
	// connection. A one-slot channel rather than a mutex, so a waiter can give up when its ctx ends.
	connSem chan struct{}
}

// newCacheEntry builds an entry whose connection slot is free. ready is closed once conn is usable; pass an already
// closed channel together with conn for a pre-opened connection.
func newCacheEntry(databaseID string, ready chan struct{}, conn *Conn) *cacheEntry {
	return &cacheEntry{
		databaseID: databaseID,
		ready:      ready,
		conn:       conn,
		lastUsed:   time.Now(),
		connSem:    make(chan struct{}, 1),
	}
}

// acquireConn takes the entry's connection slot, waiting until it is free or ctx ends. On success the returned release
// must be called once the caller is done with the connection; on failure it returns ctx's error and nothing to release.
func (e *cacheEntry) acquireConn(ctx context.Context) (release func(), err error) {
	select {
	case e.connSem <- struct{}{}:
		if err = ctx.Err(); err != nil {
			<-e.connSem
			return nil, err
		}
		return func() {
			<-e.connSem
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// connectorCache is a TTL+LRU cache of open SQLCipher connections keyed by
// database. The zero value is not usable; construct via newConnectorCache.
//
// Concurrency: cache.mu protects entries+lru+closed. entry.entryMu protects
// each entry's connection lifetime. Lock order is always cache.mu → entryMu;
// evictors release cache.mu before taking entryMu to avoid stalling unrelated
// lookups on a slow Close.
type connectorCache struct {
	cfg cacheConfig

	mu      sync.Mutex
	entries map[string]*cacheEntry
	lru     *list.List // front = MRU; values are *cacheEntry
	closed  bool

	// Sweeper lifecycle. bgCancel stops the sweep loop on Close; wg blocks Close until the sweeper goroutine exits.
	bgCancel context.CancelFunc
	wg       sync.WaitGroup
}

// newConnectorCache constructs a cache and starts its sweeper goroutine.
// Callers must invoke Close to stop the sweeper and release connections.
func newConnectorCache(cfg cacheConfig) (*connectorCache, error) {
	if cfg.dataDir == "" {
		return nil, errors.New("sqlitelocal: cacheConfig.dataDir is required")
	}
	if cfg.dekResolver == nil {
		return nil, errors.New("sqlitelocal: cacheConfig.dekResolver is required")
	}
	if cfg.ttl <= 0 {
		return nil, errors.New("sqlitelocal: cacheConfig.ttl must be positive")
	}
	if cfg.maxEntries <= 0 {
		return nil, errors.New("sqlitelocal: cacheConfig.maxEntries must be positive")
	}
	if cfg.sweepEvery <= 0 {
		return nil, errors.New("sqlitelocal: cacheConfig.sweepEvery must be positive")
	}

	bgCtx, bgCancel := context.WithCancel(context.Background())
	c := &connectorCache{
		cfg:      cfg,
		entries:  make(map[string]*cacheEntry),
		lru:      list.New(),
		bgCancel: bgCancel,
	}

	c.wg.Go(func() { c.runSweeper(bgCtx) })

	return c, nil
}

// Use looks up (or opens) the connection for database and invokes fn. The
// connection is held under entryMu.RLock for the duration of fn — the sweeper
// cannot evict it mid-call. fn also runs under entry.connMu, so concurrent
// Use calls for the same database serialise on the connection (required for
// transaction correctness — see cacheEntry.connMu).
//
// Updates lastUsed + LRU position on a hit. On a miss, the calling goroutine
// performs the open; concurrent Use calls for the same database block on
// the entry's ready channel and reuse the resulting connection.
func (c *connectorCache) Use(ctx context.Context, databaseID string, fn func(*Conn) error) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errCacheClosed
	}

	if entry, ok := c.entries[databaseID]; ok {
		entry.lastUsed = time.Now()
		c.lru.MoveToFront(entry.lruElem)
		entry.entryMu.RLock()
		c.mu.Unlock()
		defer entry.entryMu.RUnlock()
		return waitAndCall(ctx, entry, fn)
	}

	// Miss: insert placeholder under cache.mu so concurrent Use calls for
	// the same database observe and wait on it. RLock the entry before
	// dropping cache.mu — that way the LRU/sweeper observers (which need
	// entryMu.Lock) cannot race ahead and close it before load runs.
	entry := newCacheEntry(databaseID, make(chan struct{}), nil)
	entry.lruElem = c.lru.PushFront(entry)
	c.entries[databaseID] = entry
	entry.entryMu.RLock()
	victim := c.evictOverCapLocked(entry)
	c.mu.Unlock()

	if victim != nil {
		c.cfg.logger.Debug().
			Str("database_id", victim.databaseID).
			Int("max_entries", c.cfg.maxEntries).
			Msg("sqlitelocal: LRU evicted entry over cap")
		if _, err := closeEntry(victim); err != nil {
			c.cfg.logger.Warn().
				Err(err).
				Str("database_id", victim.databaseID).
				Msg("sqlitelocal: closing LRU-evicted connection")
		}
	}

	c.load(ctx, entry)

	if entry.loadErr != nil {
		// Remove the failed entry so the next caller retries the open.
		c.mu.Lock()
		if c.entries[databaseID] == entry {
			delete(c.entries, databaseID)
			c.lru.Remove(entry.lruElem)
		}
		c.mu.Unlock()
		entry.entryMu.RUnlock()
		return entry.loadErr
	}

	defer entry.entryMu.RUnlock()
	release, err := entry.acquireConn(ctx)
	if err != nil {
		return err
	}
	defer release()
	return fn(entry.conn)
}

// Insert seeds the cache with an already-open connection. Used by
// CreateDatabase so the first query after creation skips the dekResolver
// round trip. If an entry already exists for the database, Insert closes
// conn and returns an error — callers should treat this as a logic bug.
func (c *connectorCache) Insert(databaseID string, conn *Conn) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = conn.Close()
		return errCacheClosed
	}
	if existing, ok := c.entries[databaseID]; ok {
		c.mu.Unlock()
		_ = conn.Close()
		c.cfg.logger.Warn().
			Str("database_id", databaseID).
			Msg("sqlitelocal: Insert rejected — database already cached")
		return fmt.Errorf("sqlitelocal: database %q already cached (existing=%p)", databaseID, existing)
	}

	entry := newCacheEntry(databaseID, closedChan(), conn)
	entry.lruElem = c.lru.PushFront(entry)
	c.entries[databaseID] = entry
	victim := c.evictOverCapLocked(entry)
	c.mu.Unlock()

	if victim != nil {
		c.cfg.logger.Debug().
			Str("database_id", victim.databaseID).
			Int("max_entries", c.cfg.maxEntries).
			Msg("sqlitelocal: LRU evicted entry over cap")
		if _, err := closeEntry(victim); err != nil {
			c.cfg.logger.Warn().
				Err(err).
				Str("database_id", victim.databaseID).
				Msg("sqlitelocal: closing LRU-evicted connection")
		}
	}
	return nil
}

// Evict removes database's connection from the cache and closes it. Returns
// nil if there was no entry. Used by DeleteDatabase before unlinking the
// file. Blocks until any in-flight Use for the database completes.
func (c *connectorCache) Evict(databaseID string) error {
	c.mu.Lock()
	entry, ok := c.entries[databaseID]
	if !ok {
		c.mu.Unlock()
		return nil
	}
	delete(c.entries, databaseID)
	c.lru.Remove(entry.lruElem)
	c.mu.Unlock()

	_, err := closeEntry(entry)
	if err != nil {
		c.cfg.logger.Warn().
			Err(err).
			Str("database_id", databaseID).
			Msg("sqlitelocal: closing evicted connection")
	}
	return err
}

// Close stops the sweeper, waits for any in-flight async closures, and
// closes all remaining cached connections. Safe to call multiple times.
func (c *connectorCache) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.bgCancel()

	// Drain entries under the lock so no new Use can register them.
	pending := make([]*cacheEntry, 0, len(c.entries))
	for ns, entry := range c.entries {
		delete(c.entries, ns)
		c.lru.Remove(entry.lruElem)
		pending = append(pending, entry)
	}
	c.mu.Unlock()

	// Wait for the sweeper goroutine to exit before closing the drained connections.
	c.wg.Wait()

	var firstErr error
	for _, entry := range pending {
		_, err := closeEntry(entry)
		if err != nil {
			c.cfg.logger.Warn().
				Err(err).
				Str("database_id", entry.databaseID).
				Msg("sqlitelocal: closing connection on cache shutdown")
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// runSweeper wakes every cfg.sweepEvery and evicts entries idle past cfg.ttl. Exits when ctx is cancelled (Close).
func (c *connectorCache) runSweeper(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.sweepEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.sweepOnce(time.Now())
		}
	}
}

// sweepOnce removes entries with time.Since(lastUsed) > ttl from the map+LRU
// under cache.mu, then closes their connections after releasing cache.mu.
// Exposed (lowercase) so tests can drive the sweeper deterministically.
func (c *connectorCache) sweepOnce(now time.Time) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}

	var victims []*cacheEntry
	for ns, entry := range c.entries {
		if now.Sub(entry.lastUsed) <= c.cfg.ttl {
			continue
		}
		// Skip still-loading entries — they have no conn yet, and their
		// inserter will manage them.
		select {
		case <-entry.ready:
		default:
			continue
		}
		delete(c.entries, ns)
		c.lru.Remove(entry.lruElem)
		victims = append(victims, entry)
	}
	c.mu.Unlock()

	for _, entry := range victims {
		alreadyClosed, err := closeEntry(entry)
		if alreadyClosed {
			c.cfg.logger.Warn().
				Str("database_id", entry.databaseID).
				Msg("sqlitelocal: sweeper observed already-closed entry")
		}
		if err != nil {
			c.cfg.logger.Warn().
				Err(err).
				Str("database_id", entry.databaseID).
				Msg("sqlitelocal: closing TTL-evicted connection")
		}
	}
}

// evictOverCapLocked removes the LRU entry (excluding `inserted`) when the
// cache exceeds maxEntries. Caller holds c.mu. Returns the evicted entry, if
// any, for the caller to close after dropping c.mu.
func (c *connectorCache) evictOverCapLocked(inserted *cacheEntry) *cacheEntry {
	if len(c.entries) <= c.cfg.maxEntries {
		return nil
	}
	// Walk back-to-front to find the oldest entry that isn't `inserted`.
	for e := c.lru.Back(); e != nil; e = e.Prev() {
		victim := e.Value.(*cacheEntry)
		if victim == inserted {
			continue
		}
		// Skip still-loading entries — closing them mid-load is messy and
		// they'll get cleaned up when load returns or on the next sweep.
		select {
		case <-victim.ready:
		default:
			continue
		}
		delete(c.entries, victim.databaseID)
		c.lru.Remove(victim.lruElem)
		return victim
	}
	return nil
}

// load resolves the DEK and opens the SQLCipher file for entry, populating
// entry.conn or entry.loadErr. Closes entry.ready before returning so any
// waiters proceed. Safe to call only on a freshly-inserted entry.
func (c *connectorCache) load(ctx context.Context, entry *cacheEntry) {
	defer close(entry.ready)

	dek, err := c.cfg.dekResolver(ctx, entry.databaseID)
	if err != nil {
		entry.loadErr = fmt.Errorf("resolving DEK for %q: %w", entry.databaseID, err)
		return
	}
	if len(dek) != KeySize {
		entry.loadErr = fmt.Errorf("sqlitelocal: dekResolver returned %d bytes, want %d", len(dek), KeySize)
		return
	}

	// The database exists in the instance's records, so its file must too: a missing one is reported, never
	// recreated empty.
	path := filepath.Join(c.cfg.dataDir, entry.databaseID+".db")
	conn, err := openExisting(path, dek, c.cfg.busyTimeout)
	if err != nil {
		if _, serr := os.Stat(path); errors.Is(serr, fs.ErrNotExist) {
			err = fmt.Errorf("%w: %q", domain.ErrDatabaseFileMissing, path)
		}
		entry.loadErr = fmt.Errorf("opening %q: %w", path, err)
		return
	}
	// Locks down ATTACH at the engine level on every cached production
	// connection. See Conn.DisableAttach for the security rationale.
	conn.DisableAttach()
	entry.conn = conn
}

// waitAndCall blocks on entry.ready (or ctx), then takes the connection slot (or gives up when ctx ends) and runs fn.
// The caller already holds entry.entryMu.RLock.
func waitAndCall(ctx context.Context, entry *cacheEntry, fn func(*Conn) error) error {
	select {
	case <-entry.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	if entry.loadErr != nil {
		return entry.loadErr
	}
	release, err := entry.acquireConn(ctx)
	if err != nil {
		return err
	}
	defer release()
	return fn(entry.conn)
}

// closeEntry takes entryMu.Lock, closes the connection if not already closed,
// and marks entry.closed. Returns alreadyClosed=true if a prior caller already
// closed the entry — used by the sweeper to flag anomalous double-evictions.
// Idempotent.
func closeEntry(entry *cacheEntry) (alreadyClosed bool, err error) {
	entry.entryMu.Lock()
	defer entry.entryMu.Unlock()

	if entry.closed {
		return true, nil
	}
	entry.closed = true
	if entry.conn == nil {
		return false, nil
	}
	return false, entry.conn.Close()
}

// closedChan returns a chan that is already closed. Used as a sentinel for
// entries inserted with a pre-opened connection (no load needed).
func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
