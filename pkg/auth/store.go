package auth

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/auth/adapters/postgres"
	authsqlite "github.com/alexandremgo/baguettedb/pkg/auth/adapters/sqlite"
	authmigrations "github.com/alexandremgo/baguettedb/pkg/auth/migrations"
	authsqlitemigrations "github.com/alexandremgo/baguettedb/pkg/auth/migrations/sqlite"
	"github.com/alexandremgo/baguettedb/pkg/auth/service"
	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
)

// store holds the auth repositories over the instance store the config selects: Postgres, or the SQLite file at
// cfg.InstanceSQLitePath when it is set.
type store struct {
	// pool is the Postgres pool, nil on SQLite.
	pool       *pgxpool.Pool
	users      service.UserRepository
	roles      service.RoleRepository
	oidcStates service.OIDCStateRepository
	// userTx, identityTx and ownerTx run a transaction over the repositories of the service of the same name.
	userTx     service.RunInTx[service.UserTx]
	identityTx service.RunInTx[service.IdentityTx]
	ownerTx    service.RunInTx[service.OwnerTx]
	close      func()

	// tokenSvc is the access token service over the store's access token repository.
	tokenSvc *service.TokenService
}

// openStore migrates the selected instance store and opens the auth repositories on it. close releases the
// connection.
func openStore(ctx context.Context, cfg config.Config, logger zerolog.Logger) (*store, error) {
	if cfg.InstanceSQLitePath != "" {
		return openSQLiteStore(ctx, cfg.InstanceSQLitePath, logger)
	}

	if err := database.RunMigrations(cfg.AuthDatabaseDSN, authmigrations.FS, "auth", logger); err != nil {
		return nil, fmt.Errorf("auth: running migrations: %w", err)
	}
	pool, err := database.NewPostgresPool(ctx, cfg.AuthDatabaseDSN)
	if err != nil {
		return nil, fmt.Errorf("auth: connecting to postgres: %w", err)
	}
	users, roles := postgres.NewUserRepo(pool), postgres.NewRoleRepo(pool)
	return &store{
		pool:       pool,
		users:      users,
		roles:      roles,
		oidcStates: postgres.NewOIDCStateRepo(pool),
		userTx: database.PostgresInTx(pool, func(tx pgx.Tx) service.UserTx {
			return service.UserTx{
				Users:    postgres.NewUserRepo(tx),
				Roles:    postgres.NewRoleRepo(tx),
				Sessions: postgres.NewAccessTokenRepo(tx),
			}
		}),
		identityTx: database.PostgresInTx(pool, func(tx pgx.Tx) service.IdentityTx {
			return service.IdentityTx{
				Identities: postgres.NewExternalIdentityRepo(tx),
				Users:      postgres.NewUserRepo(tx),
			}
		}),
		ownerTx: database.PostgresInTx(pool, func(tx pgx.Tx) service.OwnerTx {
			return service.OwnerTx{
				Owner: postgres.NewInstanceOwnerRepo(tx),
				Users: postgres.NewUserRepo(tx),
				Roles: postgres.NewRoleRepo(tx),
			}
		}),
		close: pool.Close,
		tokenSvc: service.NewTokenService(postgres.NewAccessTokenRepo(pool), users,
			service.NewAuthenticationService(users)),
	}, nil
}

// openSQLiteStore migrates the SQLite file at path and opens the auth repositories on a handle of their own.
func openSQLiteStore(ctx context.Context, path string, logger zerolog.Logger) (*store, error) {
	if err := database.RunSQLiteMigrations(ctx, path, authsqlitemigrations.FS, "auth", logger); err != nil {
		return nil, fmt.Errorf("auth: running sqlite migrations: %w", err)
	}
	db, err := database.OpenSQLite(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	users, roles := authsqlite.NewUserRepo(db), authsqlite.NewRoleRepo(db)
	return &store{
		users:      users,
		roles:      roles,
		oidcStates: authsqlite.NewOIDCStateRepo(db),
		userTx: database.SQLiteInTx(db, func(tx *sql.Tx) service.UserTx {
			return service.UserTx{
				Users:    authsqlite.NewUserRepo(tx),
				Roles:    authsqlite.NewRoleRepo(tx),
				Sessions: authsqlite.NewAccessTokenRepo(tx),
			}
		}),
		identityTx: database.SQLiteInTx(db, func(tx *sql.Tx) service.IdentityTx {
			return service.IdentityTx{
				Identities: authsqlite.NewExternalIdentityRepo(tx),
				Users:      authsqlite.NewUserRepo(tx),
			}
		}),
		ownerTx: database.SQLiteInTx(db, func(tx *sql.Tx) service.OwnerTx {
			return service.OwnerTx{
				Owner: authsqlite.NewInstanceOwnerRepo(tx),
				Users: authsqlite.NewUserRepo(tx),
				Roles: authsqlite.NewRoleRepo(tx),
			}
		}),
		tokenSvc: service.NewTokenService(authsqlite.NewAccessTokenRepo(db), users,
			service.NewAuthenticationService(users)),
		close: func() {
			if err := db.Close(); err != nil {
				logger.Warn().Err(err).Msg("auth: closing the sqlite store")
			}
		},
	}, nil
}
