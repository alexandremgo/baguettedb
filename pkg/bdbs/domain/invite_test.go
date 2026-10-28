package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

func TestNewInvite(t *testing.T) {
	bdbID := uuid.New()
	createdBy := uuid.New()
	future := time.Now().UTC().Add(72 * time.Hour)

	tests := []struct {
		name      string
		role      authz.BdbRole
		expiresAt time.Time
		wantErr   error
	}{
		{
			name:      "valid editor invite",
			role:      authz.BdbRoleEditor,
			expiresAt: future,
		},
		{
			name:      "valid viewer invite",
			role:      authz.BdbRoleViewer,
			expiresAt: future,
		},
		{
			name:      "owner role rejected",
			role:      authz.BdbRoleOwner,
			expiresAt: future,
			wantErr:   ErrCannotChangeOwnerRole,
		},
		{
			name:      "invalid role rejected",
			role:      authz.BdbRole("superadmin"),
			expiresAt: future,
			wantErr:   ErrInvalidRole,
		},
		{
			name:      "past expiration rejected",
			role:      authz.BdbRoleEditor,
			expiresAt: time.Now().UTC().Add(-1 * time.Hour),
			wantErr:   ErrInviteExpired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invite, err := NewInvite(bdbID, createdBy, tt.role, tt.expiresAt)

			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tt.wantErr)
				}
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("expected error %v, got %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if invite.BdbID != bdbID {
				t.Errorf("BdbID = %v, want %v", invite.BdbID, bdbID)
			}
			if invite.CreatedBy != createdBy {
				t.Errorf("CreatedBy = %v, want %v", invite.CreatedBy, createdBy)
			}
			if invite.Role != tt.role {
				t.Errorf("Role = %v, want %v", invite.Role, tt.role)
			}
			if invite.Status != InviteStatusPending {
				t.Errorf("Status = %v, want %v", invite.Status, InviteStatusPending)
			}
			if invite.Token == "" {
				t.Error("Token should not be empty")
			}
			if invite.ID == uuid.Nil {
				t.Error("ID should not be nil")
			}
		})
	}
}

func TestInvite_Accept(t *testing.T) {
	userID := uuid.New()

	tests := []struct {
		name    string
		invite  Invite
		wantErr error
	}{
		{
			name: "accept pending invite",
			invite: Invite{
				Status:    InviteStatusPending,
				ExpiresAt: time.Now().UTC().Add(1 * time.Hour),
			},
		},
		{
			name: "reject expired invite",
			invite: Invite{
				Status:    InviteStatusPending,
				ExpiresAt: time.Now().UTC().Add(-1 * time.Hour),
			},
			wantErr: ErrInviteExpired,
		},
		{
			name: "reject already accepted invite",
			invite: Invite{
				Status:    InviteStatusAccepted,
				ExpiresAt: time.Now().UTC().Add(1 * time.Hour),
			},
			wantErr: ErrInviteAlreadyUsed,
		},
		{
			name: "reject revoked invite",
			invite: Invite{
				Status:    InviteStatusRevoked,
				ExpiresAt: time.Now().UTC().Add(1 * time.Hour),
			},
			wantErr: ErrInviteRevoked,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.invite.Accept(userID)

			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tt.wantErr)
				}
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("expected error %v, got %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.invite.Status != InviteStatusAccepted {
				t.Errorf("Status = %v, want %v", tt.invite.Status, InviteStatusAccepted)
			}
			if tt.invite.AcceptedBy != userID {
				t.Errorf("AcceptedBy = %v, want %v", tt.invite.AcceptedBy, userID)
			}
			if tt.invite.AcceptedAt.IsZero() {
				t.Error("AcceptedAt should be set")
			}
		})
	}
}

func TestInvite_Revoke(t *testing.T) {
	tests := []struct {
		name    string
		invite  Invite
		wantErr error
	}{
		{
			name:   "revoke pending invite",
			invite: Invite{Status: InviteStatusPending},
		},
		{
			name:    "reject revoking accepted invite",
			invite:  Invite{Status: InviteStatusAccepted},
			wantErr: ErrInviteAlreadyUsed,
		},
		{
			name:    "reject revoking already revoked invite",
			invite:  Invite{Status: InviteStatusRevoked},
			wantErr: ErrInviteRevoked,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.invite.Revoke()

			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tt.wantErr)
				}
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("expected error %v, got %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.invite.Status != InviteStatusRevoked {
				t.Errorf("Status = %v, want %v", tt.invite.Status, InviteStatusRevoked)
			}
		})
	}
}

func TestInvite_IsUsable(t *testing.T) {
	tests := []struct {
		name   string
		invite Invite
		want   bool
	}{
		{
			name: "pending and not expired",
			invite: Invite{
				Status:    InviteStatusPending,
				ExpiresAt: time.Now().UTC().Add(1 * time.Hour),
			},
			want: true,
		},
		{
			name: "pending but expired",
			invite: Invite{
				Status:    InviteStatusPending,
				ExpiresAt: time.Now().UTC().Add(-1 * time.Hour),
			},
			want: false,
		},
		{
			name: "accepted",
			invite: Invite{
				Status:    InviteStatusAccepted,
				ExpiresAt: time.Now().UTC().Add(1 * time.Hour),
			},
			want: false,
		},
		{
			name: "revoked",
			invite: Invite{
				Status:    InviteStatusRevoked,
				ExpiresAt: time.Now().UTC().Add(1 * time.Hour),
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.invite.IsUsable()
			if got != tt.want {
				t.Errorf("IsUsable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGenerateInviteToken(t *testing.T) {
	token1, err := GenerateInviteToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(token1) == 0 {
		t.Fatal("token should not be empty")
	}

	token2, err := GenerateInviteToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token1 == token2 {
		t.Error("two generated tokens should differ")
	}
}
