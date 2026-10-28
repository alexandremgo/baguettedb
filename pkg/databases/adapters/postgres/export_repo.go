package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/database"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// ExportRepo persists the audit trail of user-facing exports using Postgres.
type ExportRepo struct {
	db database.DBTX
}

// NewExportRepo creates a new ExportRepo backed by the given DBTX.
func NewExportRepo(db database.DBTX) *ExportRepo {
	return &ExportRepo{db: db}
}

// RecordStart inserts the audit row for a starting export attempt.
func (r *ExportRepo) RecordStart(ctx context.Context, rec domain.ExportRecord) error {
	_, err := r.db.Exec(ctx,
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
	tag, err := r.db.Exec(ctx,
		`UPDATE database_exports SET outcome = $1, bytes = $2, finished_at = NOW() WHERE id = $3`,
		outcome, bytes, id,
	)
	if err != nil {
		return fmt.Errorf("updating export record: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("export record %s not found", id)
	}
	return nil
}

// CountSuccessesSince returns how many exports of the given kind succeeded for (databaseID, userID) since the
// given time, plus the started_at of the oldest counted attempt (nil when none) — the inputs for the rolling
// rate-limit window and its retry-after computation.
func (r *ExportRepo) CountSuccessesSince(
	ctx context.Context, databaseID, userID uuid.UUID, kind domain.ExportKind, since time.Time,
) (int, *time.Time, error) {
	var count int
	var oldest *time.Time
	err := r.db.QueryRow(ctx,
		`SELECT COUNT(*), MIN(started_at) FROM database_exports
		 WHERE database_id = $1 AND user_id = $2 AND kind = $3 AND outcome = $4 AND started_at > $5`,
		databaseID, userID, kind, domain.ExportOutcomeSuccess, since,
	).Scan(&count, &oldest)
	if err != nil {
		return 0, nil, fmt.Errorf("counting export successes: %w", err)
	}
	return count, oldest, nil
}
