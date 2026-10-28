package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
)

// stubSource is an in-memory StarterSource serving a fixed manifest list.
type stubSource struct {
	manifests []domain.StarterManifest
	err       error
}

func (s *stubSource) List(_ context.Context) ([]domain.StarterManifest, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.manifests, nil
}

func (s *stubSource) Get(_ context.Context, key string) (domain.StarterManifest, error) {
	if s.err != nil {
		return domain.StarterManifest{}, s.err
	}
	for _, m := range s.manifests {
		if m.Key == key {
			return m, nil
		}
	}
	return domain.StarterManifest{}, fmt.Errorf("%w: %q", domain.ErrStarterNotFound, key)
}

// TestRegistryFirstSourceWins verifies registration-order precedence on both paths: List dedups by key keeping
// the first source's manifest, Get falls through sources in order.
func TestRegistryFirstSourceWins(t *testing.T) {
	first := &stubSource{manifests: []domain.StarterManifest{
		{Key: "shared", Version: 1, Name: "Official"},
		{Key: "only-first", Version: 1, Name: "Only first"},
	}}
	second := &stubSource{manifests: []domain.StarterManifest{
		{Key: "shared", Version: 9, Name: "Shadow attempt"},
		{Key: "only-second", Version: 1, Name: "Only second"},
	}}
	reg := NewRegistry(first, second)

	merged, err := reg.List(t.Context())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	wantKeys := []string{"shared", "only-first", "only-second"}
	if len(merged) != len(wantKeys) {
		t.Fatalf("List() returned %d manifests, want %d", len(merged), len(wantKeys))
	}
	for i, want := range wantKeys {
		if merged[i].Key != want {
			t.Errorf("List()[%d].Key = %q, want %q", i, merged[i].Key, want)
		}
	}
	if merged[0].Name != "Official" {
		t.Errorf("List() kept %q for duplicated key, want the first source's %q", merged[0].Name, "Official")
	}

	got, err := reg.Get(t.Context(), "shared")
	if err != nil {
		t.Fatalf("Get(shared) error = %v", err)
	}
	if got.Name != "Official" {
		t.Errorf("Get(shared).Name = %q, want %q (first source wins)", got.Name, "Official")
	}

	if _, err := reg.Get(t.Context(), "only-second"); err != nil {
		t.Errorf("Get(only-second) error = %v, want fallthrough to the second source", err)
	}
}

// TestRegistryUnknownKey verifies Get returns ErrStarterNotFound when no source exposes the key.
func TestRegistryUnknownKey(t *testing.T) {
	reg := NewRegistry(&stubSource{})
	if _, err := reg.Get(t.Context(), "ghost"); !errors.Is(err, domain.ErrStarterNotFound) {
		t.Errorf("Get(ghost) error = %v, want ErrStarterNotFound", err)
	}
}

// TestRegistryPropagatesSourceErrors verifies a failing source aborts List and Get with the wrapped error
// instead of being skipped.
func TestRegistryPropagatesSourceErrors(t *testing.T) {
	boom := errors.New("backend down")
	reg := NewRegistry(&stubSource{err: boom})

	if _, err := reg.List(t.Context()); !errors.Is(err, boom) {
		t.Errorf("List() error = %v, want wrapped %v", err, boom)
	}
	if _, err := reg.Get(t.Context(), "any"); !errors.Is(err, boom) {
		t.Errorf("Get() error = %v, want wrapped %v", err, boom)
	}
}
