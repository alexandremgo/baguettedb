package authhttp

import (
	"context"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// DataAPIKeyIdentity is what a Data API key authenticates: the user who created it and whom it acts as, with their
// roles, the key itself, and the bdb it is bound to.
type DataAPIKeyIdentity struct {
	UserID uuid.UUID
	Roles  []authz.Role
	KeyID  uuid.UUID
	BdbID  uuid.UUID
}

// DataAPIKeyChecker returns what the Data API key key authenticates: ErrInvalidToken for a value that authenticates
// nothing, and any other error for a failure to check it.
type DataAPIKeyChecker func(ctx context.Context, key string) (DataAPIKeyIdentity, error)
