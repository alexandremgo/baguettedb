package apptypes

import "github.com/google/uuid"

// UserProfile is the non-credential profile of a user account.
type UserProfile struct {
	UserID    uuid.UUID `json:"user_id"`
	Email     string    `json:"email"`
	FirstName string    `json:"first_name"`
	LastName  string    `json:"last_name"`
}
