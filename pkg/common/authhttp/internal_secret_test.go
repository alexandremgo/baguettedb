package authhttp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireInternalSecret_Rejects(t *testing.T) {
	tests := []struct {
		name      string
		secret    string
		setHeader func(h http.Header)
	}{
		{name: "missing header", secret: "s3cret", setHeader: func(http.Header) {}},
		{name: "wrong secret", secret: "s3cret", setHeader: func(h http.Header) { SetInternalSecretHeader(h, "nope") }},
		{name: "empty configured secret fails closed", secret: "", setHeader: func(http.Header) {}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nextCalled := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nextCalled = true })

			req := httptest.NewRequest(http.MethodPost, "/internal/api/test/v1/things", nil)
			tt.setHeader(req.Header)
			rec := httptest.NewRecorder()

			RequireInternalSecret(tt.secret)(next).ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
			}
			if nextCalled {
				t.Fatal("next handler must not run on a rejected request")
			}
		})
	}
}

func TestRequireInternalSecret_MatchingSecretPassesThrough(t *testing.T) {
	const secret = "s3cret"
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	req := httptest.NewRequest(http.MethodPost, "/internal/api/test/v1/things", nil)
	SetInternalSecretHeader(req.Header, secret)
	rec := httptest.NewRecorder()

	RequireInternalSecret(secret)(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
}
