package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// mockExportRepo implements ExportRepository for testing. Guarded by mu — the in-flight tests call it from
// concurrent exports.
type mockExportRepo struct {
	mu       sync.Mutex
	count    int
	oldest   *time.Time
	countErr error

	started  []domain.ExportRecord
	outcomes map[uuid.UUID]domain.ExportOutcome
	bytes    map[uuid.UUID]int64
}

func (m *mockExportRepo) RecordStart(_ context.Context, rec domain.ExportRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.started = append(m.started, rec)
	return nil
}

func (m *mockExportRepo) RecordOutcome(
	_ context.Context, id uuid.UUID, outcome domain.ExportOutcome, bytes int64,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.outcomes == nil {
		m.outcomes = make(map[uuid.UUID]domain.ExportOutcome)
		m.bytes = make(map[uuid.UUID]int64)
	}
	m.outcomes[id] = outcome
	m.bytes[id] = bytes
	return nil
}

func (m *mockExportRepo) CountSuccessesSince(
	_ context.Context, _, _ uuid.UUID, _ domain.ExportKind, _ time.Time,
) (int, *time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count, m.oldest, m.countErr
}

// mockExportSnapshotter implements snapshotter, writing a small fake spool file on Export. The optional
// entered/proceed channels let a test hold an export inside the copy phase.
type mockExportSnapshotter struct {
	stats     *domain.DatabaseStats
	exportErr error
	entered   chan struct{}
	proceed   chan struct{}
}

func (m *mockExportSnapshotter) Snapshot(context.Context, string, string) error { return nil }

func (m *mockExportSnapshotter) Export(_ context.Context, _, destPath, _ string) error {
	if m.entered != nil {
		select {
		case m.entered <- struct{}{}:
		default:
		}
	}
	if m.proceed != nil {
		<-m.proceed
	}
	if m.exportErr != nil {
		return m.exportErr
	}
	return os.WriteFile(destPath, []byte("fake export content"), 0o600)
}

func (m *mockExportSnapshotter) GetDatabaseStats(context.Context, string) (*domain.DatabaseStats, error) {
	if m.stats != nil {
		return m.stats, nil
	}
	return &domain.DatabaseStats{StorageBytesUsed: 1024}, nil
}

// newTestExportService builds an ExportService on the package's standard mocks, with a database already
// resolved at the current platform schema version (so AuthorizeAndResolveDatabase takes its fast path).
func newTestExportService(
	t *testing.T, repo *mockExportRepo, snaps *mockExportSnapshotter,
) (*ExportService, uuid.UUID) {
	t.Helper()

	db := &domain.Database{
		ID:                    uuid.New(),
		BdbID:                 uuid.New(),
		PlatformSchemaVersion: CurrentPlatformSchemaVersion,
	}
	dbSvc := NewDatabaseService(
		&mockDatabaseRepo{db: db}, &mockDatabaseManager{}, &mockQueryExecutor{}, &mockBdbsApp{},
		quota.Fixed(quota.Limits{}), newStubKEKProvider(t), zerolog.Nop())

	limits := quota.Fixed(quota.Limits{MaxDBExportsPerWindow: 1, DBExportWindow: 24 * time.Hour})
	svc, err := NewExportService(dbSvc, nil, snaps, nil, repo, limits, t.TempDir(), 0, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewExportService: %v", err)
	}
	return svc, db.BdbID
}

// TestExportDatabase_StreamFailureRecordsFailed verifies a failed download marks the attempt failed (so it
// does not consume the rate-limit window) and still deletes the spool.
func TestExportDatabase_StreamFailureRecordsFailed(t *testing.T) {
	repo := &mockExportRepo{}
	svc, bdbID := newTestExportService(t, repo, &mockExportSnapshotter{})

	artifact, err := svc.ExportDatabase(context.Background(), uuid.New(), bdbID)
	if err != nil {
		t.Fatalf("ExportDatabase: %v", err)
	}

	artifact.Finish(errors.New("client went away"), 3)
	if got := repo.outcomes[repo.started[0].ID]; got != domain.ExportOutcomeFailed {
		t.Errorf("outcome = %s, want failed", got)
	}
	if _, err := os.Stat(artifact.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("spool file should be deleted after Finish, stat err = %v", err)
	}
}

// TestExportDatabase_RateLimitedWithoutOldest verifies the fallback wait (a full window) when the counted
// successes carry no oldest timestamp.
func TestExportDatabase_RateLimitedWithoutOldest(t *testing.T) {
	repo := &mockExportRepo{count: 1}
	svc, bdbID := newTestExportService(t, repo, &mockExportSnapshotter{})

	_, err := svc.ExportDatabase(context.Background(), uuid.New(), bdbID)
	limited, ok := errors.AsType[*domain.ExportRateLimitedError](err)
	if !ok {
		t.Fatalf("err = %T (%v), want *domain.ExportRateLimitedError", err, err)
	}
	if limited.RetryAfter != 24*time.Hour {
		t.Errorf("RetryAfter = %s, want the full 24h window", limited.RetryAfter)
	}
}

// TestExportDatabase_ProduceFailureRecordsFailedAndReleases verifies a failed copy records a failed attempt
// and frees the in-flight slot for the next export.
func TestExportDatabase_ProduceFailureRecordsFailedAndReleases(t *testing.T) {
	repo := &mockExportRepo{}
	snaps := &mockExportSnapshotter{exportErr: errors.New("disk on fire")}
	svc, bdbID := newTestExportService(t, repo, snaps)

	if _, err := svc.ExportDatabase(context.Background(), uuid.New(), bdbID); err == nil {
		t.Fatal("expected error from failed export production")
	}
	if got := repo.outcomes[repo.started[0].ID]; got != domain.ExportOutcomeFailed {
		t.Errorf("outcome = %s, want failed", got)
	}

	snaps.exportErr = nil
	artifact, err := svc.ExportDatabase(context.Background(), uuid.New(), bdbID)
	if err != nil {
		t.Fatalf("export after failed attempt: %v", err)
	}
	artifact.Finish(nil, artifact.SizeBytes)
}

// TestExportDatabase_LowSpace verifies an export is refused when the staging volume cannot hold a copy of
// the live database file.
func TestExportDatabase_LowSpace(t *testing.T) {
	repo := &mockExportRepo{}
	snaps := &mockExportSnapshotter{stats: &domain.DatabaseStats{StorageBytesUsed: ^uint64(0)}}
	svc, bdbID := newTestExportService(t, repo, snaps)

	_, err := svc.ExportDatabase(context.Background(), uuid.New(), bdbID)
	if !errors.Is(err, domain.ErrExportLowSpace) {
		t.Fatalf("err = %v, want ErrExportLowSpace", err)
	}
	if len(repo.started) != 0 {
		t.Errorf("recorded starts = %d, want 0", len(repo.started))
	}
}

// TestNewExportService_SweepsLeftovers verifies leftover spool files from interrupted exports are removed at
// construction time.
func TestNewExportService_SweepsLeftovers(t *testing.T) {
	stagingDir := t.TempDir()
	leftover := filepath.Join(stagingDir, "orphan-export-123.sqlite")
	if err := os.WriteFile(leftover, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	db := &domain.Database{ID: uuid.New(), BdbID: uuid.New(), PlatformSchemaVersion: CurrentPlatformSchemaVersion}
	dbSvc := NewDatabaseService(
		&mockDatabaseRepo{db: db}, &mockDatabaseManager{}, &mockQueryExecutor{}, &mockBdbsApp{},
		quota.Fixed(quota.Limits{}), newStubKEKProvider(t), zerolog.Nop())
	if _, err := NewExportService(
		dbSvc, nil, &mockExportSnapshotter{}, nil, &mockExportRepo{}, quota.Fixed(quota.Limits{}), stagingDir, 0,
		zerolog.Nop(),
	); err != nil {
		t.Fatalf("NewExportService: %v", err)
	}

	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("leftover spool should be swept at startup, stat err = %v", err)
	}
}

// TestExportDatabase_SecondExportWhileInFlight verifies the single-slot guard: while one export for a
// database is producing its copy, a concurrent request fails fast with ErrExportInFlight, and the slot frees
// once the first artifact is finished.
func TestExportDatabase_SecondExportWhileInFlight(t *testing.T) {
	repo := &mockExportRepo{}
	snaps := &mockExportSnapshotter{entered: make(chan struct{}, 4), proceed: make(chan struct{})}
	svc, bdbID := newTestExportService(t, repo, snaps)
	userID := uuid.New()

	type result struct {
		artifact *ExportArtifact
		err      error
	}
	firstDone := make(chan result, 1)
	go func() {
		a, err := svc.ExportDatabase(context.Background(), userID, bdbID)
		firstDone <- result{a, err}
	}()
	<-snaps.entered // the first export is now inside the copy phase, holding the slot

	secondDone := make(chan error, 1)
	go func() {
		_, err := svc.ExportDatabase(context.Background(), userID, bdbID)
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		if !errors.Is(err, domain.ErrExportInFlight) {
			t.Errorf("concurrent export err = %v, want ErrExportInFlight", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("concurrent export did not fail fast; want immediate ErrExportInFlight")
	}

	close(snaps.proceed)
	first := <-firstDone
	if first.err != nil {
		t.Fatalf("first export: %v", first.err)
	}
	first.artifact.Finish(nil, first.artifact.SizeBytes)

	// Finish released the slot: a fresh export must succeed.
	artifact, err := svc.ExportDatabase(context.Background(), userID, bdbID)
	if err != nil {
		t.Fatalf("export after release: %v", err)
	}
	artifact.Finish(nil, artifact.SizeBytes)
}
