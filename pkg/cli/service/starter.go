package service

import (
	"context"
	"fmt"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// starterCatalog reads the starters app's catalog.
type starterCatalog interface {
	// ListStarters returns every starter a bdb can be created from.
	ListStarters(ctx context.Context) ([]domain.Starter, error)
}

// StarterService runs the starter use cases.
type StarterService struct {
	catalog starterCatalog
}

// NewStarterService creates a StarterService over the starters app.
func NewStarterService(catalog starterCatalog) *StarterService {
	return &StarterService{catalog: catalog}
}

// List returns the starters a bdb can be created from.
func (s *StarterService) List(ctx context.Context) ([]domain.Starter, error) {
	starters, err := s.catalog.ListStarters(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing starters: %w", err)
	}
	return starters, nil
}
