package authz

import "context"

type contextKey string

const (
	userIDKey contextKey = "user_id"
	scopesKey contextKey = "scopes"
	rolesKey  contextKey = "roles"
)

// UserIDFromContext extracts the user ID (subject) from the request context.
func UserIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(userIDKey).(string)
	return id, ok
}

// ScopesFromContext extracts the granted scopes from the request context.
func ScopesFromContext(ctx context.Context) []string {
	if scopes, ok := ctx.Value(scopesKey).([]string); ok {
		return scopes
	}
	return nil
}

// RolesFromContext extracts the user roles from the request context.
func RolesFromContext(ctx context.Context) []Role {
	if roles, ok := ctx.Value(rolesKey).([]Role); ok {
		return roles
	}
	return nil
}

// ContextWithIdentity writes an identity established without a token — a subject and its roles — into ctx, under
// the keys UserIDFromContext and RolesFromContext read. No scopes are granted.
func ContextWithIdentity(ctx context.Context, userID string, roles []Role) context.Context {
	ctx = context.WithValue(ctx, userIDKey, userID)
	return context.WithValue(ctx, rolesKey, roles)
}

// ContextWithScopedIdentity writes an identity carried by an access token — subject, granted scopes, and roles —
// into ctx, under the keys UserIDFromContext, ScopesFromContext and RolesFromContext read.
//
// An empty userID is not written, so UserIDFromContext then reports no subject rather than an empty one.
func ContextWithScopedIdentity(ctx context.Context, userID string, scopes []string, roles []Role) context.Context {
	if userID != "" {
		ctx = context.WithValue(ctx, userIDKey, userID)
	}
	ctx = context.WithValue(ctx, scopesKey, scopes)
	return context.WithValue(ctx, rolesKey, roles)
}

// ContextWithIdentityFrom copies the authenticated identity — subject, granted scopes, and roles — from src into
// ctx. Each value is copied only when src carries it, so an unauthenticated src returns ctx untouched.
//
// It carries an identity across a context boundary, where the destination context is not derived from the
// request that was authenticated.
func ContextWithIdentityFrom(ctx, src context.Context) context.Context {
	ctx = copyValue[string](ctx, src, userIDKey)
	ctx = copyValue[[]string](ctx, src, scopesKey)
	return copyValue[[]Role](ctx, src, rolesKey)
}

// copyValue returns dst with the value src stores under key copied into it, preserving its type.
// dst is returned unchanged when src holds no value of type T under key.
func copyValue[T any](dst, src context.Context, key contextKey) context.Context {
	val, ok := src.Value(key).(T)
	if !ok {
		return dst
	}
	return context.WithValue(dst, key, val)
}
