// Package service runs the command line's use cases against the backend apps of one instance. Each service declares
// the slices of those apps it needs, and every implementation acts as the one account it is bound to, so no method
// takes a user.
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
)

// bdbDirectory lists, reads and creates the account's bdbs in the bdbs app.
type bdbDirectory interface {
	// ListBdbs returns the bdbs the account belongs to, soft-deleted ones excluded, with its role in each.
	ListBdbs(ctx context.Context) ([]domain.Bdb, error)

	// GetBdb returns the bdb bdbID, with the account's role in it.
	GetBdb(ctx context.Context, bdbID uuid.UUID) (domain.Bdb, error)

	// CreateBdb creates a blank bdb owned by the account and returns it. The name and description are validated by
	// the bdbs app, which also enforces the account's bdb quota.
	CreateBdb(ctx context.Context, name, description string) (domain.Bdb, error)
}

// starterSeeder creates bdbs from the starters app's catalog.
type starterSeeder interface {
	// CreateFromStarter creates a bdb owned by the account, seeded from the starter starterKey, and returns its ID.
	// An empty name or description takes the starter's own. An unknown key wraps domain.ErrStarterNotFound. On a
	// seeding failure no bdb is kept.
	CreateFromStarter(ctx context.Context, starterKey, name, description string) (uuid.UUID, error)
}

// tableLister lists a bdb's tables in the databases app.
type tableLister interface {
	// ListTables returns the user tables of the bdb bdbID, platform tables excluded.
	ListTables(ctx context.Context, bdbID uuid.UUID) ([]apptypes.TableInfo, error)
}

// formulaLister lists a bdb's formulas in the databases app.
type formulaLister interface {
	// ListFormulas returns every formula of the bdb bdbID, hidden ones included, as agents see them.
	ListFormulas(ctx context.Context, bdbID uuid.UUID) ([]apptypes.Formula, error)
}

// BdbService runs the bdb use cases: listing, resolving a reference, creating.
type BdbService struct {
	bdbs     bdbDirectory
	starters starterSeeder
	tables   tableLister
	formulas formulaLister
}

// NewBdbService creates a BdbService over the bdbs, starters and databases apps.
func NewBdbService(
	bdbs bdbDirectory, starters starterSeeder, tables tableLister, formulas formulaLister,
) *BdbService {
	return &BdbService{bdbs: bdbs, starters: starters, tables: tables, formulas: formulas}
}

// List returns the account's bdbs with their tables. A bdb whose tables cannot be read is still listed, with
// TablesErr set.
func (s *BdbService) List(ctx context.Context) ([]domain.BdbSummary, error) {
	bdbs, err := s.bdbs.ListBdbs(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing bdbs: %w", err)
	}
	summaries := make([]domain.BdbSummary, len(bdbs))
	for i, b := range bdbs {
		summaries[i] = domain.BdbSummary{Bdb: b}
		summaries[i].Tables, summaries[i].TablesErr = s.tables.ListTables(ctx, b.ID)
	}
	return summaries, nil
}

// Resolve returns the account's bdb that ref names, as domain.ResolveBdb decides.
func (s *BdbService) Resolve(ctx context.Context, ref string) (domain.Bdb, error) {
	bdbs, err := s.bdbs.ListBdbs(ctx)
	if err != nil {
		return domain.Bdb{}, fmt.Errorf("listing bdbs: %w", err)
	}
	return domain.ResolveBdb(ref, bdbs)
}

// Create creates a bdb, seeded from the starter starterKey when it is not empty and blank otherwise, and returns it
// with its tables and formulas. With a starter, an empty name or description takes the starter's own. Failing to
// read the contents back is not an error: the bdb exists, so the result carries ContentsErr instead.
func (s *BdbService) Create(ctx context.Context, name, description, starterKey string) (domain.CreatedBdb, error) {
	created := domain.CreatedBdb{StarterKey: starterKey}
	if starterKey == "" {
		b, err := s.bdbs.CreateBdb(ctx, name, description)
		if errors.Is(err, domain.ErrBdbNameTaken) {
			return domain.CreatedBdb{}, err
		}
		if err != nil {
			return domain.CreatedBdb{}, fmt.Errorf("creating the bdb: %w", err)
		}
		created.Bdb = b
	} else {
		id, err := s.starters.CreateFromStarter(ctx, starterKey, name, description)
		if errors.Is(err, domain.ErrBdbNameTaken) {
			return domain.CreatedBdb{}, err
		}
		if err != nil {
			return domain.CreatedBdb{}, fmt.Errorf("creating the bdb: %w", err)
		}
		if created.Bdb, err = s.bdbs.GetBdb(ctx, id); err != nil {
			return domain.CreatedBdb{}, fmt.Errorf("reading back the new bdb %s: %w", id, err)
		}
	}

	tables, err := s.tables.ListTables(ctx, created.ID)
	if err != nil {
		created.ContentsErr = fmt.Errorf("reading the tables: %w", err)
		return created, nil
	}
	formulas, err := s.formulas.ListFormulas(ctx, created.ID)
	if err != nil {
		created.ContentsErr = fmt.Errorf("reading the formulas: %w", err)
		return created, nil
	}
	created.Tables, created.Formulas = tables, formulas
	return created, nil
}
