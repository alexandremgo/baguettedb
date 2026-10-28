package httputil

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestMaxBodySize(t *testing.T) {
	tests := []struct {
		name       string
		maxBytes   int64
		bodySize   int
		wantStatus int
	}{
		{"under limit", 100, 50, http.StatusOK},
		{"at limit", 100, 100, http.StatusOK},
		{"over limit", 100, 200, http.StatusRequestEntityTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := MaxBodySize(tt.maxBytes)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, err := io.ReadAll(r.Body)
				if err != nil {
					w.WriteHeader(http.StatusRequestEntityTooLarge)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))

			body := strings.Repeat("x", tt.bodySize)
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rr.Code, tt.wantStatus)
			}
		})
	}
}

func TestRequestLogger_SkipsSuccessfulHealthChecks(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		status  int
		wantLog bool
	}{
		{"healthz success not logged", "/api/auth/healthz", http.StatusOK, false},
		{"bare healthz success not logged", "/healthz", http.StatusOK, false},
		{"healthz failure logged", "/api/auth/healthz", http.StatusServiceUnavailable, true},
		{"normal request logged", "/api/auth/v1/me", http.StatusOK, true},
		{"healthz-named resource logged", "/api/bdbs/v1/bdbs/healthz-tracker", http.StatusOK, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := zerolog.New(&buf)

			handler := RequestLogger(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			handler.ServeHTTP(httptest.NewRecorder(), req)

			if got := buf.Len() > 0; got != tt.wantLog {
				t.Errorf("logged = %v, want %v (output: %q)", got, tt.wantLog, buf.String())
			}
		})
	}
}

func TestIsMaxBytesError(t *testing.T) {
	handler := MaxBodySize(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			if IsMaxBytesError(err) {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 100)))
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusRequestEntityTooLarge)
	}
}
