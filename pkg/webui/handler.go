package webui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

// AuthMode is how the frontend signs its users in.
type AuthMode string

// Ways the frontend signs its users in.
const (
	// AuthModeOAuth signs users in through the instance's OAuth flow.
	AuthModeOAuth AuthMode = "oauth"

	// AuthModeToken signs users in with their email and password, on an instance with no OAuth flow.
	AuthModeToken AuthMode = "token"
)

// Config carries what the frontend needs at runtime and how the document's security policy extends.
type Config struct {
	// AuthMode is how the frontend signs its users in, published in the runtime configuration document; empty means
	// AuthModeOAuth.
	AuthMode AuthMode

	// OAuthClientID and OAuthRedirectURI identify the frontend's OAuth2 client; both are published in the runtime
	// configuration document.
	OAuthClientID    string
	OAuthRedirectURI string

	// MCPURL is the absolute URL MCP clients connect to; published in the runtime configuration document so the
	// frontend can show it.
	MCPURL string

	// OwnerEmail is the email of the account that owns the instance, for one with an implicit owner: the frontend
	// tells it apart from the other accounts, whose setup differs. Published in the runtime configuration document
	// when set.
	OwnerEmail string

	// CSPScriptSrcExtra, CSPConnectSrcExtra and CSPImgSrcExtra are extra sources (space-separated) appended to the
	// script-src, connect-src and img-src directives of the document's Content-Security-Policy.
	CSPScriptSrcExtra  string
	CSPConnectSrcExtra string
	CSPImgSrcExtra     string
}

// Handler serves the frontend bundle. Construct it with New.
type Handler struct {
	assets fs.FS
	// index is the app shell, nil when the bundle is not embedded.
	index      []byte
	configJSON []byte
	csp        string
}

// runtimeConfig is the document the frontend fetches at boot. apiUrl stays empty because the frontend is served
// from the same origin as the API.
type runtimeConfig struct {
	APIURL            string `json:"apiUrl"`
	AuthMode          string `json:"authMode"`
	OAuthClientID     string `json:"oauthClientId"`
	OAuthAuthorizeURL string `json:"oauthAuthorizeUrl"`
	OAuthRedirectURI  string `json:"oauthRedirectUri"`
	MCPURL            string `json:"mcpUrl"`
	OwnerEmail        string `json:"ownerEmail,omitempty"`
}

// backendPrefixes are the URL prefixes the API and the OAuth flow own: a request under one of them never receives
// the app shell as a fallback. The service worker's navigateFallbackDenylist in web/pwaConfig.ts lists each of them
// too, so an installed app never answers one of these navigations from its cache.
var backendPrefixes = []string{
	"/api/", "/oauth2/", "/login", "/consent", "/.well-known/", "/authorize", "/token", "/register", "/userinfo",
	"/mcp", "/healthz", "/static/", "/internal/",
}

// extraMIMETypes covers bundle files whose extension Go's built-in table does not know, since the runtime image
// ships no /etc/mime.types either.
var extraMIMETypes = map[string]string{
	".woff2":       "font/woff2",
	".webmanifest": "application/manifest+json",
}

// New builds a Handler over the bundle embedded in the binary.
func New(cfg Config) (*Handler, error) {
	dist, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, fmt.Errorf("opening the embedded bundle: %w", err)
	}
	return newFromFS(dist, cfg)
}

// newFromFS builds a Handler over any filesystem laid out like the Vite output. Split from New so tests can pass
// an in-memory bundle.
func newFromFS(assets fs.FS, cfg Config) (*Handler, error) {
	for ext, typ := range extraMIMETypes {
		if err := mime.AddExtensionType(ext, typ); err != nil {
			return nil, fmt.Errorf("registering the %s media type: %w", ext, err)
		}
	}

	h := &Handler{assets: assets, csp: contentSecurityPolicy(cfg)}

	index, err := fs.ReadFile(assets, "index.html")
	switch {
	case err == nil:
		h.index = index
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("reading the app shell: %w", err)
	}

	authMode := cfg.AuthMode
	if authMode == "" {
		authMode = AuthModeOAuth
	}
	h.configJSON, err = json.Marshal(runtimeConfig{
		AuthMode:          string(authMode),
		OAuthClientID:     cfg.OAuthClientID,
		OAuthAuthorizeURL: "/oauth2/auth",
		OAuthRedirectURI:  cfg.OAuthRedirectURI,
		MCPURL:            cfg.MCPURL,
		OwnerEmail:        cfg.OwnerEmail,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding the runtime configuration: %w", err)
	}

	return h, nil
}

// Embedded reports whether the frontend bundle is compiled into the binary.
func (h *Handler) Embedded() bool {
	return h.index != nil
}

// ServeConfig serves the runtime configuration document the frontend loads before rendering, always fresh so a
// changed client id or redirect URI reaches the browser on the next load.
func (h *Handler) ServeConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "config.json", time.Time{}, bytes.NewReader(h.configJSON))
}

// ServeHTTP is the fallback for requests no API route matched. A bundled file is served as-is; a path that looks
// like a backend route, carries a file extension, or arrives with a method other than GET or HEAD gets a plain
// 404; every other path yields the app shell so the frontend router can resolve it.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}

	p := path.Clean("/" + r.URL.Path)
	if name := strings.TrimPrefix(p, "/"); name != "" {
		if data, err := fs.ReadFile(h.assets, name); err == nil {
			h.serveFile(w, r, name, data)
			return
		}
	}

	if h.index == nil || isBackendPath(p) || path.Ext(p) != "" {
		http.NotFound(w, r)
		return
	}
	h.serveDocument(w, r, "index.html", h.index)
}

// serveFile serves one bundle file with the cache policy its role calls for: hashed assets under assets/ are
// immutable, everything else (the app shell, the service worker, the manifest, icons) must be revalidated so a new
// release is picked up on the next load.
func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, name string, data []byte) {
	if name == "index.html" {
		h.serveDocument(w, r, name, data)
		return
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

// serveDocument serves the app shell with the browser security headers. Only documents carry them: the
// server-rendered login and consent pages keep their own headers, and static files need none.
func (h *Handler) serveDocument(w http.ResponseWriter, r *http.Request, name string, data []byte) {
	header := w.Header()
	header.Set("Content-Security-Policy", h.csp)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	header.Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

// isBackendPath reports whether p falls under a prefix the API or the OAuth flow owns.
func isBackendPath(p string) bool {
	for _, prefix := range backendPrefixes {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// contentSecurityPolicy renders the document policy: same-origin everything, data: images, https and loopback
// connections, no embedding, plus the configured extras on the three extensible directives.
func contentSecurityPolicy(cfg Config) string {
	return strings.Join([]string{
		"default-src 'self'",
		withExtra("script-src 'self'", cfg.CSPScriptSrcExtra),
		"style-src 'self'",
		withExtra("img-src 'self' data:", cfg.CSPImgSrcExtra),
		"font-src 'self'",
		withExtra("connect-src 'self' https: http://localhost:* http://127.0.0.1:*", cfg.CSPConnectSrcExtra),
		"worker-src 'self'",
		"manifest-src 'self'",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
}

// withExtra appends extra sources to a directive when any are configured.
func withExtra(directive, extra string) string {
	if extra = strings.TrimSpace(extra); extra == "" {
		return directive
	}
	return directive + " " + extra
}
