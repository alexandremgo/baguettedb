package cli

import (
	"net"
	"strings"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// TestPrintServing checks the lines bdb serve starts with: the served directory and URLs; where and as whom the owner
// signs in when the binary has the web app and the owner a password, the init hint when the owner has none, or the
// note on building a binary with the web app; and the token hint every time.
func TestPrintServing(t *testing.T) {
	withPassword := domain.User{Email: "ada@example.com", HasPassword: true}
	withoutPassword := domain.User{Email: "baker@bdb.localhost"}
	for _, tc := range []struct {
		name          string
		webApp        bool
		owner         domain.User
		want, notWant []string
	}{
		{"the web app, an owner with a password", true, withPassword,
			[]string{"Serving /data on http://127.0.0.1:3035 (MCP at http://127.0.0.1:3035/mcp)",
				"Sign in at http://127.0.0.1:3035 as ada@example.com, with your password.",
				"`bdb tokens create <token-name>`"},
			[]string{"no web app", "no password"}},
		{"the web app, an owner without a password", true, withoutPassword,
			[]string{"The owner, baker@bdb.localhost, has no password yet to sign in to the web app with: run " +
				"`bdb init`.", "`bdb tokens create <token-name>`"},
			[]string{"Sign in at", "no web app"}},
		{"no web app", false, withPassword,
			[]string{"Serving /data", "This build has no web app", "`make build-cli-ui`", "`bdb tokens create"},
			[]string{"Sign in at"}},
	} {
		var out strings.Builder
		if err := printServing(&out, "bdb", "/data", "http://127.0.0.1:3035", tc.webApp, tc.owner); err != nil {
			t.Fatalf("%s: printServing: %v", tc.name, err)
		}
		for _, w := range tc.want {
			if !strings.Contains(out.String(), w) {
				t.Errorf("%s: %q lacks %q", tc.name, out.String(), w)
			}
		}
		for _, w := range tc.notWant {
			if strings.Contains(out.String(), w) {
				t.Errorf("%s: %q holds %q", tc.name, out.String(), w)
			}
		}
	}
}

// TestCheckServeURL checks the URLs serve --url takes: absolute http or https URLs with a host, a path or a port
// included; anything else is an error.
func TestCheckServeURL(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1:3035", "http://127.0.0.1:3035/", "https://bdb.example.com/base/", "http://[::1]:3035",
	} {
		if err := checkServeURL(raw); err != nil {
			t.Errorf("checkServeURL(%q) = %v, want nil", raw, err)
		}
	}
	for _, raw := range []string{"nas.local:3035", "ftp://nas.local", "http://", "://nas.local", ""} {
		if err := checkServeURL(raw); err == nil {
			t.Errorf("checkServeURL(%q) = nil, want an error", raw)
		}
	}
}

// TestListenedAs checks the address the network warning names: the host as --listen gave it, 0.0.0.0 included, with
// the port actually listened on.
func TestListenedAs(t *testing.T) {
	any6 := &net.TCPAddr{IP: net.IPv6unspecified, Port: 3039}
	for listen, want := range map[string]string{
		"0.0.0.0:3039":      "0.0.0.0:3039",
		"0.0.0.0:0":         "0.0.0.0:3039",
		":3039":             ":3039",
		"192.168.1.10:3039": "192.168.1.10:3039",
		"[::]:3039":         "[::]:3039",
		"not an address":    "[::]:3039",
	} {
		if got := listenedAs(listen, any6); got != want {
			t.Errorf("listenedAs(%q) = %q, want %q", listen, got, want)
		}
	}
}
