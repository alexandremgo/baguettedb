package httputil

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestRequireHost checks which Host headers RequireHost lets through: localhost and its subdomains, IPv4 and IPv6
// addresses, with or without a port, and the allowed names in any case; any other name, and an empty Host, answer 421.
func TestRequireHost(t *testing.T) {
	handler := RequireHost("nas.local")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for host, want := range map[string]int{
		"localhost":          http.StatusOK,
		"localhost:8080":     http.StatusOK,
		"LOCALHOST:8080":     http.StatusOK,
		"bdb.localhost:8080": http.StatusOK,
		"127.0.0.1:8080":     http.StatusOK,
		"192.168.1.10:8080":  http.StatusOK,
		"[::1]:8080":         http.StatusOK,
		"[fe80::1]":          http.StatusOK,
		"nas.local:8080":     http.StatusOK,
		"NAS.Local":          http.StatusOK,
		"nas.local.":         http.StatusOK,
		"evil.example:8080":  http.StatusMisdirectedRequest,
		"localhost.evil.com": http.StatusMisdirectedRequest,
		"nas.local.evil.com": http.StatusMisdirectedRequest,
		"":                   http.StatusMisdirectedRequest,
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %q = %d, want %d", host, rec.Code, want)
		}
	}
}

// TestRedirectLoopbackAliases checks that a page loaded through another loopback name than the base's is redirected to
// the same path and query under the base, while the base's own name, API and MCP requests, other methods and
// non-loopback names pass; and that a base that isn't a loopback name redirects nothing.
func TestRedirectLoopbackAliases(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	loopback := RedirectLoopbackAliases(&url.URL{Scheme: "http", Host: "127.0.0.1:3035"})(next)
	named := RedirectLoopbackAliases(&url.URL{Scheme: "http", Host: "nas.local:3035"})(next)

	for _, tc := range []struct {
		handler      http.Handler
		method, host string
		target       string
		wantCode     int
		wantLocation string
	}{
		{loopback, http.MethodGet, "localhost:3035", "/bdbs?tab=1", http.StatusTemporaryRedirect,
			"http://127.0.0.1:3035/bdbs?tab=1"},
		{loopback, http.MethodGet, "[::1]:3035", "/sign-in", http.StatusTemporaryRedirect,
			"http://127.0.0.1:3035/sign-in"},
		{loopback, http.MethodHead, "bdb.localhost:3035", "/", http.StatusTemporaryRedirect, "http://127.0.0.1:3035/"},
		{loopback, http.MethodGet, "127.0.0.1:3035", "/bdbs", http.StatusOK, ""},
		{loopback, http.MethodGet, "localhost:3035", "/api/bdbs/v1/bdbs", http.StatusOK, ""},
		{loopback, http.MethodGet, "localhost:3035", "/mcp", http.StatusOK, ""},
		{loopback, http.MethodPost, "localhost:3035", "/bdbs", http.StatusOK, ""},
		{loopback, http.MethodGet, "192.168.1.10:3035", "/bdbs", http.StatusOK, ""},
		{named, http.MethodGet, "localhost:3035", "/bdbs", http.StatusOK, ""},
	} {
		req := httptest.NewRequest(tc.method, tc.target, nil)
		req.Host = tc.host
		rec := httptest.NewRecorder()
		tc.handler.ServeHTTP(rec, req)
		if rec.Code != tc.wantCode || rec.Header().Get("Location") != tc.wantLocation {
			t.Errorf("%s %s%s = %d to %q, want %d to %q", tc.method, tc.host, tc.target, rec.Code,
				rec.Header().Get("Location"), tc.wantCode, tc.wantLocation)
		}
	}
}
