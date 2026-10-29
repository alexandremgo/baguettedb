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

// bdbDirectory lists, reads, creates and changes the account's bdbs in the bdbs app.
type bdbDirectory interface {
	// ListBdbs returns the bdbs the account belongs to, soft-deleted ones excluded, with its role in each.
	ListBdbs(ctx context.Context) ([]domain.Bdb, error)

	// GetBdb returns the bdb bdbID, with the account's role in it.
	GetBdb(ctx context.Context, bdbID uuid.UUID) (domain.Bdb, error)

	// CreateBdb creates a blank bdb owned by the account and returns it. The name and description are validated by
	// the bdbs app, which also enforces the account's bdb quota.
	CreateBdb(ctx context.Context, name, description string) (domain.Bdb, error)

	// UpdateBdb changes the name, the description, or both, of the bdb bdbID, which the account owns, and returns it; a
	// nil field is left as it is. A name another of the owner's bdbs has wraps domain.ErrBdbNameTaken, and a bdb the
	// account does not own domain.ErrNotBdbOwner.
	UpdateBdb(ctx context.Context, bdbID uuid.UUID, name, description *string) (domain.Bdb, error)

	// OwnerName returns the display name of the bdb bdbID's owner, empty when the account owns it.
	OwnerName(ctx context.Context, bdbID uuid.UUID) (string, error)
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

	// ListTableSchemas returns the user tables of the bdb bdbID with their columns and foreign keys, platform tables
	// excluded. A table whose schema could not be read carries a DescribeError instead.
	ListTableSchemas(ctx context.Context, bdbID uuid.UUID) ([]apptypes.TableSchema, error)
}

// skillLister lists a bdb's skills in the databases app.
type skillLister interface {
	// ListSkills returns every skill of the bdb bdbID, with its body.
	ListSkills(ctx context.Context, bdbID uuid.UUID) ([]apptypes.Skill, error)
}

// formulaLister lists a bdb's formulas in the databases app.
type formulaLister interface {
	// ListFormulas returns every formula of the bdb bdbID, hidden ones included, as agents see them.
	ListFormulas(ctx context.Context, bdbID uuid.UUID) ([]apptypes.Formula, error)
}

// BdbService runs the bdb use cases: listing, resolving a reference, describing, creating and changing.
type BdbService struct {
	bdbs     bdbDirectory
	starters starterSeeder
	tables   tableLister
	formulas formulaLister
	skills   skillLister
}

// NewBdbService creates a BdbService over the bdbs, starters and databases apps.
func NewBdbService(
	bdbs bdbDirectory, starters starterSeeder, tables tableLister, formulas formulaLister, skills skillLister,
) *BdbService {
	return &BdbService{bdbs: bdbs, starters: starters, tables: tables, formulas: formulas, skills: skills}
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

// Update changes the name, the description, or both, of the bdb ref names; a nil field is left as it is.
func (s *BdbService) Update(ctx context.Context, ref string, name, description *string) (domain.Bdb, error) {
	b, err := s.Resolve(ctx, ref)
	if err != nil {
		return domain.Bdb{}, err
	}
	return s.bdbs.UpdateBdb(ctx, b.ID, name, description)
}

// Describe returns what an agent reads before working in the bdb ref names: the bdb, its owner when the account is not
// the owner, its tables with their schemas, its skills with their bodies, bdb-level ones first, and its formulas.
func (s *BdbService) Describe(ctx context.Context, ref string) (domain.BdbDescription, error) {
	b, err := s.Resolve(ctx, ref)
	if err != nil {
		return domain.BdbDescription{}, err
	}
	d := domain.BdbDescription{Bdb: b}
	if d.Owner, err = s.bdbs.OwnerName(ctx, b.ID); err != nil {
		return domain.BdbDescription{}, fmt.Errorf("in %s: reading its owner: %w", b.Name, err)
	}
	if d.Tables, err = s.tables.ListTableSchemas(ctx, b.ID); err != nil {
		return domain.BdbDescription{}, fmt.Errorf("in %s: reading its tables: %w", b.Name, err)
	}
	if d.Skills, err = s.skills.ListSkills(ctx, b.ID); err != nil {
		return domain.BdbDescription{}, fmt.Errorf("in %s: reading its skills: %w", b.Name, err)
	}
	domain.OrderSkills(d.Skills)
	if d.Formulas, err = s.formulas.ListFormulas(ctx, b.ID); err != nil {
		return domain.BdbDescription{}, fmt.Errorf("in %s: reading its formulas: %w", b.Name, err)
	}
	return d, nil
}
