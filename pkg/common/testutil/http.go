package testutil

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
)

// InternalAPISecret is the internal service-to-service secret for tests: construct routers under test with it;
// DoRequest and DoRequestList attach it automatically to requests targeting /internal/ paths.
const InternalAPISecret = "test-internal-api-secret"

// setAuthHeaders sets the bearer token (when non-empty) and, for internal service-to-service paths, the shared
// internal API secret expected by routers built with InternalAPISecret.
func setAuthHeaders(req *http.Request, path, token string) {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if strings.HasPrefix(path, "/internal/") {
		authhttp.SetInternalSecretHeader(req.Header, InternalAPISecret)
	}
}

// DoRequest executes an HTTP request against the given handler and returns
// the status code and the JSON-decoded response body. Intended for integration
// tests that exercise a full HTTP stack.
func DoRequest(t *testing.T, handler http.Handler, method, path, token string, body any) (int, map[string]any) {
	t.Helper()

	var reqBody *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshaling request body: %v", err)
		}
		reqBody = bytes.NewReader(b)
	} else {
		reqBody = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, path, reqBody)
	req.Header.Set("Content-Type", "application/json")
	setAuthHeaders(req, path, token)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Logf("decoding response body: %v", err)
	}
	return rr.Code, resp
}

// DoRequestList executes an HTTP request and decodes the response as a JSON array.
func DoRequestList(t *testing.T, handler http.Handler, method, path, token string, body any) (int, []map[string]any) {
	t.Helper()

	var reqBody *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshaling request body: %v", err)
		}
		reqBody = bytes.NewReader(b)
	} else {
		reqBody = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, path, reqBody)
	req.Header.Set("Content-Type", "application/json")
	setAuthHeaders(req, path, token)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	var resp []map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Logf("decoding response body: %v", err)
	}
	return rr.Code, resp
}
