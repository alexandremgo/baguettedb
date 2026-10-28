package api

import (
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// createBdbRequest holds the JSON body for bdb creation.
type createBdbRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// updateBdbRequest holds the JSON body for bdb updates. Both fields are optional: an omitted one leaves the
// stored value in place.
type updateBdbRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// reorderBdbsRequest holds the JSON body for rearranging the caller's bdb list. The ids are the caller's
// bdbs in the order they want to see them, first one at the top.
type reorderBdbsRequest struct {
	BdbIDs []uuid.UUID `json:"bdb_ids"`
}

// updateMemberRoleRequest holds the JSON body for updating a member's role.
type updateMemberRoleRequest struct {
	Role string `json:"role"`
}

// errorResponse represents an error in API responses.
type errorResponse struct {
	Error string `json:"error"`
}

// createInviteRequest holds the JSON body for creating a bdb invite.
type createInviteRequest struct {
	Role           string `json:"role"`
	ExpiresInHours int    `json:"expires_in_hours,omitempty"`
}

// inviteResponse represents an invite in API responses.
type inviteResponse struct {
	ID        uuid.UUID     `json:"id"`
	Token     string        `json:"token"`
	InviteURL string        `json:"invite_url"`
	Role      authz.BdbRole `json:"role"`
	Status    string        `json:"status"`
	ExpiresAt time.Time     `json:"expires_at"`
	CreatedAt time.Time     `json:"created_at"`
}

// inviteDetailsResponse represents invite details shown to the invitee before accepting.
type inviteDetailsResponse struct {
	ID             uuid.UUID     `json:"id"`
	BdbName        string        `json:"bdb_name"`
	BdbDescription string        `json:"bdb_description"`
	Role           authz.BdbRole `json:"role"`
	Status         string        `json:"status"`
	ExpiresAt      time.Time     `json:"expires_at"`
	CreatedByEmail string        `json:"created_by_email,omitempty"`
}

// toInviteResponse converts a domain Invite to an API response.
func toInviteResponse(inv *domain.Invite, baseURL string) inviteResponse {
	return inviteResponse{
		ID:        inv.ID,
		Token:     inv.Token,
		InviteURL: baseURL + "/invite/" + inv.Token,
		Role:      inv.Role,
		Status:    string(inv.Status),
		ExpiresAt: inv.ExpiresAt,
		CreatedAt: inv.CreatedAt,
	}
}

// toInviteDetailsResponse converts an invite and its bdb to a details response.
func toInviteDetailsResponse(inv *domain.Invite, bdb *domain.Bdb, creatorEmail string) inviteDetailsResponse {
	return inviteDetailsResponse{
		ID:             inv.ID,
		BdbName:        bdb.Name,
		BdbDescription: bdb.Description,
		Role:           inv.Role,
		Status:         string(inv.Status),
		ExpiresAt:      inv.ExpiresAt,
		CreatedByEmail: creatorEmail,
	}
}
