// Package intraprocessbdbs adapts the starters service's bdbs port to direct in-process calls. Starters has no
// service of its own — it ships inside the binary hosting the bdbs module — so this adapter is in-process in
// every deployment while sibling ports may go over HTTP. One flat intraprocess* package per target app keeps a
// binary compiling only the adapters it wires.
package intraprocessbdbs

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/appconv"
	bdbsdomain "github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	bdbsservice "github.com/alexandremgo/baguettedb/pkg/bdbs/service"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
)

// BdbsApp wraps the bdbs module's bdb service for in-process use.
type BdbsApp struct {
	bdbSvc *bdbsservice.BdbService
}

// New creates a new BdbsApp.
func New(bdbSvc *bdbsservice.BdbService) *BdbsApp {
	return &BdbsApp{bdbSvc: bdbSvc}
}

// CreateBdb creates a blank bdb and returns its ID.
func (a *BdbsApp) CreateBdb(
	ctx context.Context, userID uuid.UUID, roles []authz.Role, name, description string,
) (uuid.UUID, error) {
	bdb, err := a.bdbSvc.CreateBdb(ctx, userID, roles, name, description)
	if err != nil {
		return uuid.Nil, startersError(err)
	}
	return bdb.ID, nil
}

// CreateBdbWithProvenance creates a bdb with the starter provenance set as part of the initial create and
// returns the new bdb's ID.
func (a *BdbsApp) CreateBdbWithProvenance(
	ctx context.Context, userID uuid.UUID, roles []authz.Role,
	name, description, starterKey string, starterVersion int,
) (uuid.UUID, error) {
	bdb, err := a.bdbSvc.CreateBdb(ctx, userID, roles, name, description,
		bdbsdomain.WithStarterProvenance(starterKey, starterVersion))
	if err != nil {
		return uuid.Nil, startersError(err)
	}
	return bdb.ID, nil
}

// GetBdb returns the bdb as userID sees it, with its members' profiles resolved.
func (a *BdbsApp) GetBdb(ctx context.Context, userID, bdbID uuid.UUID) (apptypes.Bdb, error) {
	bdb, err := a.bdbSvc.GetBdb(ctx, userID, bdbID)
	if err != nil {
		return apptypes.Bdb{}, err
	}
	return appconv.Bdb(bdb, userID, a.bdbSvc.ResolveMemberProfiles(ctx, bdb)), nil
}

// DeleteBdb delegates to the bdbs module's bdb service.
func (a *BdbsApp) DeleteBdb(ctx context.Context, userID, bdbID uuid.UUID) error {
	return a.bdbSvc.DeleteBdb(ctx, userID, bdbID)
}

// startersError translates a bdb-creation failure of the bdbs app into the starters domain's error for it, wherever
// it sits in err's chain, so the starters app matches on its own errors. Any other error is returned unchanged.
func startersError(err error) error {
	if limitErr, ok := errors.AsType[bdbsdomain.BdbLimitReachedError](err); ok {
		return domain.BdbLimitReachedError{Max: limitErr.Max}
	}
	switch {
	case errors.Is(err, bdbsdomain.ErrBdbNameEmpty):
		return domain.ErrBdbNameEmpty
	case errors.Is(err, bdbsdomain.ErrBdbNameTooLong):
		return domain.ErrBdbNameTooLong
	case errors.Is(err, bdbsdomain.ErrBdbNameTaken):
		return domain.ErrBdbNameTaken
	}
	return err
}
