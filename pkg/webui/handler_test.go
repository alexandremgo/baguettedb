package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// bundle returns an in-memory filesystem laid out like the Vite output, with the files whose cache and header
// rules differ from one another.
func bundle() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>shell</title>")},
		"assets/app-abc123.js": {Data: []byte("console.log('app')")},
		"assets/font.woff2":    {Data: []byte("woff2")},
		"sw.js":                {Data: []byte("self.addEventListener('fetch', () => {})")},
		"manifest.webmanifest": {Data: []byte(`{"name":"BaguetteDB"}`)},
		"favicon-192x192.png":  {Data: []byte("png")},
	}
}

// newTestHandler builds a Handler over the in-memory bundle with a client id, redirect URI and MCP URL set.
func newTestHandler(t *testing.T, fsys fstest.MapFS) *Handler {
	t.Helper()
	h, err := newFromFS(fsys, Config{
		OAuthClientID:      "baguettedb-web",
		OAuthRedirectURI:   "http://localhost:8080/callback",
		MCPURL:             "http://localhost:8080/mcp",
		CSPConnectSrcExtra: "https://telemetry.example.com",
	})
	if err != nil {
		t.Fatalf("newFromFS: %v", err)
	}
	return h
}

// get performs a request against the fallback handler and returns the recorded response.
func get(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

// TestServeHTTP_AppShellForClientRoutes checks that the root and any extension-less path outside the backend
// prefixes receive the app shell with the security headers and a revalidate cache policy.
func TestServeHTTP_AppShellForClientRoutes(t *testing.T) {
	h := newTestHandler(t, bundle())

	for _, target := range []string{"/", "/bdbs/123", "/app/bdbs/abc/tables/notes", "/settings"} {
		rec := get(h, http.MethodGet, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", target, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "shell") {
			t.Fatalf("%s: body = %q, want the app shell", target, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("%s: Content-Type = %q, want text/html", target, ct)
		}
		if rec.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s: Cache-Control = %q, want no-cache", target, rec.Header().Get("Cache-Control"))
		}
		csp := rec.Header().Get("Content-Security-Policy")
		wantConnect := "connect-src 'self' https: http://localhost:* http://127.0.0.1:* https://telemetry.example.com"
		if !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, wantConnect) {
			t.Fatalf("%s: Content-Security-Policy = %q", target, csp)
		}
		if rec.Header().Get("X-Frame-Options") != "DENY" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: missing frame or sniffing headers: %v", target, rec.Header())
		}
	}
}

// TestServeHTTP_FilesCarryTheirCachePolicy checks that hashed assets are immutable, that every other bundle file
// revalidates, that no static file carries the document headers, and that the extra media types resolve.
func TestServeHTTP_FilesCarryTheirCachePolicy(t *testing.T) {
	h := newTestHandler(t, bundle())

	tests := []struct {
		target      string
		wantCache   string
		wantTypeHas string
	}{
		{"/assets/app-abc123.js", "public, max-age=31536000, immutable", "javascript"},
		{"/assets/font.woff2", "public, max-age=31536000, immutable", "font/woff2"},
		{"/sw.js", "no-cache", "javascript"},
		{"/manifest.webmanifest", "no-cache", "application/manifest+json"},
		{"/favicon-192x192.png", "no-cache", "image/png"},
	}
	for _, tt := range tests {
		rec := get(h, http.MethodGet, tt.target)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", tt.target, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != tt.wantCache {
			t.Fatalf("%s: Cache-Control = %q, want %q", tt.target, got, tt.wantCache)
		}
		if got := rec.Header().Get("Content-Type"); !strings.Contains(got, tt.wantTypeHas) {
			t.Fatalf("%s: Content-Type = %q, want it to contain %q", tt.target, got, tt.wantTypeHas)
		}
		if rec.Header().Get("Content-Security-Policy") != "" {
			t.Fatalf("%s: static files must not carry the document policy", tt.target)
		}
	}
}

// TestServeHTTP_NoShellForBackendPathsMissingFilesOrWrites checks that unknown API routes, missing files, and
// non-GET methods get a plain 404 rather than the app shell, which would mask a broken route as a blank page.
func TestServeHTTP_NoShellForBackendPathsMissingFilesOrWrites(t *testing.T) {
	h := newTestHandler(t, bundle())

	for _, target := range []string{
		"/api/nope", "/login/oidc/x", "/oauth2/whatever", "/mcp/extra", "/healthz/x", "/assets/missing.js",
		"/logo.svg", "/.well-known/other",
	} {
		rec := get(h, http.MethodGet, target)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", target, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "shell") {
			t.Fatalf("%s: must not serve the app shell", target)
		}
	}
	if rec := get(h, http.MethodPost, "/bdbs"); rec.Code != http.StatusNotFound {
		t.Fatalf("POST /bdbs: status = %d, want 404", rec.Code)
	}
}

// TestServeHTTP_TraversalStaysInsideTheBundle checks that a path climbing out of the bundle root resolves inside
// it and that a direct request for the shell file is treated as a document.
func TestServeHTTP_TraversalStaysInsideTheBundle(t *testing.T) {
	h := newTestHandler(t, bundle())

	rec := get(h, http.MethodGet, "/../../index.html")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("/../../index.html: status = %d, headers = %v", rec.Code, rec.Header())
	}
}

// TestServeConfig_PublishesTheRuntimeDocument checks the shape of /config.json: same-origin API, the OAuth auth mode
// by default, the configured client id, redirect URI and MCP URL, the relative authorize URL, and a revalidate cache
// policy.
func TestServeConfig_PublishesTheRuntimeDocument(t *testing.T) {
	h := newTestHandler(t, bundle())

	rec := httptest.NewRecorder()
	h.ServeConfig(rec, httptest.NewRequest(http.MethodGet, "/config.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", rec.Header().Get("Cache-Control"))
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	want := map[string]string{
		"apiUrl":            "",
		"authMode":          "oauth",
		"oauthClientId":     "baguettedb-web",
		"oauthAuthorizeUrl": "/oauth2/auth",
		"oauthRedirectUri":  "http://localhost:8080/callback",
		"mcpUrl":            "http://localhost:8080/mcp",
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %q, want %q (full document: %v)", k, got[k], v, got)
		}
	}
}

// TestServeConfig_TokenMode checks that /config.json publishes the token auth mode when configured with it.
func TestServeConfig_TokenMode(t *testing.T) {
	h, err := newFromFS(bundle(), Config{AuthMode: AuthModeToken, MCPURL: "http://127.0.0.1:3035/mcp"})
	if err != nil {
		t.Fatalf("newFromFS: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeConfig(rec, httptest.NewRequest(http.MethodGet, "/config.json", nil))
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if got["authMode"] != "token" {
		t.Errorf("authMode = %q, want token (full document: %v)", got["authMode"], got)
	}
}

// TestNotEmbedded_ServesConfigOnly checks the placeholder build: without an app shell the handler reports the
// frontend as not embedded, still serves the configuration document, and answers every fallback with 404.
func TestNotEmbedded_ServesConfigOnly(t *testing.T) {
	h := newTestHandler(t, fstest.MapFS{".gitkeep": {Data: nil}})

	if h.Embedded() {
		t.Fatal("Embedded() = true without an app shell")
	}
	rec := httptest.NewRecorder()
	h.ServeConfig(rec, httptest.NewRequest(http.MethodGet, "/config.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/config.json without a bundle: status = %d, want 200", rec.Code)
	}
	if rec := get(h, http.MethodGet, "/"); rec.Code != http.StatusNotFound {
		t.Fatalf("/ without a bundle: status = %d, want 404", rec.Code)
	}
}

// TestNew_PlaceholderBuildIsNotEmbedded checks the real embedded directory as committed: with only the placeholder
// present, New succeeds and reports no bundle, so an API-only binary still boots.
func TestNew_PlaceholderBuildIsNotEmbedded(t *testing.T) {
	h, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if h.Embedded() {
		t.Skip("a bundle is synced into pkg/webui/dist on this machine; the placeholder case is covered above")
	}
}
