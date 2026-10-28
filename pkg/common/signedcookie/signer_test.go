package signedcookie

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type samplePayload struct {
	Email   string `json:"email"`
	Subject string `json:"sub"`
}

func TestSigner_RoundTrip(t *testing.T) {
	s := NewSigner([]byte("test-secret-with-enough-entropy"))
	in := samplePayload{Email: "alice@example.com", Subject: "abc-123"}

	cookie, err := s.Sign(in, 5*time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	var out samplePayload
	if err := s.Verify(cookie, &out); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if out != in {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", out, in)
	}
}

func TestSigner_Expired(t *testing.T) {
	s := NewSigner([]byte("test-secret-with-enough-entropy"))
	// A negative TTL produces a payload whose `exp` is already in the past.
	cookie, err := s.Sign(samplePayload{Email: "a@b"}, -1*time.Second)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	var out samplePayload
	err = s.Verify(cookie, &out)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("expected ErrExpired, got %v", err)
	}
}

func TestSigner_TamperedBody(t *testing.T) {
	s := NewSigner([]byte("test-secret-with-enough-entropy"))
	cookie, err := s.Sign(samplePayload{Email: "alice@example.com"}, 5*time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// Flip a byte in the body half.
	idx := strings.LastIndexByte(cookie, '.')
	tampered := cookie[:idx-1] + flipByte(cookie[idx-1:idx]) + cookie[idx:]

	var out samplePayload
	err = s.Verify(tampered, &out)
	if !errors.Is(err, ErrInvalidSignature) && !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("expected ErrInvalidSignature or ErrInvalidFormat, got %v", err)
	}
}

func TestSigner_TamperedSignature(t *testing.T) {
	s := NewSigner([]byte("test-secret-with-enough-entropy"))
	cookie, err := s.Sign(samplePayload{Email: "alice@example.com"}, 5*time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// Flip a byte in the signature half.
	idx := strings.LastIndexByte(cookie, '.')
	tampered := cookie[:idx+1] + flipByte(cookie[idx+1:idx+2]) + cookie[idx+2:]

	var out samplePayload
	err = s.Verify(tampered, &out)
	if !errors.Is(err, ErrInvalidSignature) && !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("expected ErrInvalidSignature or ErrInvalidFormat, got %v", err)
	}
}

func TestSigner_WrongSecret(t *testing.T) {
	signer := NewSigner([]byte("original-secret-with-enough-entropy"))
	verifier := NewSigner([]byte("different-secret-with-enough-entr"))

	cookie, err := signer.Sign(samplePayload{Email: "alice@example.com"}, 5*time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	var out samplePayload
	err = verifier.Verify(cookie, &out)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature, got %v", err)
	}
}

func TestSigner_InvalidFormat(t *testing.T) {
	s := NewSigner([]byte("test-secret-with-enough-entropy"))
	cases := []string{
		"",
		"no-dot-in-here",
		"!!!.!!!",
		"valid-base64-but-no-sig.",
		".valid-base64-but-no-body",
	}
	for _, c := range cases {
		var out samplePayload
		err := s.Verify(c, &out)
		if err == nil {
			t.Fatalf("expected error for %q, got nil", c)
		}
	}
}

// flipByte returns a single-byte string that differs from b's first byte.
func flipByte(b string) string {
	if len(b) == 0 {
		return "A"
	}
	if b[0] == 'A' {
		return "B"
	}
	return "A"
}
