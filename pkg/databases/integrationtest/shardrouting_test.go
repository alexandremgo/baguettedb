//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/filekek"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlitelocal"
	dbapi "github.com/alexandremgo/baguettedb/pkg/databases/api"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// shardPod is one databases "pod": its own SQLite client + data dir (a distinct PVC), sharing the test's
// Postgres metadata and KEK with every other pod, plus an HTTP router carrying the shard-routing middleware.
type shardPod struct {
	dbSvc        *service.DatabaseService
	sqliteClient *sqlitelocal.Client
	router       http.Handler
}

// newShardPod builds a pod at the given ordinal. peerURL maps a shard ordinal to the base URL of the pod owning
// it (so this pod can forward); kek is shared across pods so any pod can unwrap any database's DEK.
func newShardPod(
	t *testing.T, color, ordinal int16, kek *filekek.Provider, peerURL func(color, ordinal int16) string,
) *shardPod {
	t.Helper()
	dbRepo := newDatabaseRepo()
	sqliteClient, dataDir := newTestSQLiteClient(t, dbRepo, kek)
	classifier := sqlitelocal.NewClassifier()
	limits := quota.Fixed(quota.Limits{MaxDBSizeBytes: 10 * 1024 * 1024, MaxStatementsPerRequest: 100,
		MaxRowsPerRequest: 1000})
	dbSvc := service.NewDatabaseService(dbRepo, sqliteClient, sqliteClient, &allowAllBdbsApp{}, limits, kek, testLogger)
	dbSvc.SetShardOrdinal(ordinal) // creator-owns: databases created here are assigned this pod's (color, ordinal)
	dbSvc.SetPodColor(color)
	querySvc := service.NewQueryService(dbSvc, sqliteClient, classifier, limits, 100, 0)
	skillSvc := service.NewSkillService(dbSvc, sqliteClient)
	formulaSvc := service.NewFormulaService(dbSvc, sqliteClient, classifier, querySvc, defaultTestFormulaRunTimeout,
		testLogger)

	tv, err := authhttp.NewTokenValidator(context.Background(), testJWKS.JWKSURL(), testJWKS.Issuer(), "", testLogger)
	if err != nil {
		t.Fatalf("token validator: %v", err)
	}
	t.Cleanup(tv.Close)

	exportSvc, err := service.NewExportService(dbSvc, formulaSvc, sqliteClient, sqliteClient,
		newExportRepo(), limits, filepath.Join(dataDir, ".exports-tmp"), 0, testLogger)
	if err != nil {
		t.Fatalf("creating export service: %v", err)
	}

	shardRouter := dbapi.NewShardRouter(color, ordinal, peerURL, dbSvc, testLogger)
	router := dbapi.NewRouter(
		dbapi.NewHandler(dbSvc, querySvc, testLogger),
		dbapi.NewSkillHandler(skillSvc, testLogger),
		dbapi.NewFormulaHandler(formulaSvc, testLogger),
		dbapi.NewExportHandler(exportSvc, testLogger),
		dbapi.NewInternalHandler(querySvc, skillSvc, formulaSvc, dbSvc, nil, testLogger),
		shardRouter, tv.RequireToken(), authz.Policy{}, nil, 1024*1024, testutil.InternalAPISecret,
	)
	return &shardPod{dbSvc: dbSvc, sqliteClient: sqliteClient, router: router}
}

// TestShardRouting_ForwardsToOwnerAndServesLocal verifies the following contract: a request for a bdb owned by
// another pod is transparently reverse-proxied to the owner and succeeds, while a request for a locally-owned
// bdb is served without forwarding.
func TestShardRouting_ForwardsToOwnerAndServesLocal(t *testing.T) {
	truncateAll(t)
	ctx := context.Background()
	kek := newTestKEKProvider(t)

	// pod1 owns shard 1. It never forwards in this test, so its peerURL is a placeholder.
	pod1 := newShardPod(t, 0, 1, kek, func(int16, int16) string { return "http://unused" })
	pod1Server := httptest.NewServer(pod1.router)
	t.Cleanup(pod1Server.Close)

	// pod0 owns shard 0 and forwards shard-1 traffic to pod1's real HTTP server.
	pod0 := newShardPod(t, 0, 0, kek, func(_, ordinal int16) string {
		if ordinal == 1 {
			return pod1Server.URL
		}
		return "http://unused"
	})

	// --- A bdb owned by shard 1, whose file lives on pod1 ---
	remoteBdb := uuid.New()
	remoteDB, err := pod1.dbSvc.CreateDatabase(ctx, remoteBdb) // file created in pod1's data dir, assigned shard 1
	if err != nil {
		t.Fatalf("creating remote database on pod1: %v", err)
	}
	for _, sql := range []string{
		"CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)",
		"INSERT INTO t (v) VALUES ('hello')",
	} {
		if _, err := pod1.sqliteClient.Execute(ctx, remoteDB.String(), sql, nil); err != nil {
			t.Fatalf("seeding remote db (%q): %v", sql, err)
		}
	}

	// Query it via pod0 — must be forwarded to pod1 and return the row.
	tok := userToken(t, uuid.New().String())
	status, resp := testutil.DoRequest(t, pod0.router, http.MethodPost, dbPath(remoteBdb.String(), "query"),
		tok, map[string]string{"sql": "SELECT v FROM t"})
	if status != http.StatusOK {
		t.Fatalf("forwarded query status = %d, want 200 (resp: %v)", status, resp)
	}
	if rows, ok := resp["rows"].([]any); !ok || len(rows) != 1 {
		t.Fatalf("forwarded query rows = %v, want 1 row", resp["rows"])
	}

	// --- A bdb owned by shard 0, whose file lives on pod0 ---
	localBdb := uuid.New()
	localDB, err := pod0.dbSvc.CreateDatabase(ctx, localBdb) // shard 0 by default
	if err != nil {
		t.Fatalf("creating local database on pod0: %v", err)
	}
	if _, err := pod0.sqliteClient.Execute(ctx, localDB.String(),
		"CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)", nil); err != nil {
		t.Fatalf("seeding local db: %v", err)
	}

	// Served locally by pod0 (no forward needed).
	status, resp = testutil.DoRequest(t, pod0.router, http.MethodPost, dbPath(localBdb.String(), "query"),
		tok, map[string]string{"sql": "SELECT count(*) FROM t"})
	if status != http.StatusOK {
		t.Fatalf("local query status = %d, want 200 (resp: %v)", status, resp)
	}
}

// TestShardRouting_InternalEndpointForwardsToOwner verifies the bdb-scoped internal endpoints (called over the
// load-balanced Service, with the bdb in the body) are shard-routed: tables-with-schemas for a shard-1 bdb
// sent to pod-0 is proxied to the owner and returns 200, while a non-owner serving it locally would 500 on the
// missing file. The owner serves it directly.
func TestShardRouting_InternalEndpointForwardsToOwner(t *testing.T) {
	truncateAll(t)
	ctx := context.Background()
	kek := newTestKEKProvider(t)

	pod1 := newShardPod(t, 0, 1, kek, func(int16, int16) string { return "http://unused" })
	pod1Server := httptest.NewServer(pod1.router)
	t.Cleanup(pod1Server.Close)

	pod0 := newShardPod(t, 0, 0, kek, func(_, ordinal int16) string {
		if ordinal == 1 {
			return pod1Server.URL
		}
		return "http://unused"
	})

	// A bdb owned by shard 1, with a table, whose file lives on pod1.
	bdbID := uuid.New()
	dbID, err := pod1.dbSvc.CreateDatabase(ctx, bdbID)
	if err != nil {
		t.Fatalf("creating database on pod1: %v", err)
	}
	if _, err := pod1.sqliteClient.Execute(ctx, dbID.String(),
		"CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT)", nil); err != nil {
		t.Fatalf("seeding pod1 db: %v", err)
	}

	internalBody := map[string]string{"user_id": uuid.New().String(), "bdb_id": bdbID.String()}

	// Entering via pod0 (non-owner): must forward to pod1 and succeed.
	status, resp := testutil.DoRequest(t, pod0.router, http.MethodPost,
		"/internal/api/databases/v1/tables-with-schemas", "", internalBody)
	if status != http.StatusOK {
		t.Fatalf("forwarded tables-with-schemas status = %d, want 200 (resp: %v)", status, resp)
	}

	// Entering via pod1 (owner): served locally.
	status, resp = testutil.DoRequest(t, pod1.router, http.MethodPost,
		"/internal/api/databases/v1/tables-with-schemas", "", internalBody)
	if status != http.StatusOK {
		t.Fatalf("owner-local tables-with-schemas status = %d, want 200 (resp: %v)", status, resp)
	}
}

// TestShardRouting_ForwardsAcrossColors verifies routing on the fleet-color dimension (blue/green), independent of
// shard ordinal: a bdb owned by the green pod (color 1, ordinal 0), queried via the blue pod (color 0, ordinal
// 0), is forwarded to green and returns its row; the blue pod serves its own bdb locally. This is the
// blue/green deploy's hand-off in miniature — same ordinal, different fleet.
func TestShardRouting_ForwardsAcrossColors(t *testing.T) {
	truncateAll(t)
	ctx := context.Background()
	kek := newTestKEKProvider(t)

	// green: color 1, ordinal 0 — never forwards here, so its peer URL is a placeholder.
	green := newShardPod(t, 1, 0, kek, func(int16, int16) string { return "http://unused" })
	greenServer := httptest.NewServer(green.router)
	t.Cleanup(greenServer.Close)

	// blue: color 0, ordinal 0 — forwards green-owned (color 1) traffic to green's real server.
	blue := newShardPod(t, 0, 0, kek, func(color, _ int16) string {
		if color == 1 {
			return greenServer.URL
		}
		return "http://unused"
	})

	// A bdb owned by green (color 1), whose file lives on green's volume.
	greenBdb := uuid.New()
	greenDB, err := green.dbSvc.CreateDatabase(ctx, greenBdb)
	if err != nil {
		t.Fatalf("creating green-owned database: %v", err)
	}
	for _, sql := range []string{
		"CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)",
		"INSERT INTO t (v) VALUES ('hello-green')",
	} {
		if _, err := green.sqliteClient.Execute(ctx, greenDB.String(), sql, nil); err != nil {
			t.Fatalf("seeding green db (%q): %v", sql, err)
		}
	}

	tok := userToken(t, uuid.New().String())

	// Query green's bdb via blue — must forward to green (same ordinal, different color) and return the row.
	status, resp := testutil.DoRequest(t, blue.router, http.MethodPost, dbPath(greenBdb.String(), "query"),
		tok, map[string]string{"sql": "SELECT v FROM t"})
	if status != http.StatusOK {
		t.Fatalf("cross-color forwarded query status = %d, want 200 (resp: %v)", status, resp)
	}
	if rows, ok := resp["rows"].([]any); !ok || len(rows) != 1 {
		t.Fatalf("cross-color forwarded query rows = %v, want 1 row", resp["rows"])
	}

	// A bdb owned by blue (color 0) is served locally without forwarding.
	blueBdb := uuid.New()
	blueDB, err := blue.dbSvc.CreateDatabase(ctx, blueBdb)
	if err != nil {
		t.Fatalf("creating blue-owned database: %v", err)
	}
	if _, err := blue.sqliteClient.Execute(ctx, blueDB.String(),
		"CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)", nil); err != nil {
		t.Fatalf("seeding blue db: %v", err)
	}
	status, resp = testutil.DoRequest(t, blue.router, http.MethodPost, dbPath(blueBdb.String(), "query"),
		tok, map[string]string{"sql": "SELECT count(*) FROM t"})
	if status != http.StatusOK {
		t.Fatalf("blue-local query status = %d, want 200 (resp: %v)", status, resp)
	}
}

// TestShardRouting_SteersNewBdbToActiveFleet verifies creation-steering: with green (color 1) the active fleet,
// a brand-new bdb first touched on a blue (non-active) pod is forwarded to green, so its lazily-created file
// lands on the live fleet instead of being stranded on the draining one. This is what makes a flip-first deploy
// safe — once active flips to green, new bdbs stop landing on blue.
func TestShardRouting_SteersNewBdbToActiveFleet(t *testing.T) {
	truncateAll(t)
	ctx := context.Background()
	kek := newTestKEKProvider(t)

	// Active fleet = green (color 1). Reset to the default afterwards so it doesn't leak to other tests.
	repo := newActiveColorRepo()
	if err := repo.SetActiveColor(ctx, 1); err != nil {
		t.Fatalf("setting active color: %v", err)
	}
	t.Cleanup(func() { _ = repo.SetActiveColor(context.Background(), 0) })

	// green pod (active), reachable for forwarding.
	green := newShardPod(t, 1, 0, kek, func(int16, int16) string { return "http://unused" })
	greenServer := httptest.NewServer(green.router)
	t.Cleanup(greenServer.Close)

	// blue pod (non-active) forwards new-bdb creation to green at the same ordinal.
	blue := newShardPod(t, 0, 0, kek, func(color, _ int16) string {
		if color == 1 {
			return greenServer.URL
		}
		return "http://unused"
	})

	// First touch of a brand-new bdb via blue: SELECT 1 lazily provisions the database. Steering must create it
	// on green, not blue.
	newBdb := uuid.New()
	tok := userToken(t, uuid.New().String())
	status, resp := testutil.DoRequest(t, blue.router, http.MethodPost, dbPath(newBdb.String(), "query"),
		tok, map[string]string{"sql": "SELECT 1"})
	if status != http.StatusOK {
		t.Fatalf("steered create status = %d, want 200 (resp: %v)", status, resp)
	}

	var color int16
	if err := testDB.QueryRow(ctx,
		"SELECT pod_color FROM databases WHERE bdb_id = $1", newBdb).Scan(&color); err != nil {
		t.Fatalf("reading new bdb's pod_color: %v", err)
	}
	if color != 1 {
		t.Errorf("new bdb created on color %d, want 1 (active/green fleet)", color)
	}
}
