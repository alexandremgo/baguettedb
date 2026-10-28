package starters

import (
	"context"
	"slices"
	"testing"

	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/embedded"
	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
	"github.com/alexandremgo/baguettedb/pkg/starters/service"
)

// stubSource offers a fixed list of manifests.
type stubSource struct {
	manifests []domain.StarterManifest
}

func (s stubSource) List(context.Context) ([]domain.StarterManifest, error) {
	return s.manifests, nil
}

func (s stubSource) Get(_ context.Context, key string) (domain.StarterManifest, error) {
	for _, m := range s.manifests {
		if m.Key == key {
			return m, nil
		}
	}
	return domain.StarterManifest{}, domain.ErrStarterNotFound
}

// TestSetup_CatalogPrecedence checks that the catalog lists the built-in starters first, then those of
// SetupOptions.Sources, and that a source offering a built-in starter's key does not replace it.
func TestSetup_CatalogPrecedence(t *testing.T) {
	builtin, err := embedded.NewSource()
	if err != nil {
		t.Fatalf("loading built-in starters: %v", err)
	}
	builtins, err := builtin.List(t.Context())
	if err != nil || len(builtins) == 0 {
		t.Fatalf("listing built-in starters = (%d, %v), want some", len(builtins), err)
	}
	shadowed := builtins[0]
	extra := stubSource{manifests: []domain.StarterManifest{
		{Key: shadowed.Key, Name: "Shadow"},
		{Key: "extra-only", Name: "Extra"},
	}}

	res, err := Setup(config.Config{}, zerolog.Nop(), nil, nil, Host{},
		SetupOptions{Sources: []service.StarterSource{extra}})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	catalog, err := res.StarterSvc.ListStarters(t.Context())
	if err != nil {
		t.Fatalf("ListStarters: %v", err)
	}

	var keys, wantKeys []string
	for _, m := range catalog {
		keys = append(keys, m.Key)
	}
	for _, m := range builtins {
		wantKeys = append(wantKeys, m.Key)
	}
	wantKeys = append(wantKeys, "extra-only")
	if !slices.Equal(keys, wantKeys) {
		t.Errorf("catalog keys = %v, want the built-ins then extra-only: %v", keys, wantKeys)
	}
	if catalog[0].Name != shadowed.Name {
		t.Errorf("catalog[0] = %q named %q, want the built-in %q", catalog[0].Key, catalog[0].Name, shadowed.Name)
	}
}
