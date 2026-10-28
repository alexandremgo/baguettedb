package intraprocess

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	authdomain "github.com/alexandremgo/baguettedb/pkg/auth/domain"
	authservice "github.com/alexandremgo/baguettedb/pkg/auth/service"
	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// usersPageSize is how many users ListUsers reads per page.
const usersPageSize = 100

// AuthApp adapts the auth app's user, role and token services, and converts their users and tokens into domain.User
// and domain.AccessToken. It manages every account, acting as the instance itself rather than as one of them.
type AuthApp struct {
	userSvc  *authservice.UserService
	roleSvc  *authservice.RoleService
	tokenSvc *authservice.TokenService
}

// NewAuthApp creates an AuthApp over the auth app's services.
func NewAuthApp(
	userSvc *authservice.UserService, roleSvc *authservice.RoleService, tokenSvc *authservice.TokenService,
) *AuthApp {
	return &AuthApp{userSvc: userSvc, roleSvc: roleSvc, tokenSvc: tokenSvc}
}

// CreateUser creates an account with password, none when it is empty, an admin one when admin is set, and returns
// it. An email already in use wraps domain.ErrUserExists, an invalid one domain.ErrInvalidEmail, a weak password
// domain.ErrWeakPassword.
func (a *AuthApp) CreateUser(
	ctx context.Context, email, firstName, lastName, password string, admin bool,
) (domain.User, error) {
	user, err := a.userSvc.CreateAccount(ctx, email, firstName, lastName, password, admin)
	if err != nil {
		return domain.User{}, toAccountError(err, email)
	}
	return toUser(user, admin), nil
}

// ListUsers returns every account, in the order the auth app lists them.
func (a *AuthApp) ListUsers(ctx context.Context) ([]domain.User, error) {
	var users []domain.User
	for offset := 0; ; offset += usersPageSize {
		page, total, err := a.userSvc.ListUsers(ctx, "", usersPageSize, offset)
		if err != nil {
			return nil, err
		}
		for _, u := range page {
			roles, err := a.roleSvc.GetUserRoles(ctx, u.ID)
			if err != nil {
				return nil, fmt.Errorf("reading the roles of %s: %w", u.Email, err)
			}
			users = append(users, toUser(u, slices.Contains(roles, authz.RoleAdmin)))
		}
		if len(page) == 0 || offset+len(page) >= total {
			return users, nil
		}
	}
}

// FindUserByEmail returns the account with email. None wraps domain.ErrUserNotFound.
func (a *AuthApp) FindUserByEmail(ctx context.Context, email string) (domain.User, error) {
	user, err := a.userSvc.FindByEmail(ctx, email)
	if errors.Is(err, authdomain.ErrUserNotFound) {
		return domain.User{}, fmt.Errorf("%w with email %s", domain.ErrUserNotFound, email)
	}
	if err != nil {
		return domain.User{}, err
	}
	roles, err := a.roleSvc.GetUserRoles(ctx, user.ID)
	if err != nil {
		return domain.User{}, fmt.Errorf("reading the roles of %s: %w", user.Email, err)
	}
	return toUser(user, slices.Contains(roles, authz.RoleAdmin)), nil
}

// ActAs returns the account with email for adapters to act as, with its roles, and the account itself. None wraps
// domain.ErrUserNotFound.
func (a *AuthApp) ActAs(ctx context.Context, email string) (Account, domain.User, error) {
	user, err := a.userSvc.FindByEmail(ctx, email)
	if errors.Is(err, authdomain.ErrUserNotFound) {
		return Account{}, domain.User{}, fmt.Errorf("%w with email %s", domain.ErrUserNotFound, email)
	}
	if err != nil {
		return Account{}, domain.User{}, err
	}
	roles, err := a.roleSvc.GetUserRoles(ctx, user.ID)
	if err != nil {
		return Account{}, domain.User{}, fmt.Errorf("reading the roles of %s: %w", user.Email, err)
	}
	return Account{ID: user.ID, Roles: roles}, toUser(user, slices.Contains(roles, authz.RoleAdmin)), nil
}

// FindUserByID returns the account id. None wraps domain.ErrUserNotFound.
func (a *AuthApp) FindUserByID(ctx context.Context, id uuid.UUID) (domain.User, error) {
	user, err := a.userSvc.GetProfile(ctx, id)
	if errors.Is(err, authdomain.ErrUserNotFound) {
		return domain.User{}, fmt.Errorf("%w %s", domain.ErrUserNotFound, id)
	}
	if err != nil {
		return domain.User{}, err
	}
	roles, err := a.roleSvc.GetUserRoles(ctx, user.ID)
	if err != nil {
		return domain.User{}, fmt.Errorf("reading the roles of %s: %w", user.Email, err)
	}
	return toUser(user, slices.Contains(roles, authz.RoleAdmin)), nil
}

// UpdateUser sets the email and names of the account id, and its password unless password is empty, ending its web
// sessions with a new password when endSessions is set: all of it or nothing. An email another account has wraps
// domain.ErrUserExists, an invalid one domain.ErrInvalidEmail, a weak password domain.ErrWeakPassword.
func (a *AuthApp) UpdateUser(
	ctx context.Context, id uuid.UUID, email, firstName, lastName, password string, endSessions bool,
) error {
	return toAccountError(a.userSvc.UpdateAccount(ctx, id, email, firstName, lastName, password, endSessions), email)
}

// SetPassword sets the password of the account id, whatever its current one, and ends its web sessions with it when
// endSessions is set. A weak password wraps domain.ErrWeakPassword.
func (a *AuthApp) SetPassword(ctx context.Context, id uuid.UUID, password string, endSessions bool) error {
	return toAccountError(a.userSvc.SetPassword(ctx, id, password, endSessions), "")
}

// CheckPasswordStrength returns nil for a password strong enough to set, an error wrapping domain.ErrWeakPassword
// otherwise.
func (a *AuthApp) CheckPasswordStrength(password string) error {
	return toAccountError(authdomain.ValidatePasswordStrength(password), "")
}

// OpenSessions returns how many web sessions of the account id are open: neither ended nor expired.
func (a *AuthApp) OpenSessions(ctx context.Context, id uuid.UUID) (int, error) {
	return a.tokenSvc.OpenSessions(ctx, id)
}

// CreateToken creates a personal access token named name for the account userID, expiring at expiresAt (nil for
// never), and returns its value, which can't be read again, with the token.
func (a *AuthApp) CreateToken(
	ctx context.Context, userID uuid.UUID, name string, expiresAt *time.Time,
) (string, domain.AccessToken, error) {
	plain, token, err := a.tokenSvc.Create(ctx, userID, name, expiresAt)
	if err != nil {
		return "", domain.AccessToken{}, err
	}
	return plain, toAccessToken(token), nil
}

// ListTokens returns the account's personal access tokens that are not revoked, newest first.
func (a *AuthApp) ListTokens(ctx context.Context, userID uuid.UUID) ([]domain.AccessToken, error) {
	tokens, err := a.tokenSvc.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.AccessToken, len(tokens))
	for i, t := range tokens {
		out[i] = toAccessToken(t)
	}
	return out, nil
}

// RevokeToken revokes the account's token id. A token the account doesn't have, or already revoked, wraps
// domain.ErrTokenNotFound.
func (a *AuthApp) RevokeToken(ctx context.Context, userID, id uuid.UUID) error {
	err := a.tokenSvc.Revoke(ctx, userID, id)
	if errors.Is(err, authdomain.ErrAccessTokenNotFound) {
		return fmt.Errorf("%w %s", domain.ErrTokenNotFound, id)
	}
	return err
}

// toAccountError converts err, from creating or changing the account with email, into the command line's errors: an
// email already in use wraps domain.ErrUserExists, an invalid one domain.ErrInvalidEmail, a weak password
// domain.ErrWeakPassword; any other is returned as is.
func toAccountError(err error, email string) error {
	switch {
	case errors.Is(err, authdomain.ErrEmailAlreadyExists):
		return fmt.Errorf("%w: %s", domain.ErrUserExists, email)
	case errors.Is(err, authdomain.ErrInvalidEmail):
		return fmt.Errorf("%q is %w", email, domain.ErrInvalidEmail)
	case errors.Is(err, authdomain.ErrPasswordTooWeak):
		return fmt.Errorf("%w (%w)", domain.ErrWeakPassword, err)
	default:
		return err
	}
}

// toUser converts u, an admin when admin is set, into a domain.User.
func toUser(u *authdomain.User, admin bool) domain.User {
	return domain.User{
		ID:          u.ID,
		Email:       u.Email,
		FirstName:   u.FirstName,
		LastName:    u.LastName,
		Admin:       admin,
		HasPassword: u.PasswordHash != nil,
		CreatedAt:   u.CreatedAt,
	}
}

// toAccessToken converts t into a domain.AccessToken, its prefix led by its kind's.
func toAccessToken(t *authdomain.AccessToken) domain.AccessToken {
	return domain.AccessToken{
		ID:         t.ID,
		Name:       t.Name,
		Prefix:     t.Kind.Prefix() + t.TokenPrefix,
		CreatedAt:  t.CreatedAt,
		LastUsedAt: t.LastUsedAt,
		ExpiresAt:  t.ExpiresAt,
	}
}
