package cli

import (
	"testing"
	"time"
)

// TestLastUsedLabel checks how tokens list words a token's last use: never for none, just now under a minute or in
// the future, then the whole minutes, hours or days elapsed, singular for one.
func TestLastUsedLabel(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time {
		at := now.Add(-d)
		return &at
	}
	for _, tc := range []struct {
		at   *time.Time
		want string
	}{
		{nil, "never"},
		{ago(0), "just now"},
		{ago(59 * time.Second), "just now"},
		{ago(-time.Hour), "just now"},
		{ago(time.Minute), "1 minute ago"},
		{ago(59*time.Minute + 59*time.Second), "59 minutes ago"},
		{ago(time.Hour), "1 hour ago"},
		{ago(23*time.Hour + 59*time.Minute), "23 hours ago"},
		{ago(24 * time.Hour), "1 day ago"},
		{ago(40 * 24 * time.Hour), "40 days ago"},
	} {
		if got := lastUsedLabel(tc.at, now); got != tc.want {
			t.Errorf("lastUsedLabel(%v, %v) = %q, want %q", tc.at, now, got, tc.want)
		}
	}
}

// TestExpiresLabel checks how tokens list words a token's expiry: never for none, expired once it has passed, and
// the local time to the minute before.
func TestExpiresLabel(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Minute), now.Add(36*time.Hour)
	for _, tc := range []struct {
		at   *time.Time
		want string
	}{
		{nil, "never"},
		{&now, "expired"},
		{&past, "expired"},
		{&future, future.Local().Format("2006-01-02 15:04")},
	} {
		if got := expiresLabel(tc.at, now); got != tc.want {
			t.Errorf("expiresLabel(%v) = %q, want %q", tc.at, got, tc.want)
		}
	}
}
