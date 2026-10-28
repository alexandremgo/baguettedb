//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// mutableLimits holds limits a test can change between requests (the stack holds a pointer); its resolve
// method value is the quota.Resolver handed to the stack, delegating to quota.Fixed so role handling
// (admin = unlimited) matches production.
type mutableLimits struct {
	limits quota.Limits
}

func (m *mutableLimits) resolve(roles []authz.Role) quota.Limits {
	return quota.Fixed(m.limits)(roles)
}

// ownedSetBdbsApp is a BdbsApp fake with explicit ownership: every bdb belongs to ownerID, whose
// owned set and roles the test controls.
type ownedSetBdbsApp struct {
	ownerID    uuid.UUID
	ownerRoles []authz.Role
	ownedIDs   []uuid.UUID
}

func (a *ownedSetBdbsApp) CheckMemberPermission(
	_ context.Context, _, _ uuid.UUID, _ authz.DatabasePermission,
) (bool, error) {
	return true, nil
}

func (a *ownedSetBdbsApp) GetBdbOwner(_ context.Context, _ uuid.UUID) (uuid.UUID, []authz.Role, error) {
	return a.ownerID, a.ownerRoles, nil
}

func (a *ownedSetBdbsApp) GetOwnedBdbIDs(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return a.ownedIDs, nil
}

// TestStorageQuota_WritePathEnforcement pins the aggregate-cap semantics on the write path: with the
// owner at cap, size-growing writes are rejected 422 while reads, deletes, and drops stay allowed;
// removing the cap (or dropping data) unblocks writes again.
func TestStorageQuota_WritePathEnforcement(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	token := userToken(t, userID)
	bdbID := uuid.New()

	limits := &mutableLimits{limits: quota.Limits{
		MaxDBSizeBytes:          10 * 1024 * 1024,
		MaxStatementsPerRequest: 100,
		MaxRowsPerRequest:       1000,
	}}
	bdbsApp := &ownedSetBdbsApp{
		ownerID:    uuid.New(),
		ownerRoles: []authz.Role{authz.RoleUser},
		ownedIDs:   []uuid.UUID{bdbID},
	}
	router, _ := setupStackWithOptions(t, stackOptions{bdbsApp: bdbsApp, resolver: limits.resolve})

	// Seeds data while the aggregate cap is disabled.
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID.String(), "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, payload TEXT)"})
	if status != http.StatusOK {
		t.Fatalf("create table status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID.String(), "data"), token,
		map[string]string{"sql": "INSERT INTO items (payload) VALUES ('under-cap write')"})
	if status != http.StatusOK {
		t.Fatalf("under-cap insert status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	// 1 byte: the owner's tracked total (fresh, interval 0) is now far above it.
	limits.limits.MaxTotalDBSizeBytes = 1

	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID.String(), "data"), token,
		map[string]string{"sql": "INSERT INTO items (payload) VALUES ('over-cap write')"})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("over-cap insert status = %d, want %d, body: %v", status, http.StatusUnprocessableEntity, resp)
	}
	if msg, _ := resp["error"].(string); !strings.Contains(msg, "total storage quota") {
		t.Errorf("over-cap error = %q, want it to name the total storage quota", msg)
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID.String(), "data"), token,
		map[string]string{"sql": "UPDATE items SET payload = 'grown' WHERE id = 1"})
	if status != http.StatusUnprocessableEntity {
		t.Errorf("over-cap update status = %d, want %d, body: %v", status, http.StatusUnprocessableEntity, resp)
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID.String(), "query"), token,
		map[string]string{"sql": "SELECT COUNT(*) FROM items"})
	if status != http.StatusOK {
		t.Errorf("over-cap read status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID.String(), "data"), token,
		map[string]string{"sql": "DELETE FROM items WHERE id = 1"})
	if status != http.StatusOK {
		t.Errorf("over-cap delete status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID.String(), "schema"), token,
		map[string]string{"sql": "DROP TABLE items"})
	if status != http.StatusOK {
		t.Errorf("over-cap drop status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	// Lifting the cap unblocks growth (stands in for the owner's total shrinking below it).
	limits.limits.MaxTotalDBSizeBytes = 1024 * 1024 * 1024
	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID.String(), "schema"), token,
		map[string]string{"sql": "CREATE TABLE items2 (id INTEGER PRIMARY KEY)"})
	if status != http.StatusOK {
		t.Errorf("post-cap create status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
}

// TestStorageQuota_AdminBypassesCap pins that an admin owner is never capped: the resolver returns
// unlimited for admin roles regardless of the configured aggregate limit.
func TestStorageQuota_AdminBypassesCap(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	token := userToken(t, userID)
	bdbID := uuid.New()

	limits := &mutableLimits{limits: quota.Limits{
		MaxDBSizeBytes:          10 * 1024 * 1024,
		MaxTotalDBSizeBytes:     1,
		MaxStatementsPerRequest: 100,
		MaxRowsPerRequest:       1000,
	}}
	bdbsApp := &ownedSetBdbsApp{
		ownerID:    uuid.New(),
		ownerRoles: []authz.Role{authz.RoleUser, authz.RoleAdmin},
		ownedIDs:   []uuid.UUID{bdbID},
	}
	router, _ := setupStackWithOptions(t, stackOptions{bdbsApp: bdbsApp, resolver: limits.resolve})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID.String(), "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY)"})
	if status != http.StatusOK {
		t.Fatalf("admin create status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID.String(), "data"), token,
		map[string]string{"sql": "INSERT INTO items DEFAULT VALUES"})
	if status != http.StatusOK {
		t.Errorf("admin insert status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
}

// TestStorageQuota_CreationBlockedAtCap pins that a new database cannot be provisioned for an owner
// already at their aggregate cap: the auto-create on first touch is rejected 422 before any file exists.
func TestStorageQuota_CreationBlockedAtCap(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	token := userToken(t, userID)
	firstBdb := uuid.New()
	secondBdb := uuid.New()

	limits := &mutableLimits{limits: quota.Limits{
		MaxDBSizeBytes:          10 * 1024 * 1024,
		MaxStatementsPerRequest: 100,
		MaxRowsPerRequest:       1000,
	}}
	bdbsApp := &ownedSetBdbsApp{
		ownerID:    uuid.New(),
		ownerRoles: []authz.Role{authz.RoleUser},
		ownedIDs:   []uuid.UUID{firstBdb, secondBdb},
	}
	router, _ := setupStackWithOptions(t, stackOptions{bdbsApp: bdbsApp, resolver: limits.resolve})

	// Provisions the first database while the cap is disabled, putting the owner over the tiny cap.
	status, resp := testutil.DoRequest(t, router, http.MethodGet, dbPath(firstBdb.String(), ""), token, nil)
	if status != http.StatusOK {
		t.Fatalf("first auto-create status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	limits.limits.MaxTotalDBSizeBytes = 1

	status, resp = testutil.DoRequest(t, router, http.MethodGet, dbPath(secondBdb.String(), ""), token, nil)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("second auto-create status = %d, want %d, body: %v", status, http.StatusUnprocessableEntity, resp)
	}

	// The rejected bdb keeps working read-only paths: no database row was created for it.
	var count int
	if err := testDB.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM databases WHERE bdb_id = $1`, secondBdb).Scan(&count); err != nil {
		t.Fatalf("counting databases: %v", err)
	}
	if count != 0 {
		t.Errorf("databases rows for rejected bdb = %d, want 0", count)
	}
}

// TestStorageQuota_AccountStorageEndpoint pins the account surface: GET /account/storage returns the
// calling user's tracked total across their owned bdbs and their aggregate cap, resolved from the
// caller's own token roles.
func TestStorageQuota_AccountStorageEndpoint(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	token := userToken(t, userID)
	bdbID := uuid.New()

	limits := &mutableLimits{limits: quota.Limits{
		MaxDBSizeBytes:          10 * 1024 * 1024,
		MaxTotalDBSizeBytes:     50 * 1024 * 1024,
		MaxStatementsPerRequest: 100,
		MaxRowsPerRequest:       1000,
	}}
	bdbsApp := &ownedSetBdbsApp{
		ownerID:    uuid.New(),
		ownerRoles: []authz.Role{authz.RoleUser},
		ownedIDs:   []uuid.UUID{bdbID},
	}
	router, _ := setupStackWithOptions(t, stackOptions{bdbsApp: bdbsApp, resolver: limits.resolve})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID.String(), "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY)"})

	status, storage := testutil.DoRequest(t, router, http.MethodGet,
		"/api/databases/v1/account/storage", token, nil)
	if status != http.StatusOK {
		t.Fatalf("account storage status = %d, want %d, body: %v", status, http.StatusOK, storage)
	}

	totalUsed, ok := storage["total_storage_bytes_used"].(float64)
	if !ok || totalUsed <= 0 {
		t.Errorf("total_storage_bytes_used = %v, want > 0", storage["total_storage_bytes_used"])
	}
	if totalLimit := storage["total_storage_bytes_limit"]; totalLimit != float64(50*1024*1024) {
		t.Errorf("total_storage_bytes_limit = %v, want %d", totalLimit, 50*1024*1024)
	}

	// An admin token resolves to unlimited regardless of the configured cap.
	status, storage = testutil.DoRequest(t, router, http.MethodGet,
		"/api/databases/v1/account/storage", adminToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("admin account storage status = %d, want %d, body: %v", status, http.StatusOK, storage)
	}
	if totalLimit := storage["total_storage_bytes_limit"]; totalLimit != float64(0) {
		t.Errorf("admin total_storage_bytes_limit = %v, want 0 (unlimited)", totalLimit)
	}
}
