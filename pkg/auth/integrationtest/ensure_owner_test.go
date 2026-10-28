//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/auth/service"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// TestEnsureOwner checks that EnsureOwner creates the owner account once, passwordless with the user and admin
// roles, returns that same account on every later call, even once its email changed, and adopts an existing account
// with the default email, with its roles as stored.
func TestEnsureOwner(t *testing.T) {
	ctx := context.Background()

	t.Run("creates the account once, then returns it", func(t *testing.T) {
		truncateAll(t)
		r := repos()

		owner, roles, err := service.NewOwnerService(r.ownerTx).EnsureOwner(ctx, "baker@bdb.localhost", "Baker")
		if err != nil {
			t.Fatalf("first EnsureOwner: %v", err)
		}
		id := owner.ID
		slices.Sort(roles)
		if want := []authz.Role{authz.RoleAdmin, authz.RoleUser}; !slices.Equal(roles, want) {
			t.Errorf("roles = %v, want %v", roles, want)
		}

		user, err := r.users.FindByEmail(ctx, "baker@bdb.localhost")
		if err != nil {
			t.Fatalf("finding the owner: %v", err)
		}
		if user.ID != id || user.FirstName != "Baker" || user.PasswordHash != nil || !user.IsActive {
			t.Errorf("owner = %+v, want id %s, first name Baker, no password, active", user, id)
		}

		again, againRoles, err := service.NewOwnerService(r.ownerTx).EnsureOwner(ctx, "baker@bdb.localhost", "Baker")
		if err != nil {
			t.Fatalf("second EnsureOwner: %v", err)
		}
		slices.Sort(againRoles)
		if again.ID != id || !slices.Equal(againRoles, roles) {
			t.Errorf("second call = %s %v, want %s %v", again.ID, againRoles, id, roles)
		}
	})

	t.Run("keeps the owner once its email changed", func(t *testing.T) {
		truncateAll(t)
		r := repos()
		owner, _, err := service.NewOwnerService(r.ownerTx).EnsureOwner(ctx, "baker@bdb.localhost", "Baker")
		if err != nil {
			t.Fatalf("first EnsureOwner: %v", err)
		}
		if err := r.users.UpdateIdentity(ctx, owner.ID, "ada@example.com", "Ada", "Lovelace"); err != nil {
			t.Fatalf("renaming the owner: %v", err)
		}

		again, _, err := service.NewOwnerService(r.ownerTx).EnsureOwner(ctx, "baker@bdb.localhost", "Baker")
		if err != nil {
			t.Fatalf("EnsureOwner after the rename: %v", err)
		}
		if again.ID != owner.ID || again.Email != "ada@example.com" {
			t.Errorf("EnsureOwner after the rename = %s %s, want %s ada@example.com", again.ID, again.Email, owner.ID)
		}
		if _, err := r.users.FindByEmail(ctx, "baker@bdb.localhost"); !errors.Is(err, domain.ErrUserNotFound) {
			t.Errorf("an account with the default email after the rename: %v, want none", err)
		}
	})

	t.Run("adopts an existing account as stored", func(t *testing.T) {
		truncateAll(t)
		seeded := testDB.SeedUser(t, "baker@bdb.localhost", []string{"user"})

		owner, roles, err := service.NewOwnerService(repos().ownerTx).EnsureOwner(ctx, "baker@bdb.localhost", "Baker")
		if err != nil {
			t.Fatalf("EnsureOwner: %v", err)
		}
		if owner.ID != seeded || !slices.Equal(roles, []authz.Role{authz.RoleUser}) {
			t.Errorf("EnsureOwner = %s %v, want the seeded %s [user]", owner.ID, roles, seeded)
		}
	})
}
