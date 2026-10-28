package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/database"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// ExportRepo persists the audit trail of user-facing exports using SQLite.
type ExportRepo struct {
	db database.SQLiteDBTX
}

// NewExportRepo creates a new ExportRepo backed by the given handle or transaction.
func NewExportRepo(db database.SQLiteDBTX) *ExportRepo {
	return &ExportRepo{db: db}
}

// RecordStart inserts the audit row for a starting export attempt.
func (r *ExportRepo) RecordStart(ctx context.Context, rec domain.ExportRecord) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO database_exports (id, database_id, user_id, kind, outcome, bytes, started_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		rec.ID, rec.DatabaseID, rec.UserID, rec.Kind, rec.Outcome, rec.Bytes, rec.StartedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting export record: %w", err)
	}
	return nil
}

// RecordOutcome sets the terminal outcome and streamed byte count of an export attempt, stamping its finish
// time.
func (r *ExportRepo) RecordOutcome(
	ctx context.Context, id uuid.UUID, outcome domain.ExportOutcome, bytes int64,
) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE database_exports SET outcome = $1, bytes = $2, finished_at = $3 WHERE id = $4`,
		outcome, bytes, time.Now(), id,
	)
	return database.RequireRow(res, err, "updating export record", fmt.Errorf("export record %s not found", id))
}

// CountSuccessesSince returns how many exports of the given kind succeeded for (databaseID, userID) since the
// given time, plus the started_at of the oldest counted attempt (nil when none) — the inputs for the rolling
// rate-limit window and its retry-after computation.
//
// The oldest attempt is read as the first row of the window rather than through MIN(started_at): SQLite reports no
// declared type for an aggregate, so the driver would return its text instead of a time.Time, while a plain column
// keeps its TIMESTAMP type. COUNT(*) OVER () carries the count on that same row.
func (r *ExportRepo) CountSuccessesSince(
	ctx context.Context, databaseID, userID uuid.UUID, kind domain.ExportKind, since time.Time,
) (int, *time.Time, error) {
	var count int
	var oldest time.Time
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) OVER (), started_at FROM database_exports
		 WHERE database_id = $1 AND user_id = $2 AND kind = $3 AND outcome = $4 AND started_at > $5
		 ORDER BY started_at
		 LIMIT 1`,
		databaseID, userID, kind, domain.ExportOutcomeSuccess, since,
	).Scan(&count, &oldest)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, nil
	}
	if err != nil {
		return 0, nil, fmt.Errorf("counting export successes: %w", err)
	}
	return count, &oldest, nil
}
