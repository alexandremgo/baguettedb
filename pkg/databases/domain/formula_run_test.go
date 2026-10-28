package domain

import "testing"

// TestExpandRowIDsPlaceholder pins the bulk-selection rewrite: the lone :rowids
// placeholder becomes `count` comma-separated `?`, scalar placeholders are untouched,
// and a missing token / non-positive count / prefix-only match is an error.
func TestExpandRowIDsPlaceholder(t *testing.T) {
	tests := []struct {
		name    string
		sql     string
		count   int
		want    string
		wantErr bool
	}{
		{
			"three",
			"DELETE FROM groceries WHERE rowid IN (:rowids)", 3,
			"DELETE FROM groceries WHERE rowid IN (?, ?, ?)", false,
		},
		{
			"one",
			"DELETE FROM groceries WHERE rowid IN (:rowids)", 1,
			"DELETE FROM groceries WHERE rowid IN (?)", false,
		},
		{
			"scalar placeholder left untouched",
			"UPDATE groceries SET bought = 1 WHERE rowid IN (:rowids) AND name = :name", 2,
			"UPDATE groceries SET bought = 1 WHERE rowid IN (?, ?) AND name = :name", false,
		},
		{
			"missing placeholder",
			"DELETE FROM groceries WHERE rowid = :rowid", 3,
			"", true,
		},
		{
			"non-positive count",
			"DELETE FROM groceries WHERE rowid IN (:rowids)", 0,
			"", true,
		},
		{
			"prefix is not a match",
			"SELECT :rowidsx FROM groceries", 2,
			"", true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExpandRowIDsPlaceholder(tt.sql, tt.count)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ExpandRowIDsPlaceholder(%q, %d) = %q, want error", tt.sql, tt.count, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExpandRowIDsPlaceholder(%q, %d) unexpected error: %v", tt.sql, tt.count, err)
			}
			if got != tt.want {
				t.Errorf("ExpandRowIDsPlaceholder(%q, %d) = %q, want %q", tt.sql, tt.count, got, tt.want)
			}
		})
	}
}

// TestResolvePageLimit pins the page-size clamp: omitted or non-positive falls back to the
// default, an over-max request is clamped to the max, and an in-range request passes through.
func TestResolvePageLimit(t *testing.T) {
	ptr := func(n int64) *int64 { return &n }
	const def, maxLimit = 50, 200

	tests := []struct {
		name      string
		requested *int64
		want      int64
	}{
		{"omitted -> default", nil, def},
		{"zero -> default", ptr(0), def},
		{"negative -> default", ptr(-10), def},
		{"in range passes through", ptr(75), 75},
		{"exactly default", ptr(def), def},
		{"exactly max", ptr(maxLimit), maxLimit},
		{"over max -> clamped", ptr(1000), maxLimit},
		{"one is valid", ptr(1), 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolvePageLimit(tt.requested, def, maxLimit); got != tt.want {
				t.Errorf("ResolvePageLimit(%v, %d, %d) = %d, want %d", tt.requested, def, maxLimit, got, tt.want)
			}
		})
	}
}
