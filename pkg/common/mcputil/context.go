// Package mcputil holds the MCP helpers the apps share: the context funcs carrying the caller's identity into a tool
// handler's context, from the authenticated request or a forwarded JWT; the tool names every registration and tool
// description refers to; and BdbIDFromToolCallBody, which reads the bdb a tool call targets from its raw body before
// any MCP server parses it.
package mcputil

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

type contextKey string

const (
	userIDKey contextKey = "mcp_user_id"
	jwtKey    contextKey = "mcp_jwt"
)

// SSEContextFunc extracts the authenticated identity from the request and injects it into the MCP handler
// context: the user ID under the shared MCP key, plus the subject, scopes, and roles under the keys the auth
// package reads. All MCP servers (bdbs, databases, gateway) should use this function so they read from the same
// context key.
func SSEContextFunc(ctx context.Context, r *http.Request) context.Context {
	userID, ok := authz.UserIDFromContext(r.Context())
	if ok {
		ctx = context.WithValue(ctx, userIDKey, userID)
	}
	return authz.ContextWithIdentityFrom(ctx, r.Context())
}

// ContextWithIdentity writes an identity established without a token — a user ID and its roles — into an MCP
// handler context: the user ID under the shared MCP key, plus the subject and roles under the keys the auth package
// reads.
func ContextWithIdentity(ctx context.Context, userID string, roles []authz.Role) context.Context {
	ctx = context.WithValue(ctx, userIDKey, userID)
	return authz.ContextWithIdentity(ctx, userID, roles)
}

// GatewaySSEContextFunc extends SSEContextFunc by also storing the raw JWT
// in context, so the gateway can forward it to backend services.
func GatewaySSEContextFunc(ctx context.Context, r *http.Request) context.Context {
	ctx = SSEContextFunc(ctx, r)
	if token := extractBearerToken(r); token != "" {
		ctx = context.WithValue(ctx, jwtKey, token)
	}
	return ctx
}

// UserIDFromContext extracts the user ID injected by SSEContextFunc or HTTPContextFunc.
func UserIDFromContext(ctx context.Context) (string, bool) {
	userID, ok := ctx.Value(userIDKey).(string)
	return userID, ok
}

// JWTFromContext extracts the raw JWT stored by GatewaySSEContextFunc.
func JWTFromContext(ctx context.Context) (string, bool) {
	token, ok := ctx.Value(jwtKey).(string)
	return token, ok
}

// NewInternalHTTPContextFunc returns a context func for internal Streamable HTTP MCP endpoints: it restores
// trace context from incoming headers and identifies the caller from the forwarded JWT, verified against
// validator (signature, issuer, validity window). A missing or invalid token yields a context without a user
// ID, so tool handlers treat the call as unauthenticated instead of trusting a forgeable subject claim.
//
// The identity comes from the verified token alone, never from a header. These endpoints are gated on a shared
// static secret, so a header naming the caller's roles would be forgeable by anything holding that secret.
func NewInternalHTTPContextFunc(
	validator *authhttp.TokenValidator,
) func(ctx context.Context, r *http.Request) context.Context {
	return func(ctx context.Context, r *http.Request) context.Context {
		// Extracts W3C trace context (traceparent header) from the incoming request
		// so that spans created in this service become children of the caller's trace,
		// producing a single distributed trace across services.
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(r.Header))

		token := extractBearerToken(r)
		if token == "" {
			return ctx
		}

		parsed, err := validator.ValidateToken(ctx, token)
		if err != nil {
			return ctx
		}

		if sub := parsed.Subject(); sub != "" {
			ctx = context.WithValue(ctx, userIDKey, sub)
		}
		return authhttp.ContextWithTokenIdentity(ctx, parsed)
	}
}

// extractBearerToken extracts the bearer token from the Authorization header.
func extractBearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && h[:len(prefix)] == prefix {
		return h[len(prefix):]
	}
	return ""
}
