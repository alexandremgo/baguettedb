package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// defaultChangeLimit is how many batches a change listing returns when its filter sets no limit.
const defaultChangeLimit = 20

// changeLog reads and reverses a bdb's changes in the databases app.
type changeLog interface {
	// ListChanges returns the bdb bdbID's changes matching filter, newest first, ignoring its Limit: as many batches
	// as the most entries the databases app returns at once hold, the oldest marked Partial when the cap cut it.
	ListChanges(ctx context.Context, bdbID uuid.UUID, filter domain.ChangeFilter) ([]domain.ChangeBatch, error)

	// Undo reverses the account's most recent change in the bdb bdbID, or anyone's when anyone is set, and returns
	// the change that reversed it. An empty undo stack returns domain.ErrNothingToUndo; data or schema that moved on
	// since returns a *domain.ChangeConflictError.
	Undo(ctx context.Context, bdbID uuid.UUID, anyone bool) (domain.QueryResult, error)

	// Redo re-applies the account's most recently undone change in the bdb bdbID, or anyone's when anyone is set. An
	// empty redo stack returns domain.ErrNothingToRedo; a conflict a *domain.ChangeConflictError.
	Redo(ctx context.Context, bdbID uuid.UUID, anyone bool) (domain.QueryResult, error)

	// RevertBatch reverses every entry of the batch batchID in the bdb bdbID, whoever made it. An unknown batch wraps
	// domain.ErrChangeNotFound; a conflict returns a *domain.ChangeConflictError.
	RevertBatch(ctx context.Context, bdbID uuid.UUID, batchID string) (domain.QueryResult, error)

	// RevertEntry reverses the single change log entry entryID in the bdb bdbID, failing as RevertBatch does.
	RevertEntry(ctx context.Context, bdbID uuid.UUID, entryID int64) (domain.QueryResult, error)
}

// ChangeService runs the change history use cases: listing, undo, redo and revert.
type ChangeService struct {
	bdbs    bdbResolver
	changes changeLog
}

// NewChangeService creates a ChangeService resolving bdbs with bdbs and reading their changes from the databases app.
func NewChangeService(bdbs bdbResolver, changes changeLog) *ChangeService {
	return &ChangeService{bdbs: bdbs, changes: changes}
}

// List returns the changes of the bdb ref names that match filter, newest first, at most filter.Limit batches (20 when
// it is 0).
func (s *ChangeService) List(
	ctx context.Context, ref string, filter domain.ChangeFilter,
) (domain.Bdb, []domain.ChangeBatch, error) {
	b, err := s.bdbs.Resolve(ctx, ref)
	if err != nil {
		return domain.Bdb{}, nil, err
	}
	batches, err := s.changes.ListChanges(ctx, b.ID, filter)
	if err != nil {
		return b, nil, fmt.Errorf("in %s: listing changes: %w", b.Name, err)
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultChangeLimit
	}
	return b, batches[:min(limit, len(batches))], nil
}

// Undo reverses the account's most recent change in the bdb ref names, or anyone's when anyone is set.
func (s *ChangeService) Undo(ctx context.Context, ref string, anyone bool) (domain.Bdb, domain.QueryResult, error) {
	return s.reverse(ctx, ref, func(id uuid.UUID) (domain.QueryResult, error) {
		return s.changes.Undo(ctx, id, anyone)
	})
}

// Redo re-applies the account's most recently undone change in the bdb ref names, or anyone's when anyone is set.
func (s *ChangeService) Redo(ctx context.Context, ref string, anyone bool) (domain.Bdb, domain.QueryResult, error) {
	return s.reverse(ctx, ref, func(id uuid.UUID) (domain.QueryResult, error) {
		return s.changes.Redo(ctx, id, anyone)
	})
}

// RevertBatch reverses the batch batchRef names in the bdb ref names: its full ID, or a prefix of it matching one of
// the bdb's recent changes. It returns the full batch ID with the result.
func (s *ChangeService) RevertBatch(
	ctx context.Context, ref, batchRef string,
) (domain.Bdb, string, domain.QueryResult, error) {
	b, err := s.bdbs.Resolve(ctx, ref)
	if err != nil {
		return domain.Bdb{}, "", domain.QueryResult{}, err
	}
	batchID := batchRef
	if _, err := uuid.Parse(batchRef); err != nil {
		recent, err := s.changes.ListChanges(ctx, b.ID, domain.ChangeFilter{})
		if err != nil {
			return b, "", domain.QueryResult{}, fmt.Errorf("in %s: listing changes: %w", b.Name, err)
		}
		if batchID, err = domain.ResolveBatchID(batchRef, recent); err != nil {
			return b, "", domain.QueryResult{}, fmt.Errorf("in %s: %w", b.Name, err)
		}
	}
	result, err := s.changes.RevertBatch(ctx, b.ID, batchID)
	if errors.Is(err, domain.ErrChangeNotFound) {
		return b, batchID, domain.QueryResult{}, fmt.Errorf("in %s: %w with batch ID %s", b.Name, err, batchID)
	}
	if err != nil {
		return b, batchID, domain.QueryResult{}, fmt.Errorf("in %s: %w", b.Name, err)
	}
	return b, batchID, result, nil
}

// RevertEntry reverses the single change log entry entryID in the bdb ref names.
func (s *ChangeService) RevertEntry(
	ctx context.Context, ref string, entryID int64,
) (domain.Bdb, domain.QueryResult, error) {
	return s.reverse(ctx, ref, func(id uuid.UUID) (domain.QueryResult, error) {
		return s.changes.RevertEntry(ctx, id, entryID)
	})
}

// reverse resolves ref and runs op on the bdb, naming the bdb in its error.
func (s *ChangeService) reverse(
	ctx context.Context, ref string, op func(bdbID uuid.UUID) (domain.QueryResult, error),
) (domain.Bdb, domain.QueryResult, error) {
	b, err := s.bdbs.Resolve(ctx, ref)
	if err != nil {
		return domain.Bdb{}, domain.QueryResult{}, err
	}
	result, err := op(b.ID)
	if err != nil {
		return b, domain.QueryResult{}, fmt.Errorf("in %s: %w", b.Name, err)
	}
	return b, result, nil
}
