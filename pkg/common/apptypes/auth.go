package apptypes

import (
	"time"

	"github.com/google/uuid"
)

// UserProfile is the non-credential profile of a user account.
type UserProfile struct {
	UserID    uuid.UUID `json:"user_id"`
	Email     string    `json:"email"`
	FirstName string    `json:"first_name"`
	LastName  string    `json:"last_name"`
}

// DataAPIKey is a Data API key of a bdb, never its value: the bdb it is bound to, the user who created it and whom it
// acts as, its name, and the characters following its kind's prefix that tell it apart in a listing. LastUsedAt is nil
// until the key is first used.
type DataAPIKey struct {
	ID          uuid.UUID  `json:"id"`
	BdbID       uuid.UUID  `json:"bdb_id"`
	UserID      uuid.UUID  `json:"user_id"`
	Name        string     `json:"name"`
	TokenPrefix string     `json:"token_prefix"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
}

// CreatedDataAPIKey is a Data API key just created, with its plain value: the only shape that carries it, since the
// value is not stored and can't be shown again.
type CreatedDataAPIKey struct {
	Key   DataAPIKey `json:"key"`
	Value string     `json:"value"`
}
