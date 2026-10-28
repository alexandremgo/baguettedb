package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// stubKEKProvider implements KEKProvider with real AES-256-GCM so tests can
// round-trip wrap → unwrap and assert that consecutive wraps differ (fresh nonce).
type stubKEKProvider struct {
	aead          cipher.AEAD
	wrapErr       error
	lastPlaintext []byte
}

func newStubKEKProvider(t *testing.T) *stubKEKProvider {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	return &stubKEKProvider{aead: aead}
}

func (s *stubKEKProvider) Wrap(plaintext []byte) ([]byte, error) {
	if s.wrapErr != nil {
		return nil, s.wrapErr
	}
	s.lastPlaintext = append([]byte(nil), plaintext...)
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(nonce)+len(plaintext)+s.aead.Overhead())
	out = append(out, nonce...)
	return s.aead.Seal(out, nonce, plaintext, nil), nil
}

func (s *stubKEKProvider) Unwrap(ciphertext []byte) ([]byte, error) {
	ns := s.aead.NonceSize()
	if len(ciphertext) < ns {
		return nil, errors.New("ciphertext too short")
	}
	return s.aead.Open(nil, ciphertext[:ns], ciphertext[ns:], nil)
}

// mockDatabaseRepo implements DatabaseRepository for testing.
type mockDatabaseRepo struct {
	db  *domain.Database
	err error
}

func (m *mockDatabaseRepo) Create(_ context.Context, db *domain.Database) error {
	m.db = db
	return m.err
}
func (m *mockDatabaseRepo) FindByID(_ context.Context, _ uuid.UUID) (*domain.Database, error) {
	return m.db, m.err
}
func (m *mockDatabaseRepo) FindByBdb(_ context.Context, _ uuid.UUID) (*domain.Database, error) {
	return m.db, m.err
}
func (m *mockDatabaseRepo) Delete(_ context.Context, _ uuid.UUID) error {
	return m.err
}
func (m *mockDatabaseRepo) UpdatePlatformSchemaVersion(_ context.Context, _ uuid.UUID, _ int) error {
	return m.err
}
func (m *mockDatabaseRepo) SetReadOnly(_ context.Context, _ uuid.UUID, _ bool) error {
	return m.err
}
func (m *mockDatabaseRepo) UpdateOwnerAndReadOnly(_ context.Context, _ uuid.UUID, _, _ int16, _ bool) error {
	return m.err
}
func (m *mockDatabaseRepo) UpdateSize(_ context.Context, _ uuid.UUID, sizeBytes int64) error {
	if m.db != nil {
		m.db.SizeBytes = sizeBytes
	}
	return m.err
}
func (m *mockDatabaseRepo) ListAll(_ context.Context) ([]*domain.Database, error) {
	if m.db == nil {
		return nil, m.err
	}
	return []*domain.Database{m.db}, m.err
}
func (m *mockDatabaseRepo) ListOwned(_ context.Context, color, shard int16) ([]*domain.Database, error) {
	if m.db == nil || !m.db.OwnedBy(color, shard) {
		return nil, m.err
	}
	return []*domain.Database{m.db}, m.err
}
func (m *mockDatabaseRepo) SumSizeBytesByBdbIDs(_ context.Context, _ []uuid.UUID) (int64, error) {
	return 0, m.err
}
func (m *mockDatabaseRepo) GetActiveColor(_ context.Context) (int16, error) {
	return 0, m.err
}

// mockDatabaseManager implements databaseManager for testing.
type mockDatabaseManager struct {
	createdDatabaseID string
	maxSizeBytes      int64
	maxSizeDatabaseID string
	createErr         error
	setMaxSizeErr     error
}

func (m *mockDatabaseManager) CreateDatabase(_ context.Context, databaseID string, _ []byte) error {
	m.createdDatabaseID = databaseID
	return m.createErr
}
func (m *mockDatabaseManager) SetDatabaseMaxSize(_ context.Context, databaseID string, maxSizeBytes int64) error {
	m.maxSizeDatabaseID = databaseID
	m.maxSizeBytes = maxSizeBytes
	return m.setMaxSizeErr
}
func (m *mockDatabaseManager) GetDatabaseStats(_ context.Context, _ string) (*domain.DatabaseStats, error) {
	return &domain.DatabaseStats{StorageBytesUsed: 5 * 1024 * 1024}, nil
}
func (m *mockDatabaseManager) DeleteDatabase(_ context.Context, _ string) error {
	return nil
}
func (m *mockDatabaseManager) EvictDatabase(_ context.Context, _ string) error {
	return nil
}

// mockQueryExecutor implements queryExecutor for testing.
type mockQueryExecutor struct{}

func (m *mockQueryExecutor) Execute(
	_ context.Context, _ string, _ string, _ []domain.TypedValue,
) (*domain.QueryResult, error) {
	return &domain.QueryResult{}, nil
}
func (m *mockQueryExecutor) ExecuteInternal(
	ctx context.Context, ns string, sql string, args []domain.TypedValue,
) (*domain.QueryResult, error) {
	return m.Execute(ctx, ns, sql, args)
}
func (m *mockQueryExecutor) ExecuteWithRowCap(
	ctx context.Context, ns string, sql string, args []domain.TypedValue, _ int,
) (*domain.QueryResult, error) {
	return m.Execute(ctx, ns, sql, args)
}
func (m *mockQueryExecutor) ExecuteTransaction(
	_ context.Context, _ string, _ []domain.SQLStatement,
) ([]*domain.QueryResult, error) {
	return nil, nil
}
func (m *mockQueryExecutor) ExecuteTransactionInternal(
	ctx context.Context, ns string, stmts []domain.SQLStatement,
) ([]*domain.QueryResult, error) {
	return m.ExecuteTransaction(ctx, ns, stmts)
}
func (m *mockQueryExecutor) WithConn(_ context.Context, _ string, fn func(h ParseHandle) error) error {
	return fn(nil) // tests don't classify against the conn
}
func (m *mockQueryExecutor) WithTx(
	ctx context.Context, _ string, _ bool, fn func(ctx context.Context, tx Tx) error,
) error {
	return fn(ctx, nil)
}

// mockBdbsApp implements BdbsApp for testing.
type mockBdbsApp struct {
	ownerRoles []authz.Role
	rolesErr   error
}

func (m *mockBdbsApp) CheckMemberPermission(
	_ context.Context, _, _ uuid.UUID, _ authz.DatabasePermission,
) (bool, error) {
	return true, nil
}
func (m *mockBdbsApp) GetBdbOwner(_ context.Context, _ uuid.UUID) (uuid.UUID, []authz.Role, error) {
	return uuid.Nil, m.ownerRoles, m.rolesErr
}
func (m *mockBdbsApp) GetOwnedBdbIDs(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

func TestCreateDatabase_SetsDatabaseMaxSize(t *testing.T) {
	tests := []struct {
		name          string
		ownerRoles    []authz.Role
		wantMaxSize   int64
		wantSetCalled bool
	}{
		{
			"regular user gets 10MB limit",
			[]authz.Role{authz.RoleUser},
			10 * 1024 * 1024,
			true,
		},
		{
			"admin gets no limit",
			[]authz.Role{authz.RoleUser, authz.RoleAdmin},
			0,
			false,
		},
	}

	limits := quota.Fixed(quota.Limits{
		MaxDBSizeBytes: 10 * 1024 * 1024,
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &mockDatabaseRepo{}
			dbMgr := &mockDatabaseManager{}
			bdbsApp := &mockBdbsApp{ownerRoles: tt.ownerRoles}

			svc := NewDatabaseService(repo, dbMgr, &mockQueryExecutor{}, bdbsApp, limits,
				newStubKEKProvider(t), zerolog.Nop())

			_, err := svc.CreateDatabase(context.Background(), uuid.New())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantSetCalled {
				if dbMgr.maxSizeBytes != tt.wantMaxSize {
					t.Errorf("SetDatabaseMaxSize called with %d bytes, want %d", dbMgr.maxSizeBytes, tt.wantMaxSize)
				}
				if dbMgr.maxSizeDatabaseID == "" {
					t.Error("SetDatabaseMaxSize was not called")
				}
			} else if dbMgr.maxSizeDatabaseID != "" {
				t.Error("SetDatabaseMaxSize should not have been called for admin")
			}
		})
	}
}

// TestCreateDatabase_WrapsDistinctDEKs verifies two consecutive CreateDatabase
// calls persist different wrapped blobs (fresh DEK + fresh nonce each time).
func TestCreateDatabase_WrapsDistinctDEKs(t *testing.T) {
	kek := newStubKEKProvider(t)
	bdbsApp := &mockBdbsApp{ownerRoles: []authz.Role{authz.RoleAdmin}}
	limits := quota.Fixed(quota.Limits{})

	repo1 := &mockDatabaseRepo{}
	svc1 := NewDatabaseService(repo1, &mockDatabaseManager{}, &mockQueryExecutor{}, bdbsApp,
		limits, kek, zerolog.Nop())
	if _, err := svc1.CreateDatabase(context.Background(), uuid.New()); err != nil {
		t.Fatalf("first CreateDatabase: %v", err)
	}
	wrapped1 := append([]byte(nil), repo1.db.EncryptionKeyWrapped...)

	repo2 := &mockDatabaseRepo{}
	svc2 := NewDatabaseService(repo2, &mockDatabaseManager{}, &mockQueryExecutor{}, bdbsApp,
		limits, kek, zerolog.Nop())
	if _, err := svc2.CreateDatabase(context.Background(), uuid.New()); err != nil {
		t.Fatalf("second CreateDatabase: %v", err)
	}
	wrapped2 := repo2.db.EncryptionKeyWrapped

	if len(wrapped1) == 0 || len(wrapped2) == 0 {
		t.Fatalf("expected both wrapped blobs to be populated, got %d and %d bytes", len(wrapped1), len(wrapped2))
	}
	if bytes.Equal(wrapped1, wrapped2) {
		t.Fatal("expected distinct wrapped blobs across two CreateDatabase calls")
	}
}

// TestCreateDatabase_WrappedBlobRoundTrips verifies the persisted blob
// unwraps back to the DEK that was passed to KEKProvider.Wrap.
func TestCreateDatabase_WrappedBlobRoundTrips(t *testing.T) {
	kek := newStubKEKProvider(t)
	repo := &mockDatabaseRepo{}
	bdbsApp := &mockBdbsApp{ownerRoles: []authz.Role{authz.RoleAdmin}}

	svc := NewDatabaseService(repo, &mockDatabaseManager{}, &mockQueryExecutor{}, bdbsApp,
		quota.Fixed(quota.Limits{}), kek, zerolog.Nop())
	if _, err := svc.CreateDatabase(context.Background(), uuid.New()); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}

	if len(repo.db.EncryptionKeyWrapped) == 0 {
		t.Fatal("expected wrapped DEK to be persisted on the database row")
	}

	unwrapped, err := kek.Unwrap(repo.db.EncryptionKeyWrapped)
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	if len(unwrapped) != domain.DEKSize {
		t.Fatalf("unwrapped DEK length = %d, want %d", len(unwrapped), domain.DEKSize)
	}
	if !bytes.Equal(unwrapped, kek.lastPlaintext) {
		t.Fatal("unwrapped DEK does not match the plaintext passed to Wrap")
	}
}

// TestCreateDatabase_WrapErrorAbortsBeforeSideEffects verifies that a Wrap
// failure aborts CreateDatabase before creating a database or persisting a row.
func TestCreateDatabase_WrapErrorAbortsBeforeSideEffects(t *testing.T) {
	kek := newStubKEKProvider(t)
	kek.wrapErr = errors.New("kek down")

	repo := &mockDatabaseRepo{}
	dbMgr := &mockDatabaseManager{}
	bdbsApp := &mockBdbsApp{ownerRoles: []authz.Role{authz.RoleAdmin}}

	svc := NewDatabaseService(
		repo, dbMgr, &mockQueryExecutor{}, bdbsApp, quota.Fixed(quota.Limits{}), kek, zerolog.Nop())

	_, err := svc.CreateDatabase(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("expected error when KEKProvider.Wrap fails")
	}
	if dbMgr.createdDatabaseID != "" {
		t.Error("expected no database creation after wrap failure")
	}
	if repo.db != nil {
		t.Error("expected no row insertion after wrap failure")
	}
}

// TestReadOnlyBlocks verifies a read-only database still serves the non-mutating permissions (read, export)
// while every mutating permission is blocked.
func TestReadOnlyBlocks(t *testing.T) {
	tests := []struct {
		name     string
		readOnly bool
		perm     authz.DatabasePermission
		want     bool
	}{
		{"read allowed on read-only", true, authz.DBPermRead, false},
		{"export allowed on read-only", true, authz.DBPermExport, false},
		{"write blocked on read-only", true, authz.DBPermWrite, true},
		{"manage schema blocked on read-only", true, authz.DBPermManageSchema, true},
		{"manage history blocked on read-only", true, authz.DBPermManageHistory, true},
		{"write allowed on writable", false, authz.DBPermWrite, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &domain.Database{ReadOnly: tt.readOnly}
			if got := readOnlyBlocks(db, tt.perm); got != tt.want {
				t.Errorf("readOnlyBlocks(readOnly=%t, %s) = %t, want %t", tt.readOnly, tt.perm, got, tt.want)
			}
		})
	}
}
