package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
)

func TestAuthenticationService_Authenticate(t *testing.T) {
	password := "testpassword123"
	hash, err := domain.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	activeUser := &domain.User{
		ID:           uuid.New(),
		Email:        "alice@example.com",
		PasswordHash: &hash,
		IsActive:     true,
	}

	inactiveUser := &domain.User{
		ID:           uuid.New(),
		Email:        "inactive@example.com",
		PasswordHash: &hash,
		IsActive:     false,
	}

	oidcOnlyUser := &domain.User{
		ID:           uuid.New(),
		Email:        "oidc@example.com",
		PasswordHash: nil,
		IsActive:     true,
	}

	tests := []struct {
		name     string
		email    string
		password string
		repo     *mockUserRepo
		wantErr  error
	}{
		{
			name:     "correct credentials",
			email:    "alice@example.com",
			password: password,
			repo:     &mockUserRepo{findByEmailUser: activeUser},
		},
		{
			name:     "wrong password",
			email:    "alice@example.com",
			password: "wrongpassword",
			repo:     &mockUserRepo{findByEmailUser: activeUser},
			wantErr:  domain.ErrInvalidCredentials,
		},
		{
			name:     "user not found",
			email:    "nobody@example.com",
			password: password,
			repo:     &mockUserRepo{findByEmailErr: domain.ErrUserNotFound},
			wantErr:  domain.ErrInvalidCredentials,
		},
		{
			name:     "inactive user",
			email:    "inactive@example.com",
			password: password,
			repo:     &mockUserRepo{findByEmailUser: inactiveUser},
			wantErr:  domain.ErrInvalidCredentials,
		},
		{
			name:     "OIDC-only user — no local password set",
			email:    "oidc@example.com",
			password: password,
			repo:     &mockUserRepo{findByEmailUser: oidcOnlyUser},
			wantErr:  domain.ErrInvalidCredentials,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewAuthenticationService(tt.repo)

			user, err := svc.Authenticate(context.Background(), tt.email, tt.password)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("Authenticate() error = %v, wantErr = %v", err, tt.wantErr)
				}
				if user != nil {
					t.Error("Authenticate() should return nil user on error")
				}
				return
			}

			if err != nil {
				t.Fatalf("Authenticate() unexpected error: %v", err)
			}
			if user == nil {
				t.Fatal("Authenticate() returned nil user")
			}
		})
	}
}
