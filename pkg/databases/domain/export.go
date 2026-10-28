package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ExportRateLimitedError reports the rolling-window export limit being reached and how long until the window
// frees a slot. Unwraps to ErrExportRateLimited.
type ExportRateLimitedError struct {
	RetryAfter time.Duration
}

// Error returns the limit message with the rounded wait.
func (e *ExportRateLimitedError) Error() string {
	return fmt.Sprintf("export limit reached; retry in %s", e.RetryAfter.Round(time.Second))
}

// Unwrap ties the typed error to the ErrExportRateLimited sentinel.
func (e *ExportRateLimitedError) Unwrap() error { return ErrExportRateLimited }

// ExportKind identifies what a user-facing export produced.
type ExportKind string

const (
	// ExportKindDatabase is a full-database file export.
	ExportKindDatabase ExportKind = "database"

	// ExportKindFormulaCSV is a CSV rendering of a view formula's result set.
	ExportKindFormulaCSV ExportKind = "formula_csv"
)

// ExportOutcome is the state of an export attempt.
type ExportOutcome string

const (
	// ExportOutcomeStarted marks an export whose production or download is still in flight.
	ExportOutcomeStarted ExportOutcome = "started"

	// ExportOutcomeSuccess marks an export fully streamed to the client.
	ExportOutcomeSuccess ExportOutcome = "success"

	// ExportOutcomeFailed marks an export that errored during production or download.
	ExportOutcomeFailed ExportOutcome = "failed"
)

// ExportRecord is the audit trail of one user-facing export attempt: who exported what, when, and how it
// ended — never the exported content nor any key material. Successful records double as the rolling-window
// export rate-limit source.
type ExportRecord struct {
	ID         uuid.UUID
	DatabaseID uuid.UUID
	UserID     uuid.UUID
	Kind       ExportKind
	Outcome    ExportOutcome
	Bytes      int64
	StartedAt  time.Time
	FinishedAt *time.Time
}
