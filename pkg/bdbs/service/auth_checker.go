package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// bdbMembershipReader reads bdb aggregates, individual memberships, and ownership listings.
type bdbMembershipReader interface {
	// FindByID returns the bdb with the given ID, including all its members.
	// Returns domain.ErrBdbNotFound if not found.
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Bdb, error)

	// FindMember returns the member for the given bdb and user.
	// Returns domain.ErrMemberNotFound if not found.
	FindMember(ctx context.Context, bdbID, userID uuid.UUID) (*domain.Member, error)

	// ListOwnedIDsByUser returns the IDs of every bdb the given user owns, including soft-deleted bdbs
	// still awaiting purge (their storage remains allocated until the purge removes it).
	ListOwnedIDsByUser(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error)
}

// dataAPIKeyFormulaChecker answers which formulas Data API keys may run, and forgets a formula that is gone.
type dataAPIKeyFormulaChecker interface {
	// KeyMayRunFormula reports whether the Data API key keyID of the bdb bdbID may run the formula formulaID, always
	// false on a bdb that doesn't exist or is soft-deleted.
	KeyMayRunFormula(ctx context.Context, bdbID, keyID uuid.UUID, formulaID string) (bool, error)

	// ForgetFormula forgets the formula formulaID for every Data API key of the bdb bdbID.
	ForgetFormula(ctx context.Context, bdbID uuid.UUID, formulaID string) error
}

// BdbAuthChecker resolves bdb membership to database permissions
// and provides bdb owner role lookups.
type BdbAuthChecker struct {
	repo        bdbMembershipReader
	authApp     AuthApp
	dataAPIKeys dataAPIKeyFormulaChecker
}

// NewBdbAuthChecker creates a new BdbAuthChecker.
func NewBdbAuthChecker(repo bdbMembershipReader, authApp AuthApp) *BdbAuthChecker {
	return &BdbAuthChecker{repo: repo, authApp: authApp}
}

// SetDataAPIKeys injects what answers for Data API keys. When left unset no key may run anything.
func (c *BdbAuthChecker) SetDataAPIKeys(keys dataAPIKeyFormulaChecker) {
	c.dataAPIKeys = keys
}

// CheckDataAPIKeyFormula reports whether the Data API key keyID of the bdb bdbID may run the formula formulaID, which
// no key may on a soft-deleted bdb. Whether the key's creator may run the formula is not checked here.
func (c *BdbAuthChecker) CheckDataAPIKeyFormula(
	ctx context.Context, keyID, bdbID uuid.UUID, formulaID string,
) (bool, error) {
	if c.dataAPIKeys == nil {
		return false, nil
	}
	return c.dataAPIKeys.KeyMayRunFormula(ctx, bdbID, keyID, formulaID)
}

// ForgetDataAPIKeyFormula forgets the formula formulaID for every Data API key of the bdb bdbID.
func (c *BdbAuthChecker) ForgetDataAPIKeyFormula(ctx context.Context, bdbID uuid.UUID, formulaID string) error {
	if c.dataAPIKeys == nil {
		return nil
	}
	return c.dataAPIKeys.ForgetFormula(ctx, bdbID, formulaID)
}

// CheckMemberPermission checks whether the user has the given database permission
// on the bdb's database, based on their bdb role.
func (c *BdbAuthChecker) CheckMemberPermission(
	ctx context.Context, userID, bdbID uuid.UUID, perm authz.DatabasePermission,
) (bool, error) {
	member, err := c.repo.FindMember(ctx, bdbID, userID)
	if err != nil {
		if errors.Is(err, domain.ErrMemberNotFound) {
			return false, nil
		}
		return false, err
	}
	return authz.BdbRoleHasDatabasePermission(member.Role, perm), nil
}

// GetOwnedBdbIDs returns the IDs of every bdb the given user owns, including soft-deleted bdbs still
// awaiting purge.
func (c *BdbAuthChecker) GetOwnedBdbIDs(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	ids, err := c.repo.ListOwnedIDsByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("listing owned bdb ids: %w", err)
	}
	return ids, nil
}

// GetBdbOwner returns the bdb owner's user ID and global roles.
func (c *BdbAuthChecker) GetBdbOwner(ctx context.Context, bdbID uuid.UUID) (uuid.UUID, []authz.Role, error) {
	bdb, err := c.repo.FindByID(ctx, bdbID)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("finding bdb: %w", err)
	}

	owner := bdb.Owner()
	if owner == nil {
		return uuid.Nil, nil, fmt.Errorf("bdb %s has no owner", bdbID)
	}

	roles, err := c.authApp.GetUserRoles(ctx, owner.UserID)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("getting owner roles: %w", err)
	}

	return owner.UserID, roles, nil
}
