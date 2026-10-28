// Package embedded implements the starter source backed by manifest files compiled into the binary. The
// built-in starters ship and version with the binary, so the set never changes at run time.
package embedded

import (
	"cmp"
	"context"
	"embed"
	"fmt"
	"io/fs"
	"slices"

	"sigs.k8s.io/yaml"

	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
)

//go:embed builtin/*.yaml
var builtinFS embed.FS

// curatedOrder is the catalog order: the keys listed here lead, in this order, and any starter not named
// falls in behind them keeping the loader's lexical-by-filename order. Notes leads because it is the one to
// pick when you don't yet know what you want.
var curatedOrder = []string{
	"notes",
	"shopping-pantry",
	"recipes-meal-plan",
	"media-diary",
	"places",
	"workouts",
	"shared-expenses",
	"ai-memory",
}

// sortByCuratedOrder reorders manifests in place so the keys named in order come first, in that order,
// followed by every unnamed manifest in its existing relative order.
func sortByCuratedOrder(manifests []domain.StarterManifest, order []string) {
	rankByKey := make(map[string]int, len(order))
	for i, o := range order {
		rankByKey[o] = i
	}

	rank := func(key string) int {
		if o, ok := rankByKey[key]; ok {
			return o
		}
		return len(order)
	}

	slices.SortStableFunc(manifests, func(a, b domain.StarterManifest) int {
		return cmp.Compare(rank(a.Key), rank(b.Key))
	})
}

// Source serves the starters embedded in the binary. Manifests are decoded and validated once at construction.
type Source struct {
	manifests []domain.StarterManifest
	byKey     map[string]domain.StarterManifest
}

// NewSource decodes and validates the embedded manifests. Decoding is strict: an unknown or duplicate field in
// a manifest file is an authoring error, not data to skip silently.
func NewSource() (*Source, error) {
	return newSourceFromFS(builtinFS, "builtin/*.yaml")
}

// newSourceFromFS decodes every manifest matching pattern in fsys and validates the resulting set. Split from
// NewSource so tests can load fixture filesystems through the same path.
func newSourceFromFS(fsys fs.FS, pattern string) (*Source, error) {
	paths, err := fs.Glob(fsys, pattern)
	if err != nil {
		return nil, fmt.Errorf("globbing starter manifests: %w", err)
	}

	manifests := make([]domain.StarterManifest, 0, len(paths))
	for _, path := range paths {
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil, fmt.Errorf("reading starter manifest %s: %w", path, err)
		}
		var dto manifestDTO
		if err := yaml.UnmarshalStrict(data, &dto); err != nil {
			return nil, fmt.Errorf("decoding starter manifest %s: %w", path, err)
		}
		manifests = append(manifests, dto.toDomain())
	}

	if err := domain.ValidateManifestSet(manifests); err != nil {
		return nil, fmt.Errorf("validating starter manifests: %w", err)
	}

	sortByCuratedOrder(manifests, curatedOrder)

	byKey := make(map[string]domain.StarterManifest, len(manifests))
	for _, m := range manifests {
		byKey[m.Key] = m
	}
	return &Source{manifests: manifests, byKey: byKey}, nil
}

// List returns the embedded manifests in file order.
func (s *Source) List(_ context.Context) ([]domain.StarterManifest, error) {
	return slices.Clone(s.manifests), nil
}

// Get returns the embedded manifest with the given key, or domain.ErrStarterNotFound.
func (s *Source) Get(_ context.Context, key string) (domain.StarterManifest, error) {
	m, ok := s.byKey[key]
	if !ok {
		return domain.StarterManifest{}, fmt.Errorf("%w: %q", domain.ErrStarterNotFound, key)
	}
	return m, nil
}
