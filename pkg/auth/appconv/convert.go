// Package appconv converts the auth app's domain types into the cross-app transfer shapes of pkg/common/apptypes.
// It is behavior-free field mapping shared by every exit point where domain values leave the app — its internal
// endpoints and the in-process adapters of consuming apps — so a new field is mapped once.
package appconv

import (
	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
)

// UserProfile maps a user's non-credential profile.
func UserProfile(p *domain.UserProfile) apptypes.UserProfile {
	return apptypes.UserProfile{UserID: p.ID, Email: p.Email, FirstName: p.FirstName, LastName: p.LastName}
}

// DataAPIKey maps a Data API key without its value. A token bound to no bdb, which no Data API key is, maps to the nil
// bdb ID.
func DataAPIKey(t *domain.AccessToken) apptypes.DataAPIKey {
	key := apptypes.DataAPIKey{
		ID:          t.ID,
		UserID:      t.UserID,
		Name:        t.Name,
		TokenPrefix: t.TokenPrefix,
		CreatedAt:   t.CreatedAt,
		LastUsedAt:  t.LastUsedAt,
	}
	if t.BdbID != nil {
		key.BdbID = *t.BdbID
	}
	return key
}
