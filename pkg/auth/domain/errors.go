package domain

import "errors"

var (
	// ErrUserNotFound indicates that no user was found for the given criteria.
	ErrUserNotFound = errors.New("user not found")

	// ErrInstanceOwnerNotSet indicates an instance whose owner account has not been recorded.
	ErrInstanceOwnerNotSet = errors.New("instance owner not set")

	// ErrAccessTokenNotFound indicates that no valid access token of the user matches the given ID.
	ErrAccessTokenNotFound = errors.New("access token not found")

	// ErrInvalidAccessToken indicates a bearer token that authenticates no one: unknown, revoked, expired, of a kind
	// that is not a bearer token, or of an inactive user. One error covers them all, so a caller learns nothing about
	// which tokens exist.
	ErrInvalidAccessToken = errors.New("invalid access token")

	// ErrInvalidAccessTokenName indicates an access token name that is empty or too long.
	ErrInvalidAccessTokenName = errors.New("invalid access token name")

	// ErrInvalidAccessTokenExpiry indicates an access token expiry that is not in the future.
	ErrInvalidAccessTokenExpiry = errors.New("invalid access token expiry: it must be in the future")

	// ErrInvalidCredentials indicates that the provided credentials are incorrect.
	// Covers both "user not found" and "wrong password" to prevent user enumeration.
	ErrInvalidCredentials = errors.New("invalid credentials")

	// ErrEmailAlreadyExists indicates that a user with this email already exists.
	ErrEmailAlreadyExists = errors.New("email already exists")

	// ErrPasswordTooWeak indicates that the password does not meet strength requirements.
	ErrPasswordTooWeak = errors.New("password does not meet strength requirements")

	// ErrInvalidEmail indicates that the email address has an invalid format.
	ErrInvalidEmail = errors.New("invalid email format")

	// ErrInvalidRole indicates that the provided role is not recognized.
	ErrInvalidRole = errors.New("invalid role")

	// ErrExternalIdentityNotFound indicates that no (provider, provider_subject) link exists.
	ErrExternalIdentityNotFound = errors.New("external identity not found")

	// ErrOIDCStateNotFound indicates that no in-flight sign-in flow matches the given state value — it was never
	// issued, was already consumed by an earlier callback, or expired.
	ErrOIDCStateNotFound = errors.New("oidc flow state not found")

	// ErrOIDCProviderMismatch indicates that the in-flight sign-in flow was issued for a different provider than the
	// one handling the callback.
	ErrOIDCProviderMismatch = errors.New("oidc flow state was issued for a different provider")

	// ErrEmailUnverified indicates that the identity provider did not assert that the user owns the email address.
	// Returning this error from sign-in prevents an account-takeover vector where an attacker registers a victim's
	// email at the IdP without verification and email-matches into a pre-existing local account.
	ErrEmailUnverified = errors.New("identity provider did not verify the user's email")

	// ErrOAuth2ClientNotFound indicates that the authorization server has no client registered under the given id.
	ErrOAuth2ClientNotFound = errors.New("oauth2 client not found")

	// ErrOAuth2ClientExists indicates that the authorization server already has a client registered under the id
	// of the one being created.
	ErrOAuth2ClientExists = errors.New("oauth2 client already exists")
)
