package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/database"
)

// DataAPIKeyFormulaRepo stores the formulas each Data API key of a bdb may run, in SQLite. Times are written in UTC.
type DataAPIKeyFormulaRepo struct {
	db *sql.DB
}

// NewDataAPIKeyFormulaRepo creates a DataAPIKeyFormulaRepo backed by the given handle.
func NewDataAPIKeyFormulaRepo(db *sql.DB) *DataAPIKeyFormulaRepo {
	return &DataAPIKeyFormulaRepo{db: db}
}

// AddFormulas records, in one statement, that the key keyID of the bdb bdbID may run each of formulaIDs, as of at. A
// formula the key already has is left as is.
func (r *DataAPIKeyFormulaRepo) AddFormulas(
	ctx context.Context, bdbID, keyID uuid.UUID, formulaIDs []string, at time.Time,
) error {
	if len(formulaIDs) == 0 {
		return nil
	}
	// $1 to $3 are the key, the bdb and the time, shared by every row; each formula ID takes the next number.
	var values strings.Builder
	args := []any{keyID, bdbID, at.UTC()}
	for i, formulaID := range formulaIDs {
		if i > 0 {
			values.WriteString(", ")
		}
		fmt.Fprintf(&values, "($1, $2, $%d, $3)", len(args)+1)
		args = append(args, formulaID)
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO data_api_key_formulas (key_id, bdb_id, formula_id, created_at) VALUES `+values.String()+`
		 ON CONFLICT (key_id, formula_id) DO NOTHING`,
		args...,
	)
	if err != nil {
		return fmt.Errorf("adding a data API key's formulas: %w", err)
	}
	return nil
}

// RemoveFormula forgets that the key keyID of the bdb bdbID may run formulaID. A formula the key doesn't have is not
// an error.
func (r *DataAPIKeyFormulaRepo) RemoveFormula(ctx context.Context, bdbID, keyID uuid.UUID, formulaID string) error {
	_, err := r.db.ExecContext(ctx,
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
func (r *DataAPIKeyFormulaRepo) ListByBdb(
	ctx context.Context, bdbID uuid.UUID,
) (_ map[uuid.UUID][]string, err error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT key_id, formula_id FROM data_api_key_formulas WHERE bdb_id = $1 ORDER BY created_at, formula_id`,
		bdbID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing data API key formulas: %w", err)
	}
	defer database.CloseRows(rows, &err)

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

// DeleteByKey forgets every formula of the key keyID of the bdb bdbID.
func (r *DataAPIKeyFormulaRepo) DeleteByKey(ctx context.Context, bdbID, keyID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM data_api_key_formulas WHERE bdb_id = $1 AND key_id = $2`, bdbID, keyID)
	if err != nil {
		return fmt.Errorf("deleting a data API key's formulas: %w", err)
	}
	return nil
}

// DeleteByBdb forgets every formula of every key of the bdb bdbID.
func (r *DataAPIKeyFormulaRepo) DeleteByBdb(ctx context.Context, bdbID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM data_api_key_formulas WHERE bdb_id = $1`, bdbID)
	if err != nil {
		return fmt.Errorf("deleting a bdb's data API key formulas: %w", err)
	}
	return nil
}
