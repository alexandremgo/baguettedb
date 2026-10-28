// Package database holds the helpers the instance store is built on, Postgres or SQLite: migrations tracked per app,
// so several apps share one database, and refused on a schema a newer version migrated; opening connections; the DBTX
// and SQLiteDBTX query interfaces a repository runs on, over a connection or a transaction; and PostgresInTx and
// SQLiteInTx, running a function over repositories bound to one transaction.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	"github.com/golang-migrate/migrate/v4"
	migratedb "github.com/golang-migrate/migrate/v4/database"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/lib/pq" // registers the "postgres" driver for database/sql
	"github.com/rs/zerolog"
)

// ErrNewerSchema reports a database migrated past the last migration of the running version: a newer version migrated
// it, and this one refuses to run on a schema it doesn't know.
var ErrNewerSchema = errors.New("the database was migrated by a newer version")

// RunMigrations applies all pending migrations from the given embedded filesystem.
// Uses a per-module migration table (schema_migrations_<moduleName>) to avoid
// collisions when multiple modules share the same database.
// Uses Postgres advisory locks for safe concurrent execution.
func RunMigrations(dsn string, fsys fs.FS, moduleName string, logger zerolog.Logger) error {
	source, err := iofs.New(fsys, ".")
	if err != nil {
		return fmt.Errorf("creating migration source: %w", err)
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("opening database for migrations: %w", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			logger.Warn().Err(cerr).Msg("closing migration database connection")
		}
	}()

	driver, err := migratepg.WithInstance(db, &migratepg.Config{
		MigrationsTable: "schema_migrations_" + moduleName,
	})
	if err != nil {
		return fmt.Errorf("creating migration driver: %w", err)
	}

	return migrateUp(source, "postgres", driver, moduleName, logger)
}

// RunSQLiteMigrations applies all pending migrations from the given embedded filesystem to the SQLite file at path.
// Like RunMigrations, it tracks them in a per-app table (schema_migrations_<moduleName>), so several apps share
// one file. It works on a handle of its own, closed on return, since the migrator closes the handle it runs on.
//
// The migrator's lock only guards this process. Two processes migrating the same file at once stay safe because each
// migration runs in its own immediate transaction and every statement is idempotent (IF NOT EXISTS), so a migration
// the other process already applied replays as a no-op.
func RunSQLiteMigrations(ctx context.Context, path string, fsys fs.FS, moduleName string, logger zerolog.Logger) error {
	source, err := iofs.New(fsys, ".")
	if err != nil {
		return fmt.Errorf("creating migration source: %w", err)
	}

	db, err := OpenSQLite(ctx, path)
	if err != nil {
		return fmt.Errorf("opening database for migrations: %w", err)
	}

	driver, err := migratesqlite.WithInstance(db, &migratesqlite.Config{
		MigrationsTable: "schema_migrations_" + moduleName,
	})
	if err != nil {
		if cerr := db.Close(); cerr != nil {
			logger.Warn().Err(cerr).Msg("closing migration database connection")
		}
		return fmt.Errorf("creating migration driver: %w", err)
	}

	return migrateUp(source, "sqlite", driver, moduleName, logger)
}

// migrateUp applies every pending migration of source through driver and logs the outcome. A database migrated past
// source's last migration wraps ErrNewerSchema. It closes the migrator, which closes both source and driver along with
// the database handle the driver runs on.
func migrateUp(
	source source.Driver, driverName string, driver migratedb.Driver, moduleName string, logger zerolog.Logger,
) error {
	m, err := migrate.NewWithInstance("iofs", source, driverName, driver)
	if err != nil {
		return fmt.Errorf("creating migrator: %w", err)
	}
	defer func() {
		if srcErr, dbErr := m.Close(); srcErr != nil || dbErr != nil {
			logger.Warn().AnErr("source_error", srcErr).AnErr("db_error", dbErr).Msg("closing migrator")
		}
	}()

	if err := refuseNewerSchema(m, source, moduleName); err != nil {
		return err
	}

	err = m.Up()
	if errors.Is(err, migrate.ErrNoChange) {
		logger.Info().Str("module", moduleName).Msg("migrations: already up to date")
		return nil
	}
	if err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}

	logger.Info().Str("module", moduleName).Msg("migrations: applied successfully")
	return nil
}

// refuseNewerSchema returns an error wrapping ErrNewerSchema when the version m's database records is past the last
// migration of src.
func refuseNewerSchema(m *migrate.Migrate, src source.Driver, moduleName string) error {
	version, _, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading the migration version: %w", err)
	}
	last, err := lastVersion(src)
	if err != nil {
		return err
	}
	if version > last {
		return fmt.Errorf("%w: %s migrations at version %d, this version's last is %d",
			ErrNewerSchema, moduleName, version, last)
	}
	return nil
}

// lastVersion returns the version of src's last migration.
func lastVersion(src source.Driver) (uint, error) {
	version, err := src.First()
	if err != nil {
		return 0, fmt.Errorf("reading the first migration: %w", err)
	}
	for {
		next, err := src.Next(version)
		if errors.Is(err, fs.ErrNotExist) {
			return version, nil
		}
		if err != nil {
			return 0, fmt.Errorf("reading the migration after version %d: %w", version, err)
		}
		version = next
	}
}

// LatestMigrationVersion returns the version of the last migration in fsys, read as the migrator reads it: the version
// a database it migrates ends at. An fsys with no migration is an error.
func LatestMigrationVersion(fsys fs.FS) (latest uint, err error) {
	src, err := iofs.New(fsys, ".")
	if err != nil {
		return 0, fmt.Errorf("reading the migrations: %w", err)
	}
	defer func() {
		if cerr := src.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
	}()
	version, err := src.First()
	if err != nil {
		return 0, fmt.Errorf("reading the first migration: %w", err)
	}
	for {
		next, err := src.Next(version)
		if errors.Is(err, fs.ErrNotExist) {
			return version, nil
		}
		if err != nil {
			return 0, fmt.Errorf("reading the migration after %d: %w", version, err)
		}
		version = next
	}
}
