package service

import (
	"context"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// databaseExporter exports a bdb's database from the databases app.
type databaseExporter interface {
	// ExportDatabase writes a consistent copy of the bdb bdbID's database to w, encrypted under a one-time passphrase
	// it returns with the size written. The databases app allows it to the bdb's owner only, within its export limits.
	ExportDatabase(ctx context.Context, bdbID uuid.UUID, w io.Writer) (domain.Export, error)
}

// ExportService runs the export use case.
type ExportService struct {
	bdbs     bdbResolver
	exporter databaseExporter
}

// NewExportService creates an ExportService resolving bdbs with bdbs and exporting from the databases app.
func NewExportService(bdbs bdbResolver, exporter databaseExporter) *ExportService {
	return &ExportService{bdbs: bdbs, exporter: exporter}
}

// Export writes the database of the bdb ref names to w.
func (s *ExportService) Export(ctx context.Context, ref string, w io.Writer) (domain.Bdb, domain.Export, error) {
	b, err := s.bdbs.Resolve(ctx, ref)
	if err != nil {
		return domain.Bdb{}, domain.Export{}, err
	}
	exported, err := s.exporter.ExportDatabase(ctx, b.ID, w)
	if err != nil {
		return b, domain.Export{}, fmt.Errorf("exporting %s: %w", b.Name, err)
	}
	return b, exported, nil
}
