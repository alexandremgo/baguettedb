// Package postgres implements the databases app's repositories on the Postgres instance store: the metadata of each
// bdb's database file, the active blue/green fleet color, and the audit trail of user exports.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/alexandremgo/baguettedb/pkg/common/database"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// DatabaseRepo implements domain.DatabaseRepository using Postgres.
type DatabaseRepo struct {
	db database.DBTX
}

// NewDatabaseRepo creates a new DatabaseRepo backed by the given DBTX.
func NewDatabaseRepo(db database.DBTX) *DatabaseRepo {
	return &DatabaseRepo{db: db}
}

// Create persists a new database record.
func (r *DatabaseRepo) Create(ctx context.Context, db *domain.Database) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO databases (id, bdb_id, platform_schema_version, encryption_key_wrapped, pod_shard, pod_color,
		                        created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		db.ID, db.BdbID, db.PlatformSchemaVersion, db.EncryptionKeyWrapped, db.PodShard, db.PodColor,
		db.CreatedAt, db.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting database: %w", err)
	}
	return nil
}

// FindByID returns the database with the given ID.
func (r *DatabaseRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.Database, error) {
	return r.scanDatabase(r.db.QueryRow(ctx,
		`SELECT id, bdb_id, platform_schema_version, encryption_key_wrapped,
		        pod_shard, pod_color, read_only, size_bytes, size_updated_at, created_at, updated_at
		 FROM databases WHERE id = $1`, id))
}

// FindByBdb returns the database owned by the given bdb.
func (r *DatabaseRepo) FindByBdb(ctx context.Context, bdbID uuid.UUID) (*domain.Database, error) {
	return r.scanDatabase(r.db.QueryRow(ctx,
		`SELECT id, bdb_id, platform_schema_version, encryption_key_wrapped,
		        pod_shard, pod_color, read_only, size_bytes, size_updated_at, created_at, updated_at
		 FROM databases WHERE bdb_id = $1`, bdbID))
}

// Delete removes the database record.
func (r *DatabaseRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM databases WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting database: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrDatabaseNotFound
	}
	return nil
}

// UpdatePlatformSchemaVersion sets the platform schema version for a database.
func (r *DatabaseRepo) UpdatePlatformSchemaVersion(ctx context.Context, id uuid.UUID, version int) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE databases SET platform_schema_version = $1 WHERE id = $2`,
		version, id,
	)
	if err != nil {
		return fmt.Errorf("updating platform schema version: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrDatabaseNotFound
	}
	return nil
}

// SetReadOnly sets the read_only flag for a database. Returns domain.ErrDatabaseNotFound if not found.
func (r *DatabaseRepo) SetReadOnly(ctx context.Context, id uuid.UUID, readOnly bool) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE databases SET read_only = $1, updated_at = NOW() WHERE id = $2`,
		readOnly, id,
	)
	if err != nil {
		return fmt.Errorf("updating read_only: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrDatabaseNotFound
	}
	return nil
}

// UpdateOwnerAndReadOnly sets a database's owner (pod_color + pod_shard) and read_only flag in one statement.
// Returns domain.ErrDatabaseNotFound if not found.
func (r *DatabaseRepo) UpdateOwnerAndReadOnly(
	ctx context.Context, id uuid.UUID, color, shard int16, readOnly bool,
) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE databases SET read_only = $1, pod_color = $2, pod_shard = $3, updated_at = NOW() WHERE id = $4`,
		readOnly, color, shard, id,
	)
	if err != nil {
		return fmt.Errorf("updating owner and read_only: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrDatabaseNotFound
	}
	return nil
}

// UpdateSize sets the tracked on-disk size for a database, stamping size_updated_at. Leaves updated_at
// untouched: size refreshes are high-frequency housekeeping, and stamping updated_at on every write batch
// would drown the lifecycle changes it records. Returns domain.ErrDatabaseNotFound if not found.
func (r *DatabaseRepo) UpdateSize(ctx context.Context, id uuid.UUID, sizeBytes int64) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE databases SET size_bytes = $1, size_updated_at = NOW() WHERE id = $2`,
		sizeBytes, id,
	)
	if err != nil {
		return fmt.Errorf("updating size: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrDatabaseNotFound
	}
	return nil
}

// SumSizeBytesByBdbIDs returns the total tracked size of the databases belonging to the given bdbs.
// One indexed lookup per ID via the bdb_id unique index; unknown IDs contribute nothing.
func (r *DatabaseRepo) SumSizeBytesByBdbIDs(ctx context.Context, bdbIDs []uuid.UUID) (int64, error) {
	if len(bdbIDs) == 0 {
		return 0, nil
	}
	var total int64
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(size_bytes), 0) FROM databases WHERE bdb_id = ANY($1)`, bdbIDs,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("summing size by bdb ids: %w", err)
	}
	return total, nil
}

// GetActiveColor returns the currently active fleet color from the single-row databases_control table
// (0 = blue, 1 = green).
func (r *DatabaseRepo) GetActiveColor(ctx context.Context) (int16, error) {
	var color int16
	err := r.db.QueryRow(ctx, `SELECT active_color FROM databases_control WHERE id = true`).Scan(&color)
	if err != nil {
		return 0, fmt.Errorf("getting databases active color: %w", err)
	}

	return color, nil
}

// SetActiveColor sets the active fleet color on the single-row databases_control table. Returns an error if the
// control row is missing (zero rows updated).
func (r *DatabaseRepo) SetActiveColor(ctx context.Context, color int16) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE databases_control SET active_color = $1 WHERE id = true`, color)
	if err != nil {
		return fmt.Errorf("setting databases active color to %d: %w", color, err)
	}

	if tag.RowsAffected() == 0 {
		return fmt.Errorf("setting databases active color to %d: no row affected", color)
	}

	return nil
}

// ListAll returns every database row, ordered by creation time.
func (r *DatabaseRepo) ListAll(ctx context.Context) ([]*domain.Database, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, bdb_id, platform_schema_version, encryption_key_wrapped,
		        pod_shard, pod_color, read_only, size_bytes, size_updated_at, created_at, updated_at
		 FROM databases ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("listing databases: %w", err)
	}
	return collectDatabases(rows)
}

// ListOwned returns every database row assigned to the given fleet color and shard, ordered by creation time.
func (r *DatabaseRepo) ListOwned(ctx context.Context, color, shard int16) ([]*domain.Database, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, bdb_id, platform_schema_version, encryption_key_wrapped,
		        pod_shard, pod_color, read_only, size_bytes, size_updated_at, created_at, updated_at
		 FROM databases WHERE pod_color = $1 AND pod_shard = $2 ORDER BY created_at`, color, shard)
	if err != nil {
		return nil, fmt.Errorf("listing owned databases: %w", err)
	}
	return collectDatabases(rows)
}

// collectDatabases drains rows into database records, closing rows before returning.
func collectDatabases(rows pgx.Rows) ([]*domain.Database, error) {
	defer rows.Close()

	var dbs []*domain.Database
	for rows.Next() {
		var db domain.Database
		if err := rows.Scan(
			&db.ID, &db.BdbID, &db.PlatformSchemaVersion,
			&db.EncryptionKeyWrapped, &db.PodShard, &db.PodColor, &db.ReadOnly,
			&db.SizeBytes, &db.SizeUpdatedAt, &db.CreatedAt, &db.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning database row: %w", err)
		}
		dbs = append(dbs, &db)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating database rows: %w", err)
	}
	return dbs, nil
}

func (r *DatabaseRepo) scanDatabase(row pgx.Row) (*domain.Database, error) {
	var db domain.Database
	err := row.Scan(
		&db.ID, &db.BdbID, &db.PlatformSchemaVersion,
		&db.EncryptionKeyWrapped, &db.PodShard, &db.PodColor, &db.ReadOnly,
		&db.SizeBytes, &db.SizeUpdatedAt, &db.CreatedAt, &db.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrDatabaseNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scanning database: %w", err)
	}
	return &db, nil
}
