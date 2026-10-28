// Package testutil holds the fixtures the integration suites share: InstanceDB, the migrated instance store a suite
// runs on (a Postgres container, or an SQLite file when BAGUETTEDB_TEST_STORE=sqlite) with seeded users; an
// S3-compatible store in a container; stand-ins for authentication (a test JWKS signing tokens in place of the
// authorization server, and a middleware accepting every request as a given user); and helpers driving a handler.
package testutil

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
)

// Migrations holds one app's migrations for each instance store.
type Migrations struct {
	App      string
	Postgres fs.FS
	SQLite   fs.FS
}

// InstanceDB is the instance store an integration suite runs on: a Postgres database or an SQLite file. Exactly one
// of Pool and SQLite is set. Exec and QueryRow run the suite's own statements on either store; they take $N
// placeholders, which both drivers accept.
type InstanceDB struct {
	// DSN and Pool are set on Postgres.
	DSN  string
	Pool *pgxpool.Pool
	// SQLitePath and SQLite are set on SQLite.
	SQLitePath string
	SQLite     *sql.DB
}

// Row is a query result of at most one row.
type Row interface {
	Scan(dest ...any) error
}

// OpenInstanceDB starts the instance store InstanceStore selects — a Postgres container, or an SQLite file in a
// fresh temporary directory — applies each app's migrations to it and connects to it. cleanup releases all of it.
func OpenInstanceDB(ctx context.Context, logger zerolog.Logger, apps ...Migrations) (*InstanceDB, func(), error) {
	store, err := InstanceStore()
	if err != nil {
		return nil, nil, err
	}
	if store == StoreSQLite {
		return openSQLiteInstanceDB(ctx, logger, apps)
	}

	dsn, stopContainer, err := NewPostgresContainer(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("starting postgres container: %w", err)
	}
	for _, app := range apps {
		if err := database.RunMigrations(dsn, app.Postgres, app.App, logger); err != nil {
			stopContainer()
			return nil, nil, fmt.Errorf("running %s migrations: %w", app.App, err)
		}
	}
	pool, err := database.NewPostgresPool(ctx, dsn)
	if err != nil {
		stopContainer()
		return nil, nil, fmt.Errorf("creating pool: %w", err)
	}
	return &InstanceDB{DSN: dsn, Pool: pool}, func() {
		pool.Close()
		stopContainer()
	}, nil
}

// openSQLiteInstanceDB creates an instance store SQLite file in a fresh temporary directory, migrates it and opens it.
func openSQLiteInstanceDB(ctx context.Context, logger zerolog.Logger, apps []Migrations) (*InstanceDB, func(), error) {
	dir, err := os.MkdirTemp("", "baguettedb-instance-*")
	if err != nil {
		return nil, nil, fmt.Errorf("creating the sqlite directory: %w", err)
	}
	removeDir := func() {
		if err := os.RemoveAll(dir); err != nil {
			logger.Warn().Err(err).Str("dir", dir).Msg("removing the sqlite directory")
		}
	}

	path := filepath.Join(dir, "instance.sqlite")
	for _, app := range apps {
		if err := database.RunSQLiteMigrations(ctx, path, app.SQLite, app.App, logger); err != nil {
			removeDir()
			return nil, nil, fmt.Errorf("running %s sqlite migrations: %w", app.App, err)
		}
	}
	db, err := database.OpenSQLite(ctx, path)
	if err != nil {
		removeDir()
		return nil, nil, err
	}
	return &InstanceDB{SQLitePath: path, SQLite: db}, func() {
		if err := db.Close(); err != nil {
			logger.Warn().Err(err).Msg("closing the sqlite instance store")
		}
		removeDir()
	}, nil
}

// Configure points every app's store setting in cfg at this instance store: the three database DSNs on Postgres, the
// instance SQLite path on SQLite.
func (db *InstanceDB) Configure(cfg *config.Config) {
	if db.SQLite != nil {
		cfg.InstanceSQLitePath = db.SQLitePath
		return
	}
	cfg.AuthDatabaseDSN = db.DSN
	cfg.BdbsDatabaseDSN = db.DSN
	cfg.DatabasesDatabaseDSN = db.DSN
}

// Exec runs a statement that returns no rows.
func (db *InstanceDB) Exec(ctx context.Context, query string, args ...any) error {
	if db.SQLite != nil {
		_, err := db.SQLite.ExecContext(ctx, query, args...)
		return err
	}
	_, err := db.Pool.Exec(ctx, query, args...)
	return err
}

// QueryRow runs a query that returns at most one row.
func (db *InstanceDB) QueryRow(ctx context.Context, query string, args ...any) Row {
	if db.SQLite != nil {
		return db.SQLite.QueryRowContext(ctx, query, args...)
	}
	return db.Pool.QueryRow(ctx, query, args...)
}

// DeleteAll empties the given tables in order, so a child table must come before the table it references.
func (db *InstanceDB) DeleteAll(ctx context.Context, tables ...string) error {
	var errs []error
	for _, table := range tables {
		if err := db.Exec(ctx, "DELETE FROM "+table); err != nil {
			errs = append(errs, fmt.Errorf("emptying %s: %w", table, err))
		}
	}
	return errors.Join(errs...)
}
