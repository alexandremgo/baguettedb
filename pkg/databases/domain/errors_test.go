package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestQueryExecutionError_IsErrQueryExecution(t *testing.T) {
	qe := &QueryExecutionError{
		Code:    sqliteCodeConstraintUnique,
		Message: "UNIQUE constraint failed: _skills.name",
	}

	if !errors.Is(qe, ErrQueryExecution) {
		t.Errorf("errors.Is(qe, ErrQueryExecution) = false, want true")
	}
}

func TestQueryExecutionError_AsExtractsCode(t *testing.T) {
	original := &QueryExecutionError{
		Code:    sqliteCodeConstraintForeignKey,
		Message: "FOREIGN KEY constraint failed",
	}

	// Wrap once to mimic typical error-propagation through fmt.Errorf("%w").
	wrapped := errors.Join(errors.New("doing something"), original)

	var qe *QueryExecutionError
	if !errors.As(wrapped, &qe) {
		t.Fatal("errors.As(wrapped, &qe) = false, want true")
	}
	if qe.Code != sqliteCodeConstraintForeignKey {
		t.Errorf("qe.Code = %d, want %d", qe.Code, sqliteCodeConstraintForeignKey)
	}
}

func TestQueryExecutionError_ErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		err  *QueryExecutionError
		want string
	}{
		{
			name: "with code",
			err: &QueryExecutionError{
				Code:    sqliteCodeConstraintUnique,
				Message: "UNIQUE constraint failed: _skills.name",
			},
			want: "UNIQUE constraint failed: _skills.name",
		},
		{
			name: "without code",
			err:  &QueryExecutionError{Message: "something broke"},
			want: "something broke",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if !strings.Contains(got, tt.err.Message) {
				t.Errorf("Error() = %q, expected to contain message %q", got, tt.err.Message)
			}
			if got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestQueryExecutionError_Predicates(t *testing.T) {
	tests := []struct {
		name           string
		code           int
		wantUnique     bool
		wantFK         bool
		wantBusy       bool
		wantUniqueErrs bool // errors.Is(err, ErrUniqueViolation)
	}{
		{name: "unique", code: 2067, wantUnique: true, wantUniqueErrs: true},
		{name: "fk", code: 787, wantFK: true},
		{name: "busy primary", code: 5, wantBusy: true},
		{name: "busy snapshot extended", code: 517, wantBusy: true},
		{name: "generic constraint", code: 19},
		{name: "ok-ish zero", code: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qe := &QueryExecutionError{Code: tt.code, Message: "x"}
			if got := qe.IsUniqueConstraint(); got != tt.wantUnique {
				t.Errorf("IsUniqueConstraint() = %v, want %v", got, tt.wantUnique)
			}
			if got := qe.IsFKConstraint(); got != tt.wantFK {
				t.Errorf("IsFKConstraint() = %v, want %v", got, tt.wantFK)
			}
			if got := qe.IsBusy(); got != tt.wantBusy {
				t.Errorf("IsBusy() = %v, want %v", got, tt.wantBusy)
			}
			if got := errors.Is(qe, ErrUniqueViolation); got != tt.wantUniqueErrs {
				t.Errorf("errors.Is(qe, ErrUniqueViolation) = %v, want %v", got, tt.wantUniqueErrs)
			}
			// ErrQueryExecution must always match for any QueryExecutionError.
			if !errors.Is(qe, ErrQueryExecution) {
				t.Errorf("errors.Is(qe, ErrQueryExecution) = false, want true")
			}
		})
	}
}

// TestQueryExecutionError_IsBlobUnsupported pins that SQLite's refusal to render a BLOB into JSON, the way a BLOB
// write fails inside the change-log triggers, is recognised as ErrBlobUnsupported, while other engine errors are not.
func TestQueryExecutionError_IsBlobUnsupported(t *testing.T) {
	blob := &QueryExecutionError{Code: 1, Message: "JSON cannot hold BLOB values"}
	if !errors.Is(blob, ErrBlobUnsupported) {
		t.Errorf("errors.Is(%v, ErrBlobUnsupported) = false, want true", blob)
	}
	if !errors.Is(blob, ErrQueryExecution) {
		t.Errorf("a BLOB refusal must still satisfy ErrQueryExecution")
	}
	other := &QueryExecutionError{Code: 1, Message: "no such table: items"}
	if errors.Is(other, ErrBlobUnsupported) {
		t.Errorf("errors.Is(%v, ErrBlobUnsupported) = true, want false", other)
	}
}

// TestQueryExecutionError_IsDatabaseFull pins that SQLite's SQLITE_FULL result — the bdb's page cap refusing a
// write — answers errors.Is for ErrDatabaseFull through the primary code, whatever extended bits SQLite attaches,
// and that other codes do not.
func TestQueryExecutionError_IsDatabaseFull(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		want bool
	}{
		{"SQLITE_FULL", 13, true},
		{"SQLITE_FULL with extended bits", 13 | (1 << 8), true},
		{"SQLITE_BUSY", 5, false},
		{"unique constraint", 2067, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := error(&QueryExecutionError{Code: tc.code, Message: "database or disk is full"})
			if got := errors.Is(err, ErrDatabaseFull); got != tc.want {
				t.Errorf("errors.Is(code %d, ErrDatabaseFull) = %v, want %v", tc.code, got, tc.want)
			}
			if !errors.Is(err, ErrQueryExecution) {
				t.Error("every QueryExecutionError must still satisfy ErrQueryExecution")
			}
		})
	}
}
