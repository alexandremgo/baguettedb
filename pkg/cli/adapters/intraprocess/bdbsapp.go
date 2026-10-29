// Package intraprocess adapts the command line's ports to the backend apps' services running in the same process,
// each adapter bound to one account and converting the services' results into the command line's own shapes.
package intraprocess

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	bdbsdomain "github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	bdbsservice "github.com/alexandremgo/baguettedb/pkg/bdbs/service"
	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// Account is the account an adapter acts as.
type Account struct {
	ID    uuid.UUID
	Roles []authz.Role
}

// bind returns ctx carrying the account's identity, as an authenticated request would: the apps read the caller's
// roles from it to resolve role-based limits, such as an admin's exemption from export limits.
func (a Account) bind(ctx context.Context) context.Context {
	return authz.ContextWithIdentity(ctx, a.ID.String(), a.Roles)
}

// BdbsApp adapts the bdbs app's bdb service, acting as one account, and converts its bdbs into domain.Bdb.
type BdbsApp struct {
	bdbSvc  *bdbsservice.BdbService
	account Account
}

// NewBdbsApp creates a BdbsApp acting as account.
func NewBdbsApp(bdbSvc *bdbsservice.BdbService, account Account) *BdbsApp {
	return &BdbsApp{bdbSvc: bdbSvc, account: account}
}

// ListBdbs returns the bdbs the account belongs to, soft-deleted ones excluded, with its role in each. The bdbs app
// lists an owner's soft-deleted bdbs too, for a trash to restore them from, which the command line has not.
func (a *BdbsApp) ListBdbs(ctx context.Context) ([]domain.Bdb, error) {
	ctx = a.account.bind(ctx)
	bdbs, err := a.bdbSvc.ListUserBdbs(ctx, a.account.ID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Bdb, 0, len(bdbs))
	for _, b := range bdbs {
		if b.IsSoftDeleted() {
			continue
		}
		converted, err := a.toBdb(b)
		if err != nil {
			return nil, err
		}
		out = append(out, converted)
	}
	return out, nil
}

// GetBdb returns the bdb bdbID, with the account's role in it.
func (a *BdbsApp) GetBdb(ctx context.Context, bdbID uuid.UUID) (domain.Bdb, error) {
	ctx = a.account.bind(ctx)
	b, err := a.bdbSvc.GetBdb(ctx, a.account.ID, bdbID)
	if err != nil {
		return domain.Bdb{}, err
	}
	return a.toBdb(b)
}

// CreateBdb creates a blank bdb owned by the account and returns it.
func (a *BdbsApp) CreateBdb(ctx context.Context, name, description string) (domain.Bdb, error) {
	ctx = a.account.bind(ctx)
	b, err := a.bdbSvc.CreateBdb(ctx, a.account.ID, a.account.Roles, name, description)
	if errors.Is(err, bdbsdomain.ErrBdbNameTaken) {
		return domain.Bdb{}, domain.ErrBdbNameTaken
	}
	if err != nil {
		return domain.Bdb{}, err
	}
	return a.toBdb(b)
}

// UpdateBdb changes the name, the description, or both, of the bdb bdbID, which the account owns, and returns it; a
// nil field is left as it is.
func (a *BdbsApp) UpdateBdb(ctx context.Context, bdbID uuid.UUID, name, description *string) (domain.Bdb, error) {
	ctx = a.account.bind(ctx)
	b, err := a.bdbSvc.UpdateBdb(ctx, a.account.ID, bdbID, name, description)
	switch {
	case errors.Is(err, bdbsdomain.ErrBdbNameTaken):
		return domain.Bdb{}, domain.ErrBdbNameTaken
	case errors.Is(err, bdbsdomain.ErrNotBdbMember):
		return domain.Bdb{}, domain.ErrNotBdbOwner
	case err != nil:
		return domain.Bdb{}, err
	}
	return a.toBdb(b)
}

// DeleteBdb soft-deletes the bdb bdbID, owned by the account, which can restore it during the bdbs app's grace period.
func (a *BdbsApp) DeleteBdb(ctx context.Context, bdbID uuid.UUID) error {
	return a.bdbSvc.DeleteBdb(a.account.bind(ctx), a.account.ID, bdbID)
}

// toBdb converts b, taking the role from the account's membership. A bdb the account is not a member of is an
// error: the bdbs service returns only bdbs the account belongs to.
func (a *BdbsApp) toBdb(b *bdbsdomain.Bdb) (domain.Bdb, error) {
	member := b.FindMember(a.account.ID)
	if member == nil {
		return domain.Bdb{}, fmt.Errorf("bdb %s has no membership for account %s", b.ID, a.account.ID)
	}
	return domain.Bdb{ID: b.ID, Name: b.Name, Description: b.Description, Role: string(member.Role)}, nil
}
