package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DataAPIKeyFormulaRepo stores the formulas each Data API key of a bdb may run, in PostgreSQL. It is pool-bound.
type DataAPIKeyFormulaRepo struct {
	pool *pgxpool.Pool
}

// NewDataAPIKeyFormulaRepo creates a DataAPIKeyFormulaRepo backed by the given connection pool.
func NewDataAPIKeyFormulaRepo(pool *pgxpool.Pool) *DataAPIKeyFormulaRepo {
	return &DataAPIKeyFormulaRepo{pool: pool}
}

// AddFormulas records, in one statement, that the key keyID of the bdb bdbID may run each of formulaIDs, as of at. A
// formula the key already has is left as is.
func (r *DataAPIKeyFormulaRepo) AddFormulas(
	ctx context.Context, bdbID, keyID uuid.UUID, formulaIDs []string, at time.Time,
) error {
	if len(formulaIDs) == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO data_api_key_formulas (key_id, bdb_id, formula_id, created_at)
		 SELECT $1, $2, formula_id, $4 FROM unnest($3::text[]) AS formula_id
		 ON CONFLICT (key_id, formula_id) DO NOTHING`,
		keyID, bdbID, formulaIDs, at,
	)
	if err != nil {
		return fmt.Errorf("adding a data API key's formulas: %w", err)
	}
	return nil
}

// RemoveFormula forgets that the key keyID of the bdb bdbID may run formulaID. A formula the key doesn't have is not
// an error.
func (r *DataAPIKeyFormulaRepo) RemoveFormula(ctx context.Context, bdbID, keyID uuid.UUID, formulaID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM data_api_key_formulas WHERE bdb_id = $1 AND key_id = $2 AND formula_id = $3`,
		bdbID, keyID, formulaID,
	)
	if err != nil {
		return fmt.Errorf("removing a data API key's formula: %w", err)
	}
	return nil
}

// ListByBdb returns the formula IDs of each key of the bdb bdbID that has any, keyed by key ID, each key's in the order
// they were added.
func (r *DataAPIKeyFormulaRepo) ListByBdb(ctx context.Context, bdbID uuid.UUID) (map[uuid.UUID][]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT key_id, formula_id FROM data_api_key_formulas WHERE bdb_id = $1 ORDER BY created_at, formula_id`,
		bdbID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing data API key formulas: %w", err)
	}
	defer rows.Close()

	formulas := make(map[uuid.UUID][]string)
	for rows.Next() {
		var keyID uuid.UUID
		var formulaID string
		if err := rows.Scan(&keyID, &formulaID); err != nil {
			return nil, fmt.Errorf("scanning a data API key formula: %w", err)
		}
		formulas[keyID] = append(formulas[keyID], formulaID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing data API key formulas: %w", err)
	}
	return formulas, nil
}

// ListByKey returns the formula IDs of the key keyID of the bdb bdbID, in the order they were added.
func (r *DataAPIKeyFormulaRepo) ListByKey(ctx context.Context, bdbID, keyID uuid.UUID) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT formula_id FROM data_api_key_formulas WHERE bdb_id = $1 AND key_id = $2
		 ORDER BY created_at, formula_id`,
		bdbID, keyID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing a data API key's formulas: %w", err)
	}
	defer rows.Close()

	var formulaIDs []string
	for rows.Next() {
		var formulaID string
		if err := rows.Scan(&formulaID); err != nil {
			return nil, fmt.Errorf("scanning a data API key's formula: %w", err)
		}
		formulaIDs = append(formulaIDs, formulaID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing a data API key's formulas: %w", err)
	}
	return formulaIDs, nil
}

// HasFormula reports whether the key keyID of the bdb bdbID may run formulaID.
func (r *DataAPIKeyFormulaRepo) HasFormula(
	ctx context.Context, bdbID, keyID uuid.UUID, formulaID string,
) (bool, error) {
	var found bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (
		     SELECT 1 FROM data_api_key_formulas WHERE bdb_id = $1 AND key_id = $2 AND formula_id = $3
		 )`,
		bdbID, keyID, formulaID,
	).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("checking a data API key's formula: %w", err)
	}
	return found, nil
}

// DeleteByFormula forgets formulaID for every key of the bdb bdbID.
func (r *DataAPIKeyFormulaRepo) DeleteByFormula(ctx context.Context, bdbID uuid.UUID, formulaID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM data_api_key_formulas WHERE bdb_id = $1 AND formula_id = $2`, bdbID, formulaID)
	if err != nil {
		return fmt.Errorf("deleting a formula's data API key links: %w", err)
	}
	return nil
}

// DeleteByKey forgets every formula of the key keyID of the bdb bdbID.
func (r *DataAPIKeyFormulaRepo) DeleteByKey(ctx context.Context, bdbID, keyID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM data_api_key_formulas WHERE bdb_id = $1 AND key_id = $2`, bdbID, keyID)
	if err != nil {
		return fmt.Errorf("deleting a data API key's formulas: %w", err)
	}
	return nil
}

// DeleteByBdb forgets every formula of every key of the bdb bdbID.
func (r *DataAPIKeyFormulaRepo) DeleteByBdb(ctx context.Context, bdbID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM data_api_key_formulas WHERE bdb_id = $1`, bdbID)
	if err != nil {
		return fmt.Errorf("deleting a bdb's data API key formulas: %w", err)
	}
	return nil
}
