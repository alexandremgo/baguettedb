// Package appconv converts the bdbs app's domain types into the cross-app transfer shapes of pkg/common/apptypes.
// It is behavior-free field mapping shared by every exit point where domain values leave the app — its api layer and
// the in-process adapters of consuming apps — so a new field is mapped once. Member profiles come as the app's
// service resolves them from the auth app.
package appconv

import (
	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// Bdb maps a bdb as viewerID sees it: viewerID's role goes into MyRole, and each member is enriched with its entry
// in profiles when there is one (profiles may be nil or incomplete). Tables is left unset.
func Bdb(b *domain.Bdb, viewerID uuid.UUID, profiles map[uuid.UUID]*apptypes.UserProfile) apptypes.Bdb {
	members := make([]apptypes.BdbMember, len(b.Members))
	for i, m := range b.Members {
		members[i] = BdbMember(&m, profiles[m.UserID])
	}

	var myRole authz.BdbRole
	if m := b.FindMember(viewerID); m != nil {
		myRole = m.Role
	}

	var owner *apptypes.BdbMember
	if o := b.Owner(); o != nil {
		member := BdbMember(o, profiles[o.UserID])
		owner = &member
	}

	return apptypes.Bdb{
		ID:          b.ID,
		Name:        b.Name,
		Description: b.Description,
		MyRole:      myRole,
		Owner:       owner,
		Members:     members,
		CreatedAt:   b.CreatedAt,
		UpdatedAt:   b.UpdatedAt,
		DeletedAt:   b.DeletedAt,
		PurgeAt:     b.PurgeAt(),
	}
}

// BdbMember maps a member, enriched with profile when it is not nil.
func BdbMember(m *domain.Member, profile *apptypes.UserProfile) apptypes.BdbMember {
	member := apptypes.BdbMember{
		UserID:    m.UserID,
		Role:      m.Role,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
	if profile != nil {
		member.Email = profile.Email
		member.FirstName = profile.FirstName
		member.LastName = profile.LastName
	}
	return member
}
