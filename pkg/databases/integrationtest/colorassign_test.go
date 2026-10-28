//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// colorOf reads a database row's pod_color directly from Postgres.
func colorOf(t *testing.T, databaseID uuid.UUID) int16 {
	t.Helper()
	var color int16
	if err := testDB.QueryRow(
		context.Background(), "SELECT pod_color FROM databases WHERE id = $1", databaseID,
	).Scan(&color); err != nil {
		t.Fatalf("reading pod_color: %v", err)
	}
	return color
}

// TestColorAssignment_CreatorOwns verifies creator-owns stamps the creating pod's fleet color: a database created
// on a green pod (color 1) is owned by color 1, while the default (blue, 0) pod produces color 0. The shard
// ordinal is held equal so this isolates the color dimension.
func TestColorAssignment_CreatorOwns(t *testing.T) {
	ctx := context.Background()
	kek := newTestKEKProvider(t)
	dummyPeer := func(int16, int16) string { return "http://unused" }

	blue := newShardPod(t, 0, 0, kek, dummyPeer)  // color 0 (blue), ordinal 0
	green := newShardPod(t, 1, 0, kek, dummyPeer) // color 1 (green), ordinal 0

	blueDB, err := blue.dbSvc.CreateDatabase(ctx, uuid.New())
	if err != nil {
		t.Fatalf("create on blue pod: %v", err)
	}
	greenDB, err := green.dbSvc.CreateDatabase(ctx, uuid.New())
	if err != nil {
		t.Fatalf("create on green pod: %v", err)
	}

	if got := colorOf(t, blueDB); got != 0 {
		t.Errorf("db created on blue pod got pod_color %d, want 0", got)
	}
	if got := colorOf(t, greenDB); got != 1 {
		t.Errorf("db created on green pod got pod_color %d, want 1", got)
	}
}

// TestActiveColor_RoundTrip exercises the databases_control accessor: the migration seeds the active fleet to 0
// (blue); SetActiveColor flips it to 1 (green), GetActiveColor reads it back, and the singleton is reset to 0.
func TestActiveColor_RoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := newActiveColorRepo()

	got, err := repo.GetActiveColor(ctx)
	if err != nil {
		t.Fatalf("GetActiveColor (seeded): %v", err)
	}
	if got != 0 {
		t.Fatalf("seeded active color = %d, want 0 (blue)", got)
	}

	if err := repo.SetActiveColor(ctx, 1); err != nil {
		t.Fatalf("SetActiveColor(1): %v", err)
	}

	got, err = repo.GetActiveColor(ctx)
	if err != nil {
		t.Fatalf("GetActiveColor (after set): %v", err)
	}
	if got != 1 {
		t.Fatalf("active color after set = %d, want 1 (green)", got)
	}

	// Leaves the singleton back on blue so the row's state doesn't surprise a later reader.
	if err := repo.SetActiveColor(ctx, 0); err != nil {
		t.Fatalf("SetActiveColor(0) reset: %v", err)
	}
}
