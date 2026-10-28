package testutil

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// TestPassword is a fixed plaintext password used by SeedUser.
// Tests that need to authenticate a seeded user can use this value.
const TestPassword = "testpassword123"

// precomputedHash is the bcrypt hash of TestPassword, computed once to avoid
// repeated ~200ms hashing calls in tests.
var precomputedHash string

func init() {
	h, err := bcrypt.GenerateFromPassword([]byte(TestPassword), bcrypt.DefaultCost)
	if err != nil {
		panic(fmt.Sprintf("hashing test password: %v", err))
	}
	precomputedHash = string(h)
}

// SeedUser inserts a user with the given roles directly into the users and user_roles tables, on either store.
// Returns the new user's UUID. Its password is TestPassword.
func (db *InstanceDB) SeedUser(t *testing.T, email string, roles []string) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	id := uuid.New()
	now := time.Now().UTC()

	err := db.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, first_name, last_name, created_at, updated_at, is_active)
		 VALUES ($1, $2, $3, 'Test', 'User', $4, $4, true)`,
		id, email, precomputedHash, now,
	)
	if err != nil {
		t.Fatalf("seeding user %s: %v", email, err)
	}

	for _, role := range roles {
		err := db.Exec(ctx,
			`INSERT INTO user_roles (user_id, role, created_at) VALUES ($1, $2, $3)`,
			id, role, now,
		)
		if err != nil {
			t.Fatalf("seeding role %s for user %s: %v", role, email, err)
		}
	}

	return id
}
