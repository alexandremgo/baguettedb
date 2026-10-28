package intraprocess

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
	startersdomain "github.com/alexandremgo/baguettedb/pkg/starters/domain"
	startersservice "github.com/alexandremgo/baguettedb/pkg/starters/service"
)

// StartersApp adapts the starters app's service, acting as one account, and converts its manifests into
// domain.Starter.
type StartersApp struct {
	starterSvc *startersservice.StarterService
	account    Account
}

// NewStartersApp creates a StartersApp acting as account.
func NewStartersApp(starterSvc *startersservice.StarterService, account Account) *StartersApp {
	return &StartersApp{starterSvc: starterSvc, account: account}
}

// ListStarters returns every starter in the catalog.
func (a *StartersApp) ListStarters(ctx context.Context) ([]domain.Starter, error) {
	ctx = a.account.bind(ctx)
	manifests, err := a.starterSvc.ListStarters(ctx)
	if err != nil {
		return nil, err
	}
	starters := make([]domain.Starter, len(manifests))
	for i, m := range manifests {
		starters[i] = domain.Starter{Key: m.Key, Name: m.Name, Description: m.Description, Tags: m.Tags}
	}
	return starters, nil
}

// CreateFromStarter creates a bdb owned by the account, seeded from the starter starterKey, and returns its ID. An
// unknown key wraps domain.ErrStarterNotFound.
func (a *StartersApp) CreateFromStarter(ctx context.Context, starterKey, name, description string) (uuid.UUID, error) {
	ctx = a.account.bind(ctx)
	id, err := a.starterSvc.CreateFromStarter(ctx, a.account.ID, a.account.Roles, starterKey, name, description)
	if errors.Is(err, startersdomain.ErrStarterNotFound) {
		return uuid.Nil, fmt.Errorf("%w: %q", domain.ErrStarterNotFound, starterKey)
	}
	if errors.Is(err, startersdomain.ErrBdbNameTaken) {
		return uuid.Nil, domain.ErrBdbNameTaken
	}
	return id, err
}
