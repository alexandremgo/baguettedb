// Package signedcookie provides a small HMAC-SHA256 primitive for stamping short-lived, tamper-evident state into a
// cookie value (or any opaque transport). The payload is JSON-encoded with an embedded expiry; the caller is
// responsible for setting cookie attributes (httpOnly, Secure, SameSite, Path).
//
// Confidentiality is NOT provided — the payload is base64-encoded, not encrypted. Use only for values the holder is
// already entitled to know (e.g. their own login challenge or their own provider profile after authenticating).
package signedcookie

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidFormat is returned when the cookie value doesn't match the expected `<body>.<signature>` shape.
var ErrInvalidFormat = errors.New("signedcookie: invalid format")

// ErrInvalidSignature is returned when the HMAC doesn't match. Tampered body or wrong secret.
var ErrInvalidSignature = errors.New("signedcookie: invalid signature")

// ErrExpired is returned when the payload's `exp` Unix timestamp is in the past.
var ErrExpired = errors.New("signedcookie: expired")

// Signer mints and verifies HMAC-SHA256-signed values carrying an arbitrary JSON payload and an absolute expiry.
type Signer struct {
	secret []byte
}

// NewSigner returns a Signer backed by the given HMAC secret. The secret should be at least 32 bytes.
func NewSigner(secret []byte) *Signer {
	return &Signer{secret: secret}
}

// envelope is the on-the-wire structure: an absolute expiry plus the caller's opaque JSON payload.
type envelope struct {
	Exp     int64           `json:"exp"`
	Payload json.RawMessage `json:"p"`
}

// Sign serializes payload to JSON, wraps it with an absolute expiry (now + ttl), and returns the base64url
// `<body>.<signature>` string suitable for use as a cookie value.
func (s *Signer) Sign(payload any, ttl time.Duration) (string, error) {
	inner, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("signedcookie: marshalling payload: %w", err)
	}
	body, err := json.Marshal(envelope{
		Exp:     time.Now().Add(ttl).Unix(),
		Payload: inner,
	})
	if err != nil {
		return "", fmt.Errorf("signedcookie: marshalling envelope: %w", err)
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write(body)
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Verify decodes value, checks the signature in constant time, checks the embedded expiry against the current time,
// and unmarshals the inner payload into target. target must be a non-nil pointer to a JSON-compatible destination.
func (s *Signer) Verify(value string, target any) error {
	body, sig, err := splitAndDecode(value)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write(body)
	expected := mac.Sum(nil)
	if !hmac.Equal(sig, expected) {
		return ErrInvalidSignature
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("signedcookie: unmarshalling envelope: %w", err)
	}
	if time.Now().Unix() > env.Exp {
		return ErrExpired
	}
	if err := json.Unmarshal(env.Payload, target); err != nil {
		return fmt.Errorf("signedcookie: unmarshalling payload: %w", err)
	}
	return nil
}

// splitAndDecode parses `<body>.<sig>` into its two raw byte components.
func splitAndDecode(value string) (body, sig []byte, err error) {
	idx := strings.LastIndexByte(value, '.')
	if idx < 0 {
		return nil, nil, ErrInvalidFormat
	}
	body, err = base64.RawURLEncoding.DecodeString(value[:idx])
	if err != nil {
		return nil, nil, ErrInvalidFormat
	}
	sig, err = base64.RawURLEncoding.DecodeString(value[idx+1:])
	if err != nil {
		return nil, nil, ErrInvalidFormat
	}
	return body, sig, nil
}
