//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// countingBdbsApp is a BdbsApp stub whose owned-bdb-ids lookup returns a fixed ID list and counts calls,
// so tests can assert the cache short-circuits repeat lookups.
type countingBdbsApp struct {
	allowAllBdbsApp
	ownedIDs []uuid.UUID
	calls    int
}

func (a *countingBdbsApp) GetOwnedBdbIDs(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	a.calls++
	return a.ownedIDs, nil
}

// TestTotalOwnedSizeBytes pins the aggregate: the total sums the tracked sizes of exactly the owner's
// bdbs, and the owned-bdb-ids lookup is cached (repeat calls within the TTL hit the bdbs module once).
func TestTotalOwnedSizeBytes(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	token := userToken(t, userID)

	// Seeds three databases with data; the third stays outside the owned set.
	bdbIDs := make([]uuid.UUID, 3)
	for i := range bdbIDs {
		bdbIDs[i] = uuid.New()
		status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbIDs[i].String(), "schema"), token,
			map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, payload TEXT)"})
		if status != http.StatusOK {
			t.Fatalf("create table status = %d, want %d, body: %v", status, http.StatusOK, resp)
		}
	}

	sizeA, _ := readStoredSize(t, bdbIDs[0].String())
	sizeB, _ := readStoredSize(t, bdbIDs[1].String())
	sizeC, _ := readStoredSize(t, bdbIDs[2].String())
	if sizeA <= 0 || sizeB <= 0 || sizeC <= 0 {
		t.Fatalf("tracked sizes = %d, %d, %d, want all > 0", sizeA, sizeB, sizeC)
	}

	dbRepo := newDatabaseRepo()
	backing := getOrCreateStackBacking(t, dbRepo)
	bdbsApp := &countingBdbsApp{ownedIDs: bdbIDs[:2]}
	dbSvc := service.NewDatabaseService(
		dbRepo, backing.sqliteClient, backing.sqliteClient, bdbsApp, quota.Fixed(quota.Limits{}),
		backing.kekProvider, testLogger,
	)

	ownerID := uuid.New()
	total, err := dbSvc.TotalOwnedSizeBytes(context.Background(), ownerID)
	if err != nil {
		t.Fatalf("TotalOwnedSizeBytes: %v", err)
	}
	if want := sizeA + sizeB; total != want {
		t.Errorf("total = %d, want %d (sum of the two owned bdbs, third excluded)", total, want)
	}

	// A repeat aggregate within the TTL reuses the cached ID list.
	if _, err := dbSvc.TotalOwnedSizeBytes(context.Background(), ownerID); err != nil {
		t.Fatalf("TotalOwnedSizeBytes (cached): %v", err)
	}
	if bdbsApp.calls != 1 {
		t.Errorf("owned-bdb-ids calls after cached repeat = %d, want 1", bdbsApp.calls)
	}

	// TTL 0 disables caching: the next aggregate reads through.
	dbSvc.SetOwnershipCacheTTL(0)
	if _, err := dbSvc.TotalOwnedSizeBytes(context.Background(), ownerID); err != nil {
		t.Fatalf("TotalOwnedSizeBytes (read-through): %v", err)
	}
	if bdbsApp.calls != 2 {
		t.Errorf("owned-bdb-ids calls after TTL 0 = %d, want 2", bdbsApp.calls)
	}
}

// TestTotalOwnedSizeBytes_NoOwnedBdbs pins the empty case: a user owning nothing sums to 0 without
// touching Postgres with an empty ANY clause.
func TestTotalOwnedSizeBytes_NoOwnedBdbs(t *testing.T) {
	truncateAll(t)
	dbRepo := newDatabaseRepo()
	backing := getOrCreateStackBacking(t, dbRepo)
	dbSvc := service.NewDatabaseService(
		dbRepo, backing.sqliteClient, backing.sqliteClient, &countingBdbsApp{}, quota.Fixed(quota.Limits{}),
		backing.kekProvider, testLogger,
	)

	total, err := dbSvc.TotalOwnedSizeBytes(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("TotalOwnedSizeBytes: %v", err)
	}
	if total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
}
