package authhttp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

func TestRequireToken(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating RSA key: %v", err)
	}

	jwkKey, err := jwk.FromRaw(privateKey)
	if err != nil {
		t.Fatalf("creating JWK: %v", err)
	}
	if err := jwkKey.Set(jwk.KeyIDKey, "test-key-id"); err != nil {
		t.Fatalf("setting kid: %v", err)
	}
	if err := jwkKey.Set(jwk.AlgorithmKey, jwa.RS256); err != nil {
		t.Fatalf("setting alg: %v", err)
	}

	pubKey, err := jwkKey.PublicKey()
	if err != nil {
		t.Fatalf("getting public key: %v", err)
	}

	pubSet := jwk.NewSet()
	if err := pubSet.AddKey(pubKey); err != nil {
		t.Fatalf("adding key to set: %v", err)
	}

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(pubSet); err != nil {
			http.Error(w, "failed to encode JWKS", http.StatusInternalServerError)
		}
	}))
	defer jwksServer.Close()

	issuer := "http://test-issuer"
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout})

	tv, err := NewTokenValidator(context.Background(), jwksServer.URL, issuer, "", logger)
	if err != nil {
		t.Fatalf("creating token validator: %v", err)
	}
	defer tv.Close()

	buildToken := func(sub, iss string, exp time.Time, scopes []string, roles []string) string {
		tok, err := jwt.NewBuilder().
			Subject(sub).
			Issuer(iss).
			Expiration(exp).
			IssuedAt(time.Now()).
			Build()
		if err != nil {
			t.Fatalf("building token: %v", err)
		}
		if scopes != nil {
			if err := tok.Set("scp", scopes); err != nil {
				t.Fatalf("setting scopes: %v", err)
			}
		}
		if roles != nil {
			if err := tok.Set("ext", map[string]any{"roles": roles}); err != nil {
				t.Fatalf("setting ext.roles: %v", err)
			}
		}

		signed, err := jwt.Sign(tok, jwt.WithKey(jwa.RS256, jwkKey))
		if err != nil {
			t.Fatalf("signing token: %v", err)
		}
		return string(signed)
	}

	protectedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := authz.UserIDFromContext(r.Context())
		if !ok {
			t.Error("expected user ID in context")
		}
		scopes := authz.ScopesFromContext(r.Context())
		roles := authz.RolesFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
		body := map[string]any{"user_id": userID, "scopes": scopes, "roles": roles}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("encoding response: %v", err)
		}
	})

	middleware := tv.RequireToken()
	handler := middleware(protectedHandler)

	inAnHour := time.Now().Add(time.Hour)
	userAndAdmin := []string{"user", "admin"}

	tests := []struct {
		name       string
		authHeader string
		cookie     *http.Cookie
		wantStatus int
	}{
		{
			name: "valid token via header",
			authHeader: "Bearer " +
				buildToken("user-123", issuer, inAnHour, []string{"openid", "offline_access"}, userAndAdmin),
			wantStatus: http.StatusOK,
		},
		{
			name: "valid token via cookie",
			cookie: &http.Cookie{
				Name:  "bdb_access_token",
				Value: buildToken("user-123", issuer, inAnHour, []string{"openid"}, userAndAdmin),
			},
			wantStatus: http.StatusOK,
		},
		{
			name:       "header takes precedence over cookie",
			authHeader: "Bearer " + buildToken("user-123", issuer, inAnHour, []string{"openid"}, userAndAdmin),
			cookie:     &http.Cookie{Name: "bdb_access_token", Value: "garbage-cookie-token"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "missing header and cookie",
			authHeader: "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "invalid bearer prefix",
			authHeader: "Basic abc123",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "expired token",
			authHeader: "Bearer " + buildToken("user-123", issuer, time.Now().Add(-time.Hour), nil, nil),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "wrong issuer",
			authHeader: "Bearer " + buildToken("user-123", "http://wrong-issuer", time.Now().Add(time.Hour), nil, nil),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "garbage token",
			authHeader: "Bearer not.a.valid.jwt",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "expired token via cookie",
			cookie: &http.Cookie{
				Name:  "bdb_access_token",
				Value: buildToken("user-123", issuer, time.Now().Add(-time.Hour), nil, nil),
			},
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			if tt.cookie != nil {
				req.AddCookie(tt.cookie)
			}
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d, body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}

			if tt.wantStatus == http.StatusOK {
				var body map[string]any
				if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
					t.Fatalf("decoding response: %v", err)
				}
				if body["user_id"] != "user-123" {
					t.Errorf("user_id = %v, want user-123", body["user_id"])
				}
				// Checks roles are extracted from ext claim.
				if roles, ok := body["roles"].([]any); ok {
					if len(roles) != 2 {
						t.Errorf("roles len = %d, want 2", len(roles))
					}
				}
			}
		})
	}
}

func TestRequirePermission(t *testing.T) {
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		name       string
		roles      []authz.Role
		perm       authz.Permission
		wantStatus int
	}{
		{
			name:       "admin has manage:users",
			roles:      []authz.Role{authz.RoleAdmin},
			perm:       authz.PermManageUsers,
			wantStatus: http.StatusOK,
		},
		{
			name:       "user lacks manage:users",
			roles:      []authz.Role{authz.RoleUser},
			perm:       authz.PermManageUsers,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "no roles",
			roles:      nil,
			perm:       authz.PermReadOwnProfile,
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			middleware := RequirePermission(authz.Policy{}, tt.perm)
			handler := middleware(okHandler)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			ctx := req.Context()
			if tt.roles != nil {
				ctx = authz.ContextWithIdentity(ctx, "user-123", tt.roles)
			}
			req = req.WithContext(ctx)

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rr.Code, tt.wantStatus)
			}
		})
	}
}
