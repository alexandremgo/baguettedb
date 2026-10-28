package domain

import "time"

// SQLiteTimestampLayout matches the format SQLite stores via
// strftime('%Y-%m-%dT%H:%M:%fZ', 'now') across the platform tables
// (_change_log, _skills, _skill_targets, _formulas, ...).
const SQLiteTimestampLayout = "2006-01-02T15:04:05.000Z"

// ParseSQLiteTimestamp parses a SQLite TEXT timestamp into time.Time.
// Tolerates empty input and accepts RFC3339Nano as a fallback. Returns the
// zero time on failure — callers tolerate it because every DB-backed
// timestamp is non-zero in practice.
func ParseSQLiteTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(SQLiteTimestampLayout, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return time.Time{}
}

// FormatSQLiteTimestamp formats t for writing to a SQLite TEXT timestamp
// column. Mirrors strftime('%Y-%m-%dT%H:%M:%fZ', t).
func FormatSQLiteTimestamp(t time.Time) string {
	return t.UTC().Format(SQLiteTimestampLayout)
}
