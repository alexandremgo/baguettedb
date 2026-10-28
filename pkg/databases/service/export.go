package service

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// rowStreamer executes a single read statement and yields rows one at a time, without materializing the
// result set. Implementations run with the reserved-prefix authorizer active (tenant mode) and hold the
// database's connection only while scanning — never at consumer speed.
type rowStreamer interface {
	// ExecuteStream runs sql with the given args, invoking fn once per result row with the column metadata
	// and the scanned row values. A non-nil error from fn aborts the scan and is propagated unwrapped, so
	// callers can match their own sentinel errors with errors.Is.
	ExecuteStream(ctx context.Context, databaseID string, sql string, args []domain.TypedValue,
		fn func(cols []domain.Column, row []domain.TypedValue) error) error
}

// ExportService produces user-facing exports of a bdb's data. Artifacts are fully spooled to a staging
// directory and handed to the HTTP layer to stream, so no SQLite connection is ever held at client download
// speed and error responses stay truthful (nothing is written to the response before the artifact is ready).
// Every attempt is audit-logged; successful full-database exports count against a rolling-window rate limit
// resolved from the user's roles.
type ExportService struct {
	dbSvc         *DatabaseService
	formulaSvc    *FormulaService
	snapshots     snapshotter
	streamer      rowStreamer
	repo          ExportRepository
	quotaResolver quota.Resolver
	stagingDir    string
	queryTimeout  time.Duration
	logger        zerolog.Logger
	inFlight      sync.Map
}

// NewExportService creates an ExportService spooling artifacts in stagingDir, which must sit on the same
// volume as the database files (a database export is a full copy — container ephemeral storage is too
// small). The directory is created if needed and swept of leftovers from interrupted exports.
// queryTimeout bounds the query that spools a formula's rows; a non-positive value disables the ceiling.
func NewExportService(
	dbSvc *DatabaseService,
	formulaSvc *FormulaService,
	snapshots snapshotter,
	streamer rowStreamer,
	repo ExportRepository,
	quotaResolver quota.Resolver,
	stagingDir string,
	queryTimeout time.Duration,
	logger zerolog.Logger,
) (*ExportService, error) {
	if stagingDir == "" {
		return nil, errors.New("export: stagingDir is required")
	}
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return nil, fmt.Errorf("export: creating staging dir %q: %w", stagingDir, err)
	}

	s := &ExportService{
		dbSvc:         dbSvc,
		formulaSvc:    formulaSvc,
		snapshots:     snapshots,
		streamer:      streamer,
		repo:          repo,
		quotaResolver: quotaResolver,
		stagingDir:    stagingDir,
		queryTimeout:  queryTimeout,
		logger:        logger,
	}
	s.sweepStagingDir()
	return s, nil
}

// ExportArtifact is a produced export ready to stream: the spool file, its download metadata, and (for a
// database export) the one-time passphrase that opens it. Finish must be called exactly once after streaming
// ends: it records the outcome, deletes the spool file, and releases the database's in-flight export slot.
type ExportArtifact struct {
	Path       string
	Filename   string
	SizeBytes  int64
	Passphrase string

	finish func(streamErr error, bytesStreamed int64)
}

// Finish finalizes the export attempt after streaming: a nil streamErr marks it successful with the streamed
// byte count, a non-nil one marks it failed (a failed attempt does not consume the rate-limit window).
func (a *ExportArtifact) Finish(streamErr error, bytesStreamed int64) {
	a.finish(streamErr, bytesStreamed)
}

// ExportDatabase produces a fresh, consistent copy of the bdb's database re-encrypted under a newly
// generated one-time passphrase, ready to stream. Requires DBPermExport (owner-only). Enforces the
// rolling-window export limit — counting successes only — and a single in-flight export per database. The
// passphrase is returned once on the artifact and never persisted.
func (s *ExportService) ExportDatabase(ctx context.Context, userID, bdbID uuid.UUID) (*ExportArtifact, error) {
	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermExport)
	if err != nil {
		return nil, err
	}
	databaseID := db.ID.String()
	now := time.Now()

	roles := authz.RolesFromContext(ctx)
	if maxExports := s.quotaResolver(roles).MaxDBExportsPerWindow; maxExports > 0 {
		window := s.quotaResolver(roles).DBExportWindow
		count, oldest, cerr := s.repo.CountSuccessesSince(
			ctx, db.ID, userID, domain.ExportKindDatabase, now.Add(-window))
		if cerr != nil {
			return nil, fmt.Errorf("counting recent exports: %w", cerr)
		}
		if count >= maxExports {
			// The slot frees when the oldest counted success leaves the rolling window.
			retryAfter := window
			if oldest != nil {
				retryAfter = max(oldest.Add(window).Sub(now), time.Second)
			}
			return nil, &domain.ExportRateLimitedError{RetryAfter: retryAfter}
		}
	}

	release, err := s.acquireInFlight(databaseID)
	if err != nil {
		return nil, err
	}

	if err := s.ensureStagingSpace(ctx, databaseID); err != nil {
		release()
		return nil, err
	}

	passphrase, err := generateExportPassphrase()
	if err != nil {
		release()
		return nil, err
	}

	rec := domain.ExportRecord{
		ID:         uuid.New(),
		DatabaseID: db.ID,
		UserID:     userID,
		Kind:       domain.ExportKindDatabase,
		Outcome:    domain.ExportOutcomeStarted,
		StartedAt:  now,
	}
	if err := s.repo.RecordStart(ctx, rec); err != nil {
		release()
		return nil, fmt.Errorf("recording export start: %w", err)
	}

	spoolPath := filepath.Join(s.stagingDir, fmt.Sprintf("%s-export-%d.sqlite", databaseID, now.UnixNano()))
	if err := s.snapshots.Export(ctx, databaseID, spoolPath, passphrase); err != nil {
		s.recordOutcome(rec.ID, domain.ExportOutcomeFailed, 0)
		release()
		return nil, fmt.Errorf("producing export: %w", err)
	}

	info, err := os.Stat(spoolPath)
	if err != nil {
		s.recordOutcome(rec.ID, domain.ExportOutcomeFailed, 0)
		s.removeSpool(spoolPath)
		release()
		return nil, fmt.Errorf("stating export spool: %w", err)
	}

	return &ExportArtifact{
		Path:       spoolPath,
		Filename:   fmt.Sprintf("bdb-%s-%s.sqlite", bdbID, now.UTC().Format("20060102T150405Z")),
		SizeBytes:  info.Size(),
		Passphrase: passphrase,
		finish: func(streamErr error, bytesStreamed int64) {
			outcome := domain.ExportOutcomeSuccess
			if streamErr != nil {
				outcome = domain.ExportOutcomeFailed
			}
			s.recordOutcome(rec.ID, outcome, bytesStreamed)
			s.removeSpool(spoolPath)
			release()
		},
	}, nil
}

// ExportStatus describes the caller's export allowances for a bdb's database: whether they may run a full
// database export, the rolling-window quota state, and the formula CSV row cap. Non-positive limits mean
// unlimited.
type ExportStatus struct {
	DatabaseExportAllowed bool
	MaxPerWindow          int
	Window                time.Duration
	UsedInWindow          int
	NextAvailableAt       *time.Time
	MaxFormulaRows        int
}

// GetExportStatus reports the caller's export allowances for the bdb. Requires DBPermRead only, so any
// member can read the formula row cap; DatabaseExportAllowed reflects whether the caller also holds the
// export permission.
func (s *ExportService) GetExportStatus(ctx context.Context, userID, bdbID uuid.UUID) (*ExportStatus, error) {
	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermRead)
	if err != nil {
		return nil, err
	}

	allowed := true
	if perr := s.dbSvc.CheckDatabasePermission(ctx, userID, bdbID, authz.DBPermExport); perr != nil {
		if !errors.Is(perr, domain.ErrAccessDenied) {
			return nil, perr
		}
		allowed = false
	}

	roles := authz.RolesFromContext(ctx)
	status := &ExportStatus{
		DatabaseExportAllowed: allowed,
		MaxPerWindow:          s.quotaResolver(roles).MaxDBExportsPerWindow,
		Window:                s.quotaResolver(roles).DBExportWindow,
		MaxFormulaRows:        s.quotaResolver(roles).MaxFormulaExportRows,
	}
	if status.MaxPerWindow > 0 {
		now := time.Now()
		count, oldest, cerr := s.repo.CountSuccessesSince(
			ctx, db.ID, userID, domain.ExportKindDatabase, now.Add(-status.Window))
		if cerr != nil {
			return nil, fmt.Errorf("counting recent exports: %w", cerr)
		}
		status.UsedInWindow = count
		if count >= status.MaxPerWindow && oldest != nil {
			next := oldest.Add(status.Window)
			status.NextAvailableAt = &next
		}
	}
	return status, nil
}

// acquireInFlight reserves databaseID's single in-flight export slot, returning the release func that frees
// it. A second export for the same database while one runs gets ErrExportInFlight. The slot is what bounds
// snapshot work against abort-and-retry loops, since only successful exports consume the rate-limit window;
// the shard router guarantees a single owning pod per bdb, so an in-process reservation suffices.
func (s *ExportService) acquireInFlight(databaseID string) (func(), error) {
	_, loaded := s.inFlight.LoadOrStore(databaseID, struct{}{})
	if loaded {
		return nil, domain.ErrExportInFlight
	}

	return func() {
		s.inFlight.Delete(databaseID)
	}, nil
}

// ensureStagingSpace refuses to start an export when the staging volume's free space cannot hold a copy of
// the live database file — degrading exports, never wedging the volume shared with the database files.
func (s *ExportService) ensureStagingSpace(ctx context.Context, databaseID string) error {
	stats, err := s.snapshots.GetDatabaseStats(ctx, databaseID)
	if err != nil {
		return fmt.Errorf("reading database stats: %w", err)
	}
	free, err := freeSpaceBytes(s.stagingDir)
	if err != nil {
		return fmt.Errorf("reading staging free space: %w", err)
	}
	if free < 0 || uint64(free) < stats.StorageBytesUsed {
		return domain.ErrExportLowSpace
	}
	return nil
}

// recordOutcome finalizes an export's audit row on a fresh context — Finish runs after the request context
// may already be cancelled (client abort), and the audit row must still be written.
func (s *ExportService) recordOutcome(id uuid.UUID, outcome domain.ExportOutcome, bytes int64) {
	if err := s.repo.RecordOutcome(context.Background(), id, outcome, bytes); err != nil {
		s.logger.Error().Err(err).Str("export_id", id.String()).Msg("export: recording outcome")
	}
}

// removeSpool deletes an export spool file, logging instead of failing — by this point the export outcome is
// already decided. Missing files are expected on some failure paths.
func (s *ExportService) removeSpool(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.logger.Warn().Err(err).Str("path", path).Msg("export: removing spool file")
	}
}

// sweepStagingDir removes leftover spool files from exports interrupted by a crash or restart — the moment
// that bounds how long an orphaned artifact can persist on the volume. Best-effort: failures are logged and
// the service still starts.
func (s *ExportService) sweepStagingDir() {
	entries, err := os.ReadDir(s.stagingDir)
	if err != nil {
		s.logger.Warn().Err(err).Str("dir", s.stagingDir).Msg("export: reading staging dir for sweep")
		return
	}
	if len(entries) == 0 {
		return
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
		path := filepath.Join(s.stagingDir, entry.Name())
		if err := os.Remove(path); err != nil {
			s.logger.Warn().Err(err).Str("path", path).Msg("export: sweeping leftover spool file")
		}
	}
	s.logger.Info().Int("count", len(names)).Strs("files", names).
		Msg("Export: swept leftover spool files from interrupted exports")
}

// generateExportPassphrase returns a fresh high-entropy passphrase (160 bits as unpadded base32, 32 chars)
// for keying a single export artifact. Generated per export and never persisted anywhere.
func generateExportPassphrase() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating export passphrase: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}
