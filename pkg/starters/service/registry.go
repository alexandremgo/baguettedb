// Package service composes the starters catalog: sources are merged through a registry, and the apply engine
// seeding new bdbs from manifests builds on top of it.
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
)

// Registry merges several sources into one catalog. Sources are consulted in registration order and the first
// source exposing a key wins, so a later source can never shadow an earlier one's starter.
type Registry struct {
	sources []StarterSource
}

// NewRegistry builds a registry over the given sources, in precedence order.
func NewRegistry(sources ...StarterSource) *Registry {
	return &Registry{sources: sources}
}

// List returns the merged catalog, deduplicated by key with first-source precedence. Within one source, its own
// order is preserved.
func (r *Registry) List(ctx context.Context) ([]domain.StarterManifest, error) {
	seen := make(map[string]struct{})
	var merged []domain.StarterManifest
	for _, src := range r.sources {
		manifests, err := src.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing starters: %w", err)
		}
		for _, m := range manifests {
			if _, dup := seen[m.Key]; dup {
				continue
			}
			seen[m.Key] = struct{}{}
			merged = append(merged, m)
		}
	}
	return merged, nil
}

// Get returns the manifest for key from the first source exposing it, or domain.ErrStarterNotFound.
func (r *Registry) Get(ctx context.Context, key string) (domain.StarterManifest, error) {
	for _, src := range r.sources {
		m, err := src.Get(ctx, key)
		if errors.Is(err, domain.ErrStarterNotFound) {
			continue
		}
		if err != nil {
			return domain.StarterManifest{}, fmt.Errorf("getting starter %q: %w", key, err)
		}
		return m, nil
	}
	return domain.StarterManifest{}, fmt.Errorf("%w: %q", domain.ErrStarterNotFound, key)
}
