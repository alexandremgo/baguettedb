package testutil

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

const testIssuer = "http://test-issuer"

// TestJWKS replaces Ory Hydra for integration tests. In production, Hydra issues
// JWTs signed with its RSA key and serves the public key via a JWKS endpoint
// (/.well-known/jwks.json). The app's TokenValidator fetches that JWKS to verify
// token signatures.
//
// TestJWKS reproduces this setup locally: it generates its own RSA key pair,
// starts an httptest server that serves the public key as a JWKS, and signs
// tokens with the private key. The real TokenValidator can then be pointed at
// this local JWKS server — it validates tokens identically to production,
// without needing a running Hydra instance.
//
// Tokens produced by BuildToken include the same claims Hydra would set:
// sub (user ID), iss (issuer), and ext.roles (user roles in the session).
type TestJWKS struct {
	privateKey jwk.Key
	publicSet  jwk.Set
	server     *httptest.Server
}

// NewTestJWKS generates an RSA-2048 key pair, builds a JWKS, and starts
// an httptest server that serves the public key set.
func NewTestJWKS() (*TestJWKS, error) {
	raw, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generating RSA key: %w", err)
	}

	privKey, err := jwk.FromRaw(raw)
	if err != nil {
		return nil, fmt.Errorf("creating JWK from RSA key: %w", err)
	}
	if err := privKey.Set(jwk.KeyIDKey, "test-key-id"); err != nil {
		return nil, fmt.Errorf("setting kid: %w", err)
	}
	if err := privKey.Set(jwk.AlgorithmKey, jwa.RS256); err != nil {
		return nil, fmt.Errorf("setting alg: %w", err)
	}

	pubKey, err := privKey.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("deriving public key: %w", err)
	}

	pubSet := jwk.NewSet()
	if err := pubSet.AddKey(pubKey); err != nil {
		return nil, fmt.Errorf("adding public key to set: %w", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(pubSet); err != nil {
			http.Error(w, "failed to encode JWKS", http.StatusInternalServerError)
		}
	}))

	return &TestJWKS{
		privateKey: privKey,
		publicSet:  pubSet,
		server:     server,
	}, nil
}

// BuildToken creates and signs a JWT with the given subject (user ID) and roles.
func (tj *TestJWKS) BuildToken(userID string, roles []string) (string, error) {
	now := time.Now()
	tok, err := jwt.NewBuilder().
		Subject(userID).
		Issuer(testIssuer).
		Expiration(now.Add(time.Hour)).
		IssuedAt(now).
		Build()
	if err != nil {
		return "", fmt.Errorf("building token: %w", err)
	}

	if roles != nil {
		if err := tok.Set("ext", map[string]any{"roles": roles}); err != nil {
			return "", fmt.Errorf("setting ext.roles: %w", err)
		}
	}

	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.RS256, tj.privateKey))
	if err != nil {
		return "", fmt.Errorf("signing token: %w", err)
	}

	return string(signed), nil
}

// JWKSURL returns the URL of the test JWKS server.
func (tj *TestJWKS) JWKSURL() string {
	return tj.server.URL
}

// Issuer returns the test issuer string used in generated tokens.
func (tj *TestJWKS) Issuer() string {
	return testIssuer
}

// Close stops the JWKS httptest server.
func (tj *TestJWKS) Close() {
	tj.server.Close()
}
