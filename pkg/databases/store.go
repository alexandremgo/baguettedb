package databases

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/postgres"
	dbsqlite "github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlite"
	dbmigrations "github.com/alexandremgo/baguettedb/pkg/databases/migrations"
	dbsqlitemigrations "github.com/alexandremgo/baguettedb/pkg/databases/migrations/sqlite"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// store holds the databases app's metadata repositories over the instance store the config selects: Postgres, or
// the SQLite file at cfg.InstanceSQLitePath when it is set.
type store struct {
	databases service.DatabaseRepository
	exports   service.ExportRepository
	close     func()
}

// openStore migrates the selected instance store and opens the metadata repositories on it. close releases the
// connection.
func openStore(ctx context.Context, cfg config.Config, logger zerolog.Logger) (*store, error) {
	if cfg.InstanceSQLitePath != "" {
		return openSQLiteStore(ctx, cfg.InstanceSQLitePath, logger)
	}

	if err := database.RunMigrations(cfg.DatabasesDatabaseDSN, dbmigrations.FS, "databases", logger); err != nil {
		return nil, fmt.Errorf("databases: running migrations: %w", err)
	}
	pool, err := database.NewPostgresPool(ctx, cfg.DatabasesDatabaseDSN)
	if err != nil {
		return nil, fmt.Errorf("databases: connecting to postgres: %w", err)
	}
	return &store{
		databases: postgres.NewDatabaseRepo(pool),
		exports:   postgres.NewExportRepo(pool),
		close:     pool.Close,
	}, nil
}

// openSQLiteStore migrates the SQLite file at path and opens the metadata repositories on a handle of their own.
func openSQLiteStore(ctx context.Context, path string, logger zerolog.Logger) (*store, error) {
	if err := database.RunSQLiteMigrations(ctx, path, dbsqlitemigrations.FS, "databases", logger); err != nil {
		return nil, fmt.Errorf("databases: running sqlite migrations: %w", err)
	}
	db, err := database.OpenSQLite(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("databases: %w", err)
	}
	return &store{
		databases: dbsqlite.NewDatabaseRepo(db),
		exports:   dbsqlite.NewExportRepo(db),
		close: func() {
			if err := db.Close(); err != nil {
				logger.Warn().Err(err).Msg("databases: closing the sqlite store")
			}
		},
	}, nil
}
