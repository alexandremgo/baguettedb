package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// RunInTx runs fn in one database transaction, with the repositories of T bound to it: the transaction is committed
// when fn returns nil and rolled back otherwise. Each transactional service names the repositories it works through
// in its own T. Exported so that the app's setup can build one for each storage backend.
type RunInTx[T any] func(ctx context.Context, fn func(T) error) error

// AuthApp defines the subset of the auth app needed by bdbs.
// Exported so that setup.go can accept it as a parameter.
type AuthApp interface {
	// GetUserRoles returns all global roles assigned to the given user.
	GetUserRoles(ctx context.Context, userID uuid.UUID) ([]authz.Role, error)

	// GetUserProfiles returns the profile of each user matching the given IDs,
	// keyed by user ID. Unknown IDs are silently omitted. An empty input
	// returns an empty map without hitting the underlying store.
	GetUserProfiles(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]*apptypes.UserProfile, error)
}

// DatabasesApp defines the subset of the databases app needed by bdbs.
// Exported so that setup.go can accept it through SetupResult.SetDatabasesApp.
type DatabasesApp interface {
	// ListTables returns the lightweight {name, type} list of tables for the given bdb.
	ListTables(ctx context.Context, userID, bdbID uuid.UUID) ([]apptypes.TableInfo, error)

	// ListTablesWithSchemas returns all tables with their full schema for the given bdb.
	ListTablesWithSchemas(ctx context.Context, userID, bdbID uuid.UUID) ([]apptypes.TableSchema, error)

	// ListSkills returns all skills in the given bdb, each with its full
	// body and target tables.
	ListSkills(ctx context.Context, userID, bdbID uuid.UUID) ([]apptypes.Skill, error)

	// ListFormulas returns the bdb's formulas as lightweight summaries (no SQL
	// or parameter schema), ordered for display.
	ListFormulas(ctx context.Context, userID, bdbID uuid.UUID) ([]apptypes.Formula, error)

	// DeleteDatabase removes the per-bdb SQLite database (file + record).
	// Idempotent: returns nil if no database exists for the bdb.
	DeleteDatabase(ctx context.Context, bdbID uuid.UUID) error
}
