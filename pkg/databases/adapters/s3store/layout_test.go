package s3store

import (
	"testing"
	"time"
)

// TestSnapshotKeyLayout verifies the object-key layout, prefix normalization, and that key timestamps parse back
// to the original instant.
func TestSnapshotKeyLayout(t *testing.T) {
	takenAt := time.Date(2026, 6, 11, 8, 30, 15, 123456789, time.UTC)

	tests := []struct {
		name      string
		cfgPrefix string
		wantKey   string
	}{
		{
			name:      "no prefix",
			cfgPrefix: "",
			wantKey:   "snapshots/db-1/2026-06-11T08:30:15.123456789Z.db",
		},
		{
			name:      "prefix with surrounding slashes normalized",
			cfgPrefix: "/backups/",
			wantKey:   "backups/snapshots/db-1/2026-06-11T08:30:15.123456789Z.db",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewClient(Config{
				Endpoint: "http://localhost:1", Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s",
				Prefix: tt.cfgPrefix,
			})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			key := c.snapshotKey("db-1", takenAt)
			if key != tt.wantKey {
				t.Errorf("snapshotKey = %q, want %q", key, tt.wantKey)
			}

			parsed, ok := parseSnapshotKeyTime(key, c.databasePrefix("db-1"))
			if !ok {
				t.Fatalf("parseSnapshotKeyTime(%q) not ok", key)
			}
			if !parsed.Equal(takenAt) {
				t.Errorf("parsed time = %v, want %v", parsed, takenAt)
			}
		})
	}
}

// TestParseSnapshotKeyTime_RejectsForeignKeys verifies objects not following the snapshot layout are filtered out.
func TestParseSnapshotKeyTime_RejectsForeignKeys(t *testing.T) {
	const keyPrefix = "snapshots/db-1/"
	for _, key := range []string{
		"snapshots/db-1/not-a-timestamp.db",
		"snapshots/db-1/2026-06-11T08:30:15.123456789Z.db.tmp",
		"snapshots/db-1/nested/2026-06-11T08:30:15.123456789Z.db",
		"other/db-1/2026-06-11T08:30:15.123456789Z.db",
		"snapshots/db-1/2026-06-11T08:30:15Z.db", // trimmed nanoseconds — not the fixed-width layout
	} {
		if _, ok := parseSnapshotKeyTime(key, keyPrefix); ok {
			t.Errorf("parseSnapshotKeyTime(%q) = ok, want rejected", key)
		}
	}
}
