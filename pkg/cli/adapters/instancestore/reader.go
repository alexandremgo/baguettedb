// Package instancestore reads a local instance's store without changing it, for reports that must neither migrate it
// nor create anything: opening the instance itself would run the store's migrations.
package instancestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	_ "modernc.org/sqlite" // registers the "sqlite" driver for database/sql

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// readOnlyParams open the store read-only, refuse any write on the connection, and wait for a writer's lock as the
// instance's own connections do.
const readOnlyParams = "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)"

// Read summarizes the instance store at path, opened read-only: its owner, whether an account has the email as when
// as is not empty, the counts of bdbs (soft-deleted ones left out) and accounts, and the schema version of each app in
// apps, whose App and Latest the caller sets. A table the store doesn't have yet reads as empty, and an app without
// its migration table at version 0.
func Read(ctx context.Context, path, as string, apps []domain.AppSchema) (summary domain.InstanceSummary, err error) {
	db, err := sql.Open("sqlite", "file:"+path+"?"+readOnlyParams)
	if err != nil {
		return domain.InstanceSummary{}, fmt.Errorf("opening the instance store read-only: %w", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing the instance store: %w", cerr))
		}
	}()

	r := reader{db: db}
	for _, app := range apps {
		app.Version, app.Dirty, err = r.schemaVersion(ctx, app.App)
		if err != nil {
			return domain.InstanceSummary{}, err
		}
		summary.Schemas = append(summary.Schemas, app)
	}
	if summary.Owner, err = r.owner(ctx); err != nil {
		return domain.InstanceSummary{}, err
	}
	if as != "" {
		summary.AsFound, err = r.exists(ctx, "users", "SELECT 1 FROM users WHERE lower(email) = lower(?)", as)
		if err != nil {
			return domain.InstanceSummary{}, err
		}
	}
	if summary.Bdbs, err = r.count(ctx, "bdbs", "SELECT count(*) FROM bdbs WHERE deleted_at IS NULL"); err != nil {
		return domain.InstanceSummary{}, err
	}
	if summary.Accounts, err = r.count(ctx, "users", "SELECT count(*) FROM users"); err != nil {
		return domain.InstanceSummary{}, err
	}
	return summary, nil
}

// reader runs the queries of Read on one read-only handle.
type reader struct {
	db *sql.DB
}

// hasTable reports whether the store has the table name.
func (r reader) hasTable(ctx context.Context, name string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).
		Scan(&n)
	if err != nil {
		return false, fmt.Errorf("looking for the table %s: %w", name, err)
	}
	return n > 0, nil
}

// schemaVersion returns the migration version of app and whether its last migration failed halfway, 0 when the app
// has no migration table yet.
func (r reader) schemaVersion(ctx context.Context, app string) (version uint, dirty bool, err error) {
	table := "schema_migrations_" + app
	if ok, err := r.hasTable(ctx, table); err != nil || !ok {
		return 0, false, err
	}
	// The table name comes from the caller's list of apps, never from input.
	err = r.db.QueryRowContext(ctx, "SELECT version, dirty FROM "+table+" LIMIT 1").Scan(&version, &dirty)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("reading the schema version of %s: %w", app, err)
	}
	return version, dirty, nil
}

// owner returns the account the store records as the instance's owner, nil when it records none.
func (r reader) owner(ctx context.Context) (*domain.User, error) {
	if ok, err := r.hasTable(ctx, "instance_owner"); err != nil || !ok {
		return nil, err
	}
	var id, email, first, last string
	err := r.db.QueryRowContext(ctx, `SELECT u.id, u.email, u.first_name, u.last_name
		FROM instance_owner o JOIN users u ON u.id = o.user_id ORDER BY o.id LIMIT 1`).Scan(&id, &email, &first, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the owner: %w", err)
	}
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("reading the owner's ID %q: %w", id, err)
	}
	return &domain.User{ID: uid, Email: email, FirstName: first, LastName: last}, nil
}

// count runs query, a count over table, 0 when the store has no such table yet.
func (r reader) count(ctx context.Context, table, query string) (int, error) {
	if ok, err := r.hasTable(ctx, table); err != nil || !ok {
		return 0, err
	}
	var n int
	if err := r.db.QueryRowContext(ctx, query).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting %s: %w", table, err)
	}
	return n, nil
}

// exists reports whether query, over table, returns a row for args, false when the store has no such table yet.
func (r reader) exists(ctx context.Context, table, query string, args ...any) (bool, error) {
	if ok, err := r.hasTable(ctx, table); err != nil || !ok {
		return false, err
	}
	var one int
	err := r.db.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("looking in %s: %w", table, err)
	}
	return true, nil
}
