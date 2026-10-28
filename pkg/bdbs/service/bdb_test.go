package service

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
)

func TestCreateBdb_ResolvesOwnerCap(t *testing.T) {
	// An explicit resolver keeps the test about the service (it passes the resolved cap down), not about
	// quota.Fixed's role handling.
	limits := quota.Resolver(func(roles []authz.Role) quota.Limits {
		if slices.Contains(roles, authz.RoleAdmin) {
			return quota.Limits{}
		}
		return quota.Limits{MaxBdbs: 3}
	})

	tests := []struct {
		name         string
		roles        []authz.Role
		wantMaxOwned int
	}{
		{"user gets the configured cap", []authz.Role{authz.RoleUser}, 3},
		{"admin gets no cap", []authz.Role{authz.RoleUser, authz.RoleAdmin}, 0},
		{"no roles gets the configured cap", []authz.Role{}, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &mockBdbRepo{}
			svc := NewBdbService(repo, nil, &mockAuthApp{}, limits, zerolog.Nop())

			_, err := svc.CreateBdb(context.Background(), uuid.New(), tt.roles, "test", "")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if repo.gotMaxOwned != tt.wantMaxOwned {
				t.Errorf("CreateOwned received maxOwned=%d, want %d", repo.gotMaxOwned, tt.wantMaxOwned)
			}
		})
	}
}

func TestCreateBdb_PropagatesLimitError(t *testing.T) {
	repo := &mockBdbRepo{err: domain.BdbLimitReachedError{Max: 3}}
	svc := NewBdbService(repo, nil, &mockAuthApp{}, quota.Fixed(quota.Limits{MaxBdbs: 3}), zerolog.Nop())

	_, err := svc.CreateBdb(context.Background(), uuid.New(), []authz.Role{authz.RoleUser}, "test", "")

	if limitErr, ok := errors.AsType[domain.BdbLimitReachedError](err); !ok || limitErr.Max != 3 {
		t.Fatalf("expected BdbLimitReachedError{Max: 3}, got %v", err)
	}
}
