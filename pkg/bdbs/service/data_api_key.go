package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
)

// dataAPIKeyFormulaRepository stores the formulas each Data API key of a bdb may run.
type dataAPIKeyFormulaRepository interface {
	// AddFormulas records that the key keyID of the bdb bdbID may run each of formulaIDs, as of at. A formula the key
	// already has is left as is.
	AddFormulas(ctx context.Context, bdbID, keyID uuid.UUID, formulaIDs []string, at time.Time) error

	// RemoveFormula forgets that the key keyID of the bdb bdbID may run formulaID. A formula the key doesn't have is
	// not an error.
	RemoveFormula(ctx context.Context, bdbID, keyID uuid.UUID, formulaID string) error

	// ListByBdb returns the formula IDs of each key of the bdb bdbID that has any, keyed by key ID, each key's in the
	// order they were added.
	ListByBdb(ctx context.Context, bdbID uuid.UUID) (map[uuid.UUID][]string, error)

	// ListByKey returns the formula IDs of the key keyID of the bdb bdbID, in the order they were added.
	ListByKey(ctx context.Context, bdbID, keyID uuid.UUID) ([]string, error)

	// HasFormula reports whether the key keyID of the bdb bdbID may run formulaID.
	HasFormula(ctx context.Context, bdbID, keyID uuid.UUID, formulaID string) (bool, error)

	// DeleteByFormula forgets formulaID for every key of the bdb bdbID.
	DeleteByFormula(ctx context.Context, bdbID uuid.UUID, formulaID string) error

	// DeleteByKey forgets every formula of the key keyID of the bdb bdbID.
	DeleteByKey(ctx context.Context, bdbID, keyID uuid.UUID) error

	// DeleteByBdb forgets every formula of every key of the bdb bdbID.
	DeleteByBdb(ctx context.Context, bdbID uuid.UUID) error
}

// formulaLister lists the formulas of a bdb.
type formulaLister interface {
	// ListFormulas returns the bdb's formulas the user may see, hidden ones included for a member who may write them.
	ListFormulas(ctx context.Context, userID, bdbID uuid.UUID) ([]apptypes.Formula, error)
}

// DataAPIKey is a Data API key of a bdb, never its value, with the IDs of the formulas it may run.
type DataAPIKey struct {
	Key        apptypes.DataAPIKey
	FormulaIDs []string
}

// DataAPIKeyService manages the Data API keys of a bdb, which let an app of its owner's run the formulas picked for
// each key, and which formulas those are. Only a bdb's owner manages its keys. The auth app issues, checks and
// revokes the keys; the formulas each may run are stored here.
type DataAPIKeyService struct {
	bdbs     bdbFinder
	formulas dataAPIKeyFormulaRepository
	authApp  AuthApp
	dbApp    formulaLister
	logger   zerolog.Logger
}

// NewDataAPIKeyService creates a DataAPIKeyService reading bdbs from bdbs, storing each key's formulas in formulas,
// and issuing and revoking keys through authApp.
func NewDataAPIKeyService(
	bdbs bdbFinder, formulas dataAPIKeyFormulaRepository, authApp AuthApp, logger zerolog.Logger,
) *DataAPIKeyService {
	return &DataAPIKeyService{bdbs: bdbs, formulas: formulas, authApp: authApp, logger: logger}
}

// SetDatabasesApp injects the databases module's formula listing after construction, since the databases module is
// set up after bdbs.
func (s *DataAPIKeyService) SetDatabasesApp(app formulaLister) {
	s.dbApp = app
}

// ListKeys returns the bdb bdbID's Data API keys that are not revoked, newest first, each with the formulas it may run.
// Only the owner userID may list them: anyone else is a domain.ErrNotBdbMember, and a bdb that doesn't exist a
// domain.ErrBdbNotFound.
func (s *DataAPIKeyService) ListKeys(ctx context.Context, userID, bdbID uuid.UUID) ([]DataAPIKey, error) {
	if _, err := s.ownedBdb(ctx, userID, bdbID); err != nil {
		return nil, err
	}

	keys, err := s.authApp.ListDataAPIKeys(ctx, bdbID)
	if err != nil {
		return nil, fmt.Errorf("listing the bdb's data API keys: %w", err)
	}
	formulas, err := s.formulas.ListByBdb(ctx, bdbID)
	if err != nil {
		return nil, fmt.Errorf("listing the data API keys' formulas: %w", err)
	}

	listed := make([]DataAPIKey, len(keys))
	for i, key := range keys {
		listed[i] = DataAPIKey{Key: key, FormulaIDs: formulas[key.ID]}
	}
	return listed, nil
}

// CreateKey creates a Data API key named name for the bdb bdbID, acting as its owner userID and allowed to run the
// formulas formulaIDs, which may be none, and returns it with its plain value, which can't be read again.
//
// Only the owner may create one, anyone else being a domain.ErrNotBdbMember, and not on a soft-deleted bdb, a
// domain.ErrBdbAlreadyDeleted. A formula ID the bdb doesn't have is a domain.ErrFormulaNotInBdb, a name that is empty
// or too long a domain.ErrInvalidDataAPIKeyName.
//
// The auth app creates the key and its formulas are stored here, so the two don't share a transaction: when storing
// the formulas fails, the key is revoked again, and a failure to revoke it is only logged, since a key with no
// formula can run nothing.
func (s *DataAPIKeyService) CreateKey(
	ctx context.Context, userID, bdbID uuid.UUID, name string, formulaIDs []string,
) (DataAPIKey, string, error) {
	if _, err := s.liveOwnedBdb(ctx, userID, bdbID); err != nil {
		return DataAPIKey{}, "", err
	}
	formulaIDs = distinct(formulaIDs)
	if err := s.checkFormulas(ctx, userID, bdbID, formulaIDs); err != nil {
		return DataAPIKey{}, "", err
	}

	created, err := s.authApp.CreateDataAPIKey(ctx, userID, bdbID, name)
	if err != nil {
		return DataAPIKey{}, "", err
	}
	if err := s.formulas.AddFormulas(ctx, bdbID, created.Key.ID, formulaIDs, time.Now().UTC()); err != nil {
		if revokeErr := s.authApp.RevokeDataAPIKey(ctx, bdbID, created.Key.ID); revokeErr != nil {
			s.logger.Error().Err(revokeErr).Str("bdb_id", bdbID.String()).Str("key_id", created.Key.ID.String()).
				Msg("failed to revoke a data API key whose formulas could not be stored")
		}
		return DataAPIKey{}, "", fmt.Errorf("storing the data API key's formulas: %w", err)
	}

	s.logger.Info().Str("bdb_id", bdbID.String()).Str("key_id", created.Key.ID.String()).
		Int("formulas", len(formulaIDs)).Msg("Created data API key")
	return DataAPIKey{Key: created.Key, FormulaIDs: formulaIDs}, created.Value, nil
}

// AddFormula lets the Data API key keyID of the bdb bdbID run the formula formulaID too; a formula the key already has
// is not an error. The key keeps its value.
//
// Only the owner userID may add one, anyone else being a domain.ErrNotBdbMember, and not on a soft-deleted bdb, a
// domain.ErrBdbAlreadyDeleted. A key the bdb doesn't have is a domain.ErrDataAPIKeyNotFound, a formula it doesn't
// have a domain.ErrFormulaNotInBdb.
func (s *DataAPIKeyService) AddFormula(ctx context.Context, userID, bdbID, keyID uuid.UUID, formulaID string) error {
	if _, err := s.liveOwnedBdb(ctx, userID, bdbID); err != nil {
		return err
	}
	if err := s.requireKey(ctx, bdbID, keyID); err != nil {
		return err
	}
	if err := s.checkFormulas(ctx, userID, bdbID, []string{formulaID}); err != nil {
		return err
	}
	if err := s.formulas.AddFormulas(ctx, bdbID, keyID, []string{formulaID}, time.Now().UTC()); err != nil {
		return fmt.Errorf("adding a formula to the data API key: %w", err)
	}
	return nil
}

// RemoveFormula stops the Data API key keyID of the bdb bdbID from running the formula formulaID; a formula the key
// doesn't have is not an error. Only the owner userID may remove one: anyone else is a domain.ErrNotBdbMember.
func (s *DataAPIKeyService) RemoveFormula(
	ctx context.Context, userID, bdbID, keyID uuid.UUID, formulaID string,
) error {
	if _, err := s.ownedBdb(ctx, userID, bdbID); err != nil {
		return err
	}
	if err := s.formulas.RemoveFormula(ctx, bdbID, keyID, formulaID); err != nil {
		return fmt.Errorf("removing a formula from the data API key: %w", err)
	}
	return nil
}

// RevokeKey revokes the Data API key keyID of the bdb bdbID, which stops authenticating at once, and forgets its
// formulas. Only the owner userID may revoke one, anyone else being a domain.ErrNotBdbMember; a key the bdb doesn't
// have, or one already revoked, is a domain.ErrDataAPIKeyNotFound.
//
// A failure to forget the formulas once the key is revoked is only logged: a revoked key is never listed nor
// accepted, so the formulas left behind can't show or be run.
func (s *DataAPIKeyService) RevokeKey(ctx context.Context, userID, bdbID, keyID uuid.UUID) error {
	if _, err := s.ownedBdb(ctx, userID, bdbID); err != nil {
		return err
	}
	if err := s.authApp.RevokeDataAPIKey(ctx, bdbID, keyID); err != nil {
		return err
	}
	if err := s.formulas.DeleteByKey(ctx, bdbID, keyID); err != nil {
		s.logger.Error().Err(err).Str("bdb_id", bdbID.String()).Str("key_id", keyID.String()).
			Msg("failed to forget a revoked data API key's formulas")
	}
	return nil
}

// KeyMayRunFormula reports whether the Data API key keyID of the bdb bdbID may run the formula formulaID: it must be
// among the key's formulas, and the bdb must exist and not be soft-deleted, whatever formulas a key still has there.
// Whether the key's creator may run the formula is not checked here.
func (s *DataAPIKeyService) KeyMayRunFormula(
	ctx context.Context, bdbID, keyID uuid.UUID, formulaID string,
) (bool, error) {
	bdb, err := s.bdbs.FindByID(ctx, bdbID)
	if errors.Is(err, domain.ErrBdbNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("finding bdb: %w", err)
	}
	if bdb.IsSoftDeleted() {
		return false, nil
	}
	found, err := s.formulas.HasFormula(ctx, bdbID, keyID, formulaID)
	if err != nil {
		return false, fmt.Errorf("checking the data API key's formula: %w", err)
	}
	return found, nil
}

// KeyFormulaIDs returns the IDs of the formulas the Data API key keyID of the bdb bdbID may run, in the order they were
// added: none on a bdb that doesn't exist or is soft-deleted, whatever formulas a key still has there.
func (s *DataAPIKeyService) KeyFormulaIDs(ctx context.Context, bdbID, keyID uuid.UUID) ([]string, error) {
	bdb, err := s.bdbs.FindByID(ctx, bdbID)
	if errors.Is(err, domain.ErrBdbNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("finding bdb: %w", err)
	}
	if bdb.IsSoftDeleted() {
		return nil, nil
	}
	formulaIDs, err := s.formulas.ListByKey(ctx, bdbID, keyID)
	if err != nil {
		return nil, fmt.Errorf("listing the data API key's formulas: %w", err)
	}
	return formulaIDs, nil
}

// ForgetFormula forgets the formula formulaID for every Data API key of the bdb bdbID, the formula being gone.
func (s *DataAPIKeyService) ForgetFormula(ctx context.Context, bdbID uuid.UUID, formulaID string) error {
	if err := s.formulas.DeleteByFormula(ctx, bdbID, formulaID); err != nil {
		return fmt.Errorf("forgetting a formula of the data API keys: %w", err)
	}
	return nil
}

// RevokeBdbKeys revokes every Data API key of the bdb bdbID and forgets their formulas.
func (s *DataAPIKeyService) RevokeBdbKeys(ctx context.Context, bdbID uuid.UUID) error {
	if err := s.authApp.RevokeDataAPIKeys(ctx, bdbID); err != nil {
		return fmt.Errorf("revoking the bdb's data API keys: %w", err)
	}
	if err := s.formulas.DeleteByBdb(ctx, bdbID); err != nil {
		return fmt.Errorf("forgetting the bdb's data API key formulas: %w", err)
	}
	return nil
}

// RevokeMemberKeys revokes the Data API keys the user userID created for the bdb bdbID. Their formulas are left in
// place: a revoked key is never listed nor accepted, so they can't show or be run.
func (s *DataAPIKeyService) RevokeMemberKeys(ctx context.Context, bdbID, userID uuid.UUID) error {
	if err := s.authApp.RevokeMemberDataAPIKeys(ctx, bdbID, userID); err != nil {
		return fmt.Errorf("revoking a member's data API keys: %w", err)
	}
	return nil
}

// ownedBdb returns the bdb bdbID when userID owns it. A bdb that doesn't exist is a domain.ErrBdbNotFound, a user
// other than its owner a domain.ErrNotBdbMember.
func (s *DataAPIKeyService) ownedBdb(ctx context.Context, userID, bdbID uuid.UUID) (*domain.Bdb, error) {
	bdb, err := s.bdbs.FindByID(ctx, bdbID)
	if err != nil {
		return nil, fmt.Errorf("finding bdb: %w", err)
	}
	if owner := bdb.Owner(); owner == nil || owner.UserID != userID {
		return nil, domain.ErrNotBdbMember
	}
	return bdb, nil
}

// liveOwnedBdb returns the bdb bdbID as ownedBdb does, and a domain.ErrBdbAlreadyDeleted when it is soft-deleted.
func (s *DataAPIKeyService) liveOwnedBdb(ctx context.Context, userID, bdbID uuid.UUID) (*domain.Bdb, error) {
	bdb, err := s.ownedBdb(ctx, userID, bdbID)
	if err != nil {
		return nil, err
	}
	if bdb.IsSoftDeleted() {
		return nil, domain.ErrBdbAlreadyDeleted
	}
	return bdb, nil
}

// requireKey returns a domain.ErrDataAPIKeyNotFound unless keyID is one of the bdb bdbID's keys that are not revoked.
func (s *DataAPIKeyService) requireKey(ctx context.Context, bdbID, keyID uuid.UUID) error {
	keys, err := s.authApp.ListDataAPIKeys(ctx, bdbID)
	if err != nil {
		return fmt.Errorf("listing the bdb's data API keys: %w", err)
	}
	if !slices.ContainsFunc(keys, func(k apptypes.DataAPIKey) bool { return k.ID == keyID }) {
		return domain.ErrDataAPIKeyNotFound
	}
	return nil
}

// checkFormulas returns a domain.ErrFormulaNotInBdb, wrapped with the ID, for the first of ids that names none of the
// bdb bdbID's formulas, as its owner userID sees them. No ID needs no lookup.
func (s *DataAPIKeyService) checkFormulas(ctx context.Context, userID, bdbID uuid.UUID, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if s.dbApp == nil {
		return errors.New("checking formulas: the databases module is not wired")
	}
	known, err := s.dbApp.ListFormulas(ctx, userID, bdbID)
	if err != nil {
		return fmt.Errorf("listing the bdb's formulas: %w", err)
	}
	return formulasInBdb(known, ids)
}

// formulasInBdb returns a domain.ErrFormulaNotInBdb, wrapped with the ID, for the first of ids that is the ID of none
// of known, and nil when every one is.
func formulasInBdb(known []apptypes.Formula, ids []string) error {
	knownIDs := make(map[string]struct{}, len(known))
	for _, f := range known {
		knownIDs[f.ID] = struct{}{}
	}
	for _, id := range ids {
		if _, found := knownIDs[id]; !found {
			return fmt.Errorf("%w: %q", domain.ErrFormulaNotInBdb, id)
		}
	}
	return nil
}

// distinct returns ids without repeats, keeping the first of each in place.
func distinct(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	kept := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			kept = append(kept, id)
		}
	}
	return kept
}
