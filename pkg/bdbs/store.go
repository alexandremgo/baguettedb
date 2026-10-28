package bdbs

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	bdbpg "github.com/alexandremgo/baguettedb/pkg/bdbs/adapters/postgres"
	bdbsqlite "github.com/alexandremgo/baguettedb/pkg/bdbs/adapters/sqlite"
	bdbmigrations "github.com/alexandremgo/baguettedb/pkg/bdbs/migrations"
	bdbsqlitemigrations "github.com/alexandremgo/baguettedb/pkg/bdbs/migrations/sqlite"
	"github.com/alexandremgo/baguettedb/pkg/bdbs/service"
	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
)

// store holds the bdbs services wired over the instance store the config selects: Postgres, or the SQLite file at
// cfg.InstanceSQLitePath when it is set.
type store struct {
	// pool is the Postgres pool, nil on SQLite.
	pool        *pgxpool.Pool
	bdbSvc      *service.BdbService
	inviteSvc   *service.InviteService
	authChecker *service.BdbAuthChecker
	close       func()
}

// openStore migrates the selected instance store and wires the bdbs services over its repositories. close
// releases the connection.
func openStore(
	ctx context.Context, cfg config.Config, logger zerolog.Logger, authApp service.AuthApp, qr quota.Resolver,
) (*store, error) {
	if cfg.InstanceSQLitePath != "" {
		return openSQLiteStore(ctx, cfg.InstanceSQLitePath, logger, authApp, qr)
	}

	if err := database.RunMigrations(cfg.BdbsDatabaseDSN, bdbmigrations.FS, "bdbs", logger); err != nil {
		return nil, fmt.Errorf("bdbs: running migrations: %w", err)
	}
	pool, err := database.NewPostgresPool(ctx, cfg.BdbsDatabaseDSN)
	if err != nil {
		return nil, fmt.Errorf("bdbs: connecting to postgres: %w", err)
	}

	bdbRepo := bdbpg.NewBdbRepo(pool)
	membershipTx := database.PostgresInTx(pool, func(tx pgx.Tx) service.MembershipTx {
		m := bdbpg.NewMembershipTxRepo(tx)
		return service.MembershipTx{Bdbs: m, Members: m}
	})
	inviteAcceptTx := database.PostgresInTx(pool, func(tx pgx.Tx) service.InviteAcceptTx {
		m := bdbpg.NewMembershipTxRepo(tx)
		return service.InviteAcceptTx{Bdbs: m, Members: m, Invites: m}
	})
	bdbSvc := service.NewBdbService(bdbRepo, membershipTx, authApp, qr, logger)
	bdbSvc.SetPurgeLock(bdbpg.NewPurgeLock(pool, logger))
	return &store{
		pool:        pool,
		bdbSvc:      bdbSvc,
		inviteSvc:   service.NewInviteService(bdbpg.NewInviteRepo(pool), bdbRepo, inviteAcceptTx, qr, authApp, logger),
		authChecker: service.NewBdbAuthChecker(bdbRepo, authApp),
		close:       pool.Close,
	}, nil
}

// openSQLiteStore migrates the SQLite file at path and wires the bdbs services over repositories on a handle of
// their own.
func openSQLiteStore(
	ctx context.Context, path string, logger zerolog.Logger, authApp service.AuthApp, qr quota.Resolver,
) (*store, error) {
	if err := database.RunSQLiteMigrations(ctx, path, bdbsqlitemigrations.FS, "bdbs", logger); err != nil {
		return nil, fmt.Errorf("bdbs: running sqlite migrations: %w", err)
	}
	db, err := database.OpenSQLite(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("bdbs: %w", err)
	}

	bdbRepo := bdbsqlite.NewBdbRepo(db)
	membershipTx := database.SQLiteInTx(db, func(tx *sql.Tx) service.MembershipTx {
		m := bdbsqlite.NewMembershipTxRepo(tx)
		return service.MembershipTx{Bdbs: m, Members: m}
	})
	inviteAcceptTx := database.SQLiteInTx(db, func(tx *sql.Tx) service.InviteAcceptTx {
		m := bdbsqlite.NewMembershipTxRepo(tx)
		return service.InviteAcceptTx{Bdbs: m, Members: m, Invites: m}
	})
	bdbSvc := service.NewBdbService(bdbRepo, membershipTx, authApp, qr, logger)
	bdbSvc.SetPurgeLock(bdbsqlite.NewPurgeLock(path, logger))
	return &store{
		bdbSvc: bdbSvc,
		inviteSvc: service.NewInviteService(
			bdbsqlite.NewInviteRepo(db), bdbRepo, inviteAcceptTx, qr, authApp, logger,
		),
		authChecker: service.NewBdbAuthChecker(bdbRepo, authApp),
		close: func() {
			if err := db.Close(); err != nil {
				logger.Warn().Err(err).Msg("bdbs: closing the sqlite store")
			}
		},
	}, nil
}
