package service

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// databaseImporter installs bdb exports in the databases app.
type databaseImporter interface {
	// CheckImport reads the file src holds and checks that it is a bdb export the databases app can import, under
	// passphrase when it is encrypted. An encrypted file without a passphrase wraps domain.ErrPassphraseNeeded; one
	// that doesn't open domain.ErrImportUnreadable.
	CheckImport(ctx context.Context, src io.Reader, passphrase string) error

	// ImportDatabase installs the bdb export src holds, under passphrase when it is encrypted, as the database of the
	// empty bdb bdbID. Its errors are CheckImport's, and the databases app's.
	ImportDatabase(ctx context.Context, bdbID uuid.UUID, src io.Reader, passphrase string) error
}

// importBdbs creates the bdb an import fills, and deletes it when the import fails.
type importBdbs interface {
	// CreateBdb creates a blank bdb owned by the account and returns it. A name another bdb of the account has wraps
	// domain.ErrBdbNameTaken.
	CreateBdb(ctx context.Context, name, description string) (domain.Bdb, error)

	// DeleteBdb soft-deletes the bdb bdbID, owned by the account.
	DeleteBdb(ctx context.Context, bdbID uuid.UUID) error
}

// ImportService runs the import use case: a bdb export becomes a new bdb.
type ImportService struct {
	bdbs     importBdbs
	importer databaseImporter
}

// NewImportService creates an ImportService creating bdbs with bdbs and filling them with importer.
func NewImportService(bdbs importBdbs, importer databaseImporter) *ImportService {
	return &ImportService{bdbs: bdbs, importer: importer}
}

// Import creates a bdb named name from the bdb export src holds, read from its start, under passphrase when it is
// encrypted, and returns it.
//
// The file is checked before the bdb is created, so a file that can't be imported creates nothing: an encrypted one
// without a passphrase wraps domain.ErrPassphraseNeeded, one that doesn't open domain.ErrImportUnreadable. An import
// failing once the bdb exists soft-deletes it, which the error says.
func (s *ImportService) Import(ctx context.Context, src io.ReadSeeker, name, passphrase string) (domain.Bdb, error) {
	if err := rewind(src); err != nil {
		return domain.Bdb{}, err
	}
	if err := s.importer.CheckImport(ctx, src, passphrase); err != nil {
		return domain.Bdb{}, err
	}
	if err := rewind(src); err != nil {
		return domain.Bdb{}, err
	}

	b, err := s.bdbs.CreateBdb(ctx, name, "")
	if err != nil {
		return domain.Bdb{}, err
	}
	if err := s.importer.ImportDatabase(ctx, b.ID, src, passphrase); err != nil {
		if derr := s.bdbs.DeleteBdb(ctx, b.ID); derr != nil {
			return domain.Bdb{}, errors.Join(fmt.Errorf("importing into %s: %w", b.Name, err),
				fmt.Errorf("deleting %s after: %w", b.Name, derr))
		}
		return domain.Bdb{}, fmt.Errorf("importing into %s, deleted since: %w", b.Name, err)
	}
	return b, nil
}

// rewind sets src back to its start.
func rewind(src io.Seeker) error {
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("reading the file from its start: %w", err)
	}
	return nil
}
