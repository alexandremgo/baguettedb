package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/migrations"
)

// importSpoolPattern names the files ImportService spools, for os.CreateTemp and the sweep alike.
const importSpoolPattern = "import-*.sqlite"

// importSpoolMaxAge is how old a spool file gets before the sweep removes it as left by an import that never
// finished: far longer than any import runs, so a sweep never removes the spool of another process's import.
const importSpoolMaxAge = 24 * time.Hour

// emptyDatabaseQuery counts what an import would replace: user tables and views, formulas and skills.
const emptyDatabaseQuery = `SELECT
	(SELECT count(*) FROM sqlite_master WHERE type IN ('table', 'view') AND name NOT LIKE '\_%' ESCAPE '\'
		AND name NOT LIKE 'sqlite\_%' ESCAPE '\')
	+ (SELECT count(*) FROM _formulas) + (SELECT count(*) FROM _skills)`

// fileImporter checks an exported database file and installs it as a database's live file.
type fileImporter interface {
	// InspectImport opens the file at path, unencrypted or under passphrase, and describes it. An encrypted file
	// without a passphrase is a domain.ErrImportPassphraseNeeded; an empty one a domain.ErrImportNotExport; one that
	// doesn't open a domain.ErrImportUnreadable; one too damaged to open a domain.ErrImportDamaged.
	InspectImport(ctx context.Context, path, passphrase string) (domain.ImportFile, error)

	// InstallImport copies the file at path, unencrypted or under passphrase, re-encrypted under databaseID's own key,
	// and installs the copy as databaseID's live file, replacing the current one. path stays where it is.
	InstallImport(ctx context.Context, databaseID, path, passphrase string) error
}

// ImportService installs bdb exports as databases: a file `bdb bdbs export` wrote, encrypted under its passphrase or
// decrypted, becomes the database of an empty bdb. Every file is spooled to a staging directory first, so it may come
// from a local file as well as from an upload.
type ImportService struct {
	dbSvc      *DatabaseService
	importer   fileImporter
	stagingDir string
	logger     zerolog.Logger
}

// NewImportService creates an ImportService spooling files in stagingDir, which it creates, and removes the spools
// older than importSpoolMaxAge found there.
func NewImportService(
	dbSvc *DatabaseService, importer fileImporter, stagingDir string, logger zerolog.Logger,
) (*ImportService, error) {
	if stagingDir == "" {
		return nil, errors.New("import: stagingDir is required")
	}
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return nil, fmt.Errorf("import: creating staging dir %q: %w", stagingDir, err)
	}
	s := &ImportService{dbSvc: dbSvc, importer: importer, stagingDir: stagingDir, logger: logger}
	s.sweepStagingDir(time.Now())
	return s, nil
}

// CheckImport reads the file src holds and checks that it can be imported, under passphrase when it is encrypted,
// without importing it anywhere. Its errors are ImportDatabase's about the file.
func (s *ImportService) CheckImport(ctx context.Context, src io.Reader, passphrase string) error {
	path, _, err := s.spool(src, 0)
	if err != nil {
		return err
	}
	defer s.removeSpool(path)
	_, err = s.inspect(ctx, path, passphrase)
	return err
}

// ImportDatabase installs the bdb export src holds, under passphrase when it is encrypted, as the database of the bdb
// bdbID, then brings it to this version's platform schema. Requires DBPermImport (owner-only).
//
// The bdb must be empty, holding no tables, formulas or skills, or the import is a domain.ErrImportTargetNotEmpty:
// it replaces the database whole. A file larger than the bdb's database may be is a domain.ErrImportTooLarge, and one
// past the owner's storage quota a domain.ErrStorageQuotaExceeded.
//
// A file that isn't a bdb export is a domain.ErrImportNotExport; an encrypted one without a passphrase a
// domain.ErrImportPassphraseNeeded; one that doesn't open a domain.ErrImportUnreadable; a damaged one a
// domain.ErrImportDamaged; one from a newer version a domain.ErrImportNewer.
func (s *ImportService) ImportDatabase(
	ctx context.Context, userID, bdbID uuid.UUID, src io.Reader, passphrase string,
) (err error) {
	logged := s.logger.With().Str("user_id", userID.String()).Str("bdb_id", bdbID.String()).Logger()
	var size int64
	defer func() {
		if err != nil {
			logged.Info().Err(err).Int64("size_bytes", size).Msg("import: refused or failed")
			return
		}
		logged.Info().Int64("size_bytes", size).Msg("import: bdb export installed")
	}()

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermImport)
	if err != nil {
		return err
	}
	databaseID := db.ID.String()
	ownerID, roles, err := s.dbSvc.bdbOwner(ctx, bdbID)
	if err != nil {
		return fmt.Errorf("getting the bdb owner: %w", err)
	}
	limits := s.dbSvc.limits(roles)

	path, size, err := s.spool(src, limits.MaxDBSizeBytes)
	if err != nil {
		return err
	}
	defer s.removeSpool(path)
	if limit := limits.MaxTotalDBSizeBytes; limit > 0 {
		total, err := s.dbSvc.TotalOwnedSizeBytes(ctx, ownerID)
		if err != nil {
			return fmt.Errorf("computing owner storage total: %w", err)
		}
		if total+size > limit {
			return fmt.Errorf("%w (%d bytes tracked, %d imported, limit %d)", domain.ErrStorageQuotaExceeded, total,
				size, limit)
		}
	}
	if _, err := s.inspect(ctx, path, passphrase); err != nil {
		return err
	}

	// Serializes with the platform migration runner, which the installed file goes through next.
	mu := s.dbSvc.migrationLockFor(databaseID)
	mu.Lock()
	defer mu.Unlock()
	if err := s.requireEmpty(ctx, databaseID); err != nil {
		return err
	}
	if err := s.importer.InstallImport(ctx, databaseID, path, passphrase); err != nil {
		return fmt.Errorf("installing the file: %w", err)
	}
	return s.settle(ctx, db, limits.MaxDBSizeBytes)
}

// inspect describes the file at path, opened under passphrase when encrypted, and returns the error refusing it, if
// any.
func (s *ImportService) inspect(ctx context.Context, path, passphrase string) (domain.ImportFile, error) {
	file, err := s.importer.InspectImport(ctx, path, passphrase)
	switch {
	case err != nil:
		return domain.ImportFile{}, err
	case !file.Intact:
		return domain.ImportFile{}, domain.ErrImportDamaged
	case file.PlatformSchemaVersion > CurrentPlatformSchemaVersion:
		return domain.ImportFile{}, domain.ErrImportNewer
	case !file.HasPlatformTables:
		return domain.ImportFile{}, domain.ErrImportNotExport
	}
	return file, nil
}

// requireEmpty returns domain.ErrImportTargetNotEmpty unless the database databaseID holds no user tables or views,
// formulas or skills.
func (s *ImportService) requireEmpty(ctx context.Context, databaseID string) error {
	result, err := s.dbSvc.executor.ExecuteInternal(ctx, databaseID, emptyDatabaseQuery, nil)
	if err != nil {
		return fmt.Errorf("checking the bdb is empty: %w", err)
	}
	if len(result.Rows) != 1 || len(result.Rows[0]) != 1 {
		return fmt.Errorf("checking the bdb is empty: %d rows", len(result.Rows))
	}
	if count, ok := result.Rows[0][0].Value.(int64); !ok || count != 0 {
		return domain.ErrImportTargetNotEmpty
	}
	return nil
}

// settle brings the database db, just installed from an export, to this version: it runs the platform migrations
// the file lacks, installs the change-log triggers on every user table afresh, so the change log records what the
// file's own triggers might not, mirrors the schema version, and applies maxSize (none when 0) and the new size.
func (s *ImportService) settle(ctx context.Context, db *domain.Database, maxSize int64) error {
	databaseID := db.ID.String()
	migCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), platformMigrationTimeout)
	defer cancel()
	version, err := runPlatformMigrations(migCtx, s.dbSvc.executor, databaseID, migrations.PlatformFS, s.logger)
	if err != nil {
		return fmt.Errorf("running platform migrations on the imported file: %w", err)
	}
	if err := regenChangeLogTriggersForAllUserTables(migCtx, s.dbSvc.executor, databaseID); err != nil {
		return fmt.Errorf("installing the change-log triggers: %w", err)
	}
	if err := s.dbSvc.repo.UpdatePlatformSchemaVersion(ctx, db.ID, version); err != nil {
		return fmt.Errorf("recording the platform schema version: %w", err)
	}
	if maxSize > 0 {
		if err := s.dbSvc.dbMgr.SetDatabaseMaxSize(ctx, databaseID, maxSize); err != nil {
			return fmt.Errorf("setting the database size limit: %w", err)
		}
	}
	// The size was refreshed when the bdb was created, moments ago: forgetting it lets this refresh through.
	s.dbSvc.lastSizeRefresh.Delete(db.ID)
	s.dbSvc.RefreshStoredSize(ctx, databaseID)
	return nil
}

// spool copies src into a new owner-only file in the staging directory and returns its path and size. With a positive
// limit, a source longer than limit bytes is a domain.ErrImportTooLarge.
func (s *ImportService) spool(src io.Reader, limit int64) (_ string, size int64, err error) {
	f, err := os.CreateTemp(s.stagingDir, importSpoolPattern)
	if err != nil {
		return "", 0, fmt.Errorf("creating the import spool: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("writing the import spool: %w", cerr)
		}
		if err != nil {
			s.removeSpool(f.Name())
		}
	}()

	r := src
	if limit > 0 {
		r = io.LimitReader(src, limit+1)
	}
	if size, err = io.Copy(f, r); err != nil {
		return "", 0, fmt.Errorf("reading the file: %w", err)
	}
	if limit > 0 && size > limit {
		return "", 0, fmt.Errorf("%w (more than %d bytes)", domain.ErrImportTooLarge, limit)
	}
	return f.Name(), size, nil
}

// removeSpool removes the spool file at path, logging a failure: the import's outcome is decided already.
func (s *ImportService) removeSpool(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.logger.Warn().Err(err).Str("path", path).Msg("import: removing spool file")
	}
}

// sweepStagingDir removes the spool files older than importSpoolMaxAge at now, left by imports that never finished.
func (s *ImportService) sweepStagingDir(now time.Time) {
	entries, err := os.ReadDir(s.stagingDir)
	if err != nil {
		s.logger.Warn().Err(err).Str("dir", s.stagingDir).Msg("import: reading staging dir for sweep")
		return
	}
	prefix, _, _ := strings.Cut(importSpoolPattern, "*")
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !strings.HasPrefix(entry.Name(), prefix) || now.Sub(info.ModTime()) < importSpoolMaxAge {
			continue
		}
		s.removeSpool(filepath.Join(s.stagingDir, entry.Name()))
	}
}
