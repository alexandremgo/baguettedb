//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlitelocal"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// readOnlyStack groups the services needed to exercise read-only write rejection across both choke points:
// QueryService.Execute (user DML/DDL) and the AuthorizeAndResolveDatabase path (skills, undo/redo/revert).
type readOnlyStack struct {
	dbSvc    *service.DatabaseService
	querySvc *service.QueryService
	skillSvc *service.SkillService
}

func setupReadOnlyStack(t *testing.T) *readOnlyStack {
	t.Helper()
	dbRepo := newDatabaseRepo()
	backing := getOrCreateStackBacking(t, dbRepo)
	classifier := sqlitelocal.NewClassifier()
	limits := quota.Fixed(quota.Limits{
		MaxDBSizeBytes:          10 * 1024 * 1024,
		MaxStatementsPerRequest: 100,
		MaxRowsPerRequest:       1000,
	})
	dbSvc := service.NewDatabaseService(
		dbRepo, backing.sqliteClient, backing.sqliteClient, &allowAllBdbsApp{}, limits,
		backing.kekProvider, testLogger,
	)
	return &readOnlyStack{
		dbSvc:    dbSvc,
		querySvc: service.NewQueryService(dbSvc, backing.sqliteClient, classifier, limits, 100, 0),
		skillSvc: service.NewSkillService(dbSvc, backing.sqliteClient),
	}
}

// setReadOnly flips the read_only flag on a bdb's database row directly in Postgres — standing in for the
// rebalancer's release flow, which sets it in a later step.
func setReadOnly(t *testing.T, bdbID uuid.UUID, ro bool) {
	t.Helper()
	if err := testDB.Exec(
		context.Background(), "UPDATE databases SET read_only = $1 WHERE bdb_id = $2", ro, bdbID,
	); err != nil {
		t.Fatalf("setting read_only=%v: %v", ro, err)
	}
}

// TestReadOnly_RejectsWritesAllowsReads verifies that with read_only set, every mutating path is rejected with
// domain.ErrDatabaseReadOnly while reads still succeed — covering both enforcement choke points.
func TestReadOnly_RejectsWritesAllowsReads(t *testing.T) {
	s := setupReadOnlyStack(t)
	ctx := context.Background()
	userID := uuid.New()
	bdbID := uuid.New()

	// Provision while writable: a table + one row (the row gives undo something to act on).
	if _, err := s.querySvc.Execute(
		ctx, userID, bdbID, "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)", nil, domain.IntentSchema,
	); err != nil {
		t.Fatalf("CREATE TABLE (writable): %v", err)
	}
	if _, err := s.querySvc.Execute(
		ctx, userID, bdbID, "INSERT INTO t (v) VALUES ('seed')", nil, domain.IntentExecute,
	); err != nil {
		t.Fatalf("INSERT (writable): %v", err)
	}

	setReadOnly(t, bdbID, true)

	// Reads still work.
	if _, err := s.querySvc.Execute(
		ctx, userID, bdbID, "SELECT count(*) FROM t", nil, domain.IntentQuery,
	); err != nil {
		t.Errorf("SELECT under read_only should succeed, got: %v", err)
	}

	// Every write path is rejected with ErrDatabaseReadOnly.
	t.Run("DML rejected", func(t *testing.T) {
		_, err := s.querySvc.Execute(ctx, userID, bdbID, "INSERT INTO t (v) VALUES ('x')", nil, domain.IntentExecute)
		if !errors.Is(err, domain.ErrDatabaseReadOnly) {
			t.Errorf("err = %v, want ErrDatabaseReadOnly", err)
		}
	})
	t.Run("DDL rejected", func(t *testing.T) {
		_, err := s.querySvc.Execute(ctx, userID, bdbID, "CREATE TABLE t2 (id INTEGER)", nil, domain.IntentSchema)
		if !errors.Is(err, domain.ErrDatabaseReadOnly) {
			t.Errorf("err = %v, want ErrDatabaseReadOnly", err)
		}
	})
	t.Run("skill create rejected", func(t *testing.T) {
		_, err := s.skillSvc.CreateSkill(ctx, userID, bdbID, service.SkillInput{
			Name: "groceries", Description: "how to manage the list", Body: "Always check quantities.",
		})
		if !errors.Is(err, domain.ErrDatabaseReadOnly) {
			t.Errorf("err = %v, want ErrDatabaseReadOnly", err)
		}
	})
	t.Run("undo rejected", func(t *testing.T) {
		_, err := s.querySvc.UndoLastChange(ctx, userID, bdbID)
		if !errors.Is(err, domain.ErrDatabaseReadOnly) {
			t.Errorf("err = %v, want ErrDatabaseReadOnly", err)
		}
	})
}

// TestReadOnly_ClearingFlagRestoresWrites verifies the gate is exactly the flag: a write rejected while read_only
// succeeds once it is cleared (the rebalance "adopt complete" transition).
func TestReadOnly_ClearingFlagRestoresWrites(t *testing.T) {
	s := setupReadOnlyStack(t)
	ctx := context.Background()
	userID := uuid.New()
	bdbID := uuid.New()

	if _, err := s.querySvc.Execute(
		ctx, userID, bdbID, "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)", nil, domain.IntentSchema,
	); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	setReadOnly(t, bdbID, true)
	if _, err := s.querySvc.Execute(
		ctx, userID, bdbID, "INSERT INTO t (v) VALUES ('x')", nil, domain.IntentExecute,
	); !errors.Is(err, domain.ErrDatabaseReadOnly) {
		t.Fatalf("INSERT under read_only: err = %v, want ErrDatabaseReadOnly", err)
	}

	setReadOnly(t, bdbID, false)
	if _, err := s.querySvc.Execute(
		ctx, userID, bdbID, "INSERT INTO t (v) VALUES ('y')", nil, domain.IntentExecute,
	); err != nil {
		t.Errorf("INSERT after clearing read_only should succeed, got: %v", err)
	}
}

// TestReadOnly_HTTPReturnsRetryableStatus verifies the HTTP surface maps a read-only write to 503 with a
// retry-signalling body (the web/API client equivalent of the MCP retryable error).
func TestReadOnly_HTTPReturnsRetryableStatus(t *testing.T) {
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbUUID := uuid.New()
	bdbID := bdbUUID.String()
	tok := userToken(t, userID)

	if status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), tok,
		map[string]string{"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)"}); status != http.StatusOK {
		t.Fatalf("CREATE TABLE status = %d, want 200", status)
	}

	setReadOnly(t, bdbUUID, true)

	status, body := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), tok,
		map[string]string{"sql": "INSERT INTO t (v) VALUES ('x')"})
	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", status)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "read-only") {
		t.Errorf("error body = %q, want it to mention read-only", msg)
	}
}
