package httputil

import (
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// RequireHost returns middleware that answers 421 Misdirected Request to a request whose Host names the server by
// an unexpected name, the mark of a DNS rebinding attack: a page on another site that re-points its own name at the
// server, so the browser treats the server as that page's origin, still sends that name as the Host.
//
// Accepted are localhost and its subdomains, any IP address, and the names in allowed (any case). An IP can't be
// rebound, since the browser sends what the URL holds; cross-origin requests to an IP are left to authentication and
// the browser's same-origin rules.
func RequireHost(allowed ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isAllowedHost(r.Host, allowed) {
				http.Error(w, "unexpected Host "+r.Host, http.StatusMisdirectedRequest)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RedirectLoopbackAliases returns middleware that sends a browser loading a page through another loopback name than
// base's, such as localhost for a base at 127.0.0.1, to the same page under base, since a browser keeps cookies per
// name: a session started under one name is unknown under the other. It does nothing when base's host isn't a
// loopback name.
//
// Only page loads move: GET and HEAD requests outside /api/ and /mcp, whose clients authenticate with tokens rather
// than cookies. The redirect keeps the path and query, and the browser keeps the fragment.
func RedirectLoopbackAliases(base *url.URL) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !isLoopbackName(hostName(base.Host)) {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := hostName(r.Host)
			isPageLoad := (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
				!strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/mcp" &&
				!strings.HasPrefix(r.URL.Path, "/mcp/")
			if isPageLoad && isLoopbackName(host) && !strings.EqualFold(host, hostName(base.Host)) {
				target := url.URL{Scheme: base.Scheme, Host: base.Host, Path: r.URL.Path, RawQuery: r.URL.RawQuery}
				http.Redirect(w, r, target.String(), http.StatusTemporaryRedirect)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isAllowedHost reports whether hostport, a Host header with or without a port, names localhost or one of its
// subdomains, is an IP address, or is one of allowed (any case).
func isAllowedHost(hostport string, allowed []string) bool {
	host := hostName(hostport)
	if host == "" {
		return false
	}
	if isLoopbackName(host) || net.ParseIP(host) != nil {
		return true
	}
	return slices.ContainsFunc(allowed, func(a string) bool { return strings.EqualFold(a, host) })
}

// hostName returns the name in hostport, a Host header or URL host with or without a port: brackets around an IPv6
// address and a trailing dot removed.
func hostName(hostport string) string {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.Trim(host, "[]"), ".")
}

// isLoopbackName reports whether host names this machine: localhost or one of its subdomains (any case), or a
// loopback address.
func isLoopbackName(host string) bool {
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
