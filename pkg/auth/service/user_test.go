package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
)

func TestUserService_GetProfile(t *testing.T) {
	userID := uuid.New()
	user := &domain.User{ID: userID, Email: "alice@example.com", IsActive: true}

	tests := []struct {
		name    string
		repo    *mockUserRepo
		wantErr error
	}{
		{
			name: "found",
			repo: &mockUserRepo{findByIDUser: user},
		},
		{
			name:    "not found",
			repo:    &mockUserRepo{findByIDErr: domain.ErrUserNotFound},
			wantErr: domain.ErrUserNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewUserService(tt.repo, nil, zerolog.Nop())

			result, err := svc.GetProfile(context.Background(), userID)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("GetProfile() error = %v, wantErr = %v", err, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("GetProfile() unexpected error: %v", err)
			}
			if result.ID != userID {
				t.Errorf("GetProfile() user ID = %v, want %v", result.ID, userID)
			}
		})
	}
}

func TestUserService_GetProfiles(t *testing.T) {
	id1 := uuid.New()
	id2 := uuid.New()
	id3 := uuid.New()

	t.Run("empty input returns nil without repo call", func(t *testing.T) {
		repo := &mockUserRepo{}
		svc := NewUserService(repo, nil, zerolog.Nop())

		got, err := svc.GetProfiles(context.Background(), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("expected nil result, got %v", got)
		}
		if repo.findByIDsCalled {
			t.Error("repo.FindByIDs should not have been called for empty input")
		}
	})

	t.Run("unique IDs pass through unchanged", func(t *testing.T) {
		profiles := []*domain.UserProfile{
			{ID: id1, Email: "a@example.com"},
			{ID: id2, Email: "b@example.com"},
		}
		repo := &mockUserRepo{findByIDsProfiles: profiles}
		svc := NewUserService(repo, nil, zerolog.Nop())

		got, err := svc.GetProfiles(context.Background(), []uuid.UUID{id1, id2})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("expected 2 profiles, got %d", len(got))
		}
		if len(repo.findByIDsArg) != 2 {
			t.Errorf("expected repo called with 2 IDs, got %d", len(repo.findByIDsArg))
		}
	})

	t.Run("duplicate IDs are deduped before repo call", func(t *testing.T) {
		repo := &mockUserRepo{}
		svc := NewUserService(repo, nil, zerolog.Nop())

		_, err := svc.GetProfiles(context.Background(), []uuid.UUID{id1, id2, id1, id3, id2})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(repo.findByIDsArg) != 3 {
			t.Errorf("expected repo called with 3 deduped IDs, got %d: %v", len(repo.findByIDsArg), repo.findByIDsArg)
		}
		seen := make(map[uuid.UUID]int)
		for _, id := range repo.findByIDsArg {
			seen[id]++
		}
		for id, count := range seen {
			if count != 1 {
				t.Errorf("ID %v appeared %d times in deduped slice, expected 1", id, count)
			}
		}
	})

	t.Run("repo error is propagated", func(t *testing.T) {
		repoErr := errors.New("db down")
		repo := &mockUserRepo{findByIDsErr: repoErr}
		svc := NewUserService(repo, nil, zerolog.Nop())

		_, err := svc.GetProfiles(context.Background(), []uuid.UUID{id1})
		if !errors.Is(err, repoErr) {
			t.Errorf("expected error %v, got %v", repoErr, err)
		}
	})
}

func TestUserService_CreateUser(t *testing.T) {
	tests := []struct {
		name    string
		email   string
		pass    string
		tx      UserTx
		wantErr error
	}{
		{
			name:  "success",
			email: "new@example.com",
			pass:  "strongpass123",
			tx:    UserTx{Users: &mockUserRepo{}, Roles: &mockRoleRepo{}},
		},
		{
			name:    "invalid email",
			email:   "not-an-email",
			pass:    "strongpass123",
			tx:      UserTx{Users: &mockUserRepo{}, Roles: &mockRoleRepo{}},
			wantErr: domain.ErrInvalidEmail,
		},
		{
			name:    "weak password",
			email:   "weak@example.com",
			pass:    "short",
			tx:      UserTx{Users: &mockUserRepo{}, Roles: &mockRoleRepo{}},
			wantErr: domain.ErrPasswordTooWeak,
		},
		{
			name:  "duplicate email",
			email: "dup@example.com",
			pass:  "strongpass123",
			tx: UserTx{
				Users: &mockUserRepo{createErr: domain.ErrEmailAlreadyExists},
				Roles: &mockRoleRepo{},
			},
			wantErr: domain.ErrEmailAlreadyExists,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewUserService(&mockUserRepo{}, runWith(tt.tx), zerolog.Nop())

			user, err := svc.CreateUser(context.Background(), tt.email, tt.pass, "Test", "User")
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("CreateUser() error = %v, wantErr = %v", err, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("CreateUser() unexpected error: %v", err)
			}
			if user.Email != tt.email {
				t.Errorf("CreateUser() email = %v, want %v", user.Email, tt.email)
			}
			if user.ID == uuid.Nil {
				t.Error("CreateUser() expected non-nil ID")
			}
		})
	}
}

func TestUserService_ChangePassword(t *testing.T) {
	oldPassword := "oldpassword123"
	hash, err := domain.HashPassword(oldPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	userID := uuid.New()
	user := &domain.User{ID: userID, PasswordHash: &hash, IsActive: true}
	oidcOnlyUser := &domain.User{ID: userID, PasswordHash: nil, IsActive: true}

	tests := []struct {
		name        string
		oldPassword string
		newPassword string
		repo        *mockUserRepo
		wantErr     error
	}{
		{
			name:        "successful change",
			oldPassword: oldPassword,
			newPassword: "newstrongpassword",
			repo:        &mockUserRepo{findByIDUser: user},
		},
		{
			name:        "wrong old password",
			oldPassword: "wrongpassword",
			newPassword: "newstrongpassword",
			repo:        &mockUserRepo{findByIDUser: user},
			wantErr:     domain.ErrInvalidCredentials,
		},
		{
			name:        "new password too weak",
			oldPassword: oldPassword,
			newPassword: "short",
			repo:        &mockUserRepo{findByIDUser: user},
			wantErr:     domain.ErrPasswordTooWeak,
		},
		{
			name:        "user not found",
			oldPassword: oldPassword,
			newPassword: "newstrongpassword",
			repo:        &mockUserRepo{findByIDErr: domain.ErrUserNotFound},
			wantErr:     domain.ErrUserNotFound,
		},
		{
			name:        "OIDC-only user — no local password to change",
			oldPassword: oldPassword,
			newPassword: "newstrongpassword",
			repo:        &mockUserRepo{findByIDUser: oidcOnlyUser},
			wantErr:     domain.ErrInvalidCredentials,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessions := &mockSessionRepo{}
			svc := NewUserService(tt.repo, runWith(UserTx{Users: tt.repo, Sessions: sessions}), zerolog.Nop())

			err := svc.ChangePassword(context.Background(), userID, tt.oldPassword, tt.newPassword, true)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("ChangePassword() error = %v, wantErr = %v", err, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("ChangePassword() unexpected error: %v", err)
			}
			if len(sessions.revokedFor) != 1 || sessions.revokedFor[0] != userID {
				t.Errorf("sessions revoked for %v, want for %v alone", sessions.revokedFor, userID)
			}
		})
	}
}
