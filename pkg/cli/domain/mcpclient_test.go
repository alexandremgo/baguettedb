package domain

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// TestNewMCPServerCommand checks that the server command passes --data-dir only when a data directory is given, and
// --as only when an account is.
func TestNewMCPServerCommand(t *testing.T) {
	got := NewMCPServerCommand("/bin/bdb", "", "")
	if got.Path != "/bin/bdb" || !slices.Equal(got.Args, []string{"mcp"}) {
		t.Errorf("default data dir: %+v, want /bin/bdb mcp", got)
	}
	got = NewMCPServerCommand("/bin/bdb", "/srv/bdb", "")
	if !slices.Equal(got.Args, []string{"mcp", "--data-dir", "/srv/bdb"}) {
		t.Errorf("chosen data dir: %+v, want mcp --data-dir /srv/bdb", got)
	}
	got = NewMCPServerCommand("/bin/bdb", "", "alice@example.com")
	if !slices.Equal(got.Args, []string{"mcp", "--as", "alice@example.com"}) {
		t.Errorf("another account: %+v, want mcp --as alice@example.com", got)
	}
}

// TestMCPClients checks that every known client names its products and that only Vibe gets the underscored keys, the
// HTTP one apart from the stdio one.
func TestMCPClients(t *testing.T) {
	for _, client := range MCPClients() {
		if MCPClientProduct(client) == "" {
			t.Errorf("client %q names no product", client)
		}
		want := "bdb-local"
		if client == MCPClientVibe {
			want = "bdb_local"
		}
		if got := MCPServerKey(client, ""); got != want {
			t.Errorf("MCPServerKey(%q) = %q, want %q", client, got, want)
		}
		if got, want := MCPServeKey(client, ""), strings.Replace(want, "local", "serve", 1); got != want {
			t.Errorf("MCPServeKey(%q) = %q, want %q", client, got, want)
		}
	}
	if MCPClientProduct("cursor") != "" {
		t.Error("an unknown client names a product")
	}
}

// TestValidateMCPServerName checks that a name of letters, digits, - and _ is taken, and an empty one, one with a
// space or a dot, and one too long are refused.
func TestValidateMCPServerName(t *testing.T) {
	for _, name := range []string{"bdb-local", "bdb_alice2", "X"} {
		if err := ValidateMCPServerName(name); err != nil {
			t.Errorf("ValidateMCPServerName(%q) = %v, want nil", name, err)
		}
	}
	for _, name := range []string{"", "bdb local", "bdb.local", strings.Repeat("x", 65)} {
		if err := ValidateMCPServerName(name); !errors.Is(err, ErrInvalidMCPServerName) {
			t.Errorf("ValidateMCPServerName(%q) = %v, want ErrInvalidMCPServerName", name, err)
		}
	}
}

// TestMCPEndpoint checks that the endpoint is the base URL's /mcp, a trailing slash or an /mcp already there kept
// single, a path prefix kept, and that a URL without http or https, without a host, or with a query is refused.
func TestMCPEndpoint(t *testing.T) {
	for base, want := range map[string]string{
		"http://127.0.0.1:3035":    "http://127.0.0.1:3035/mcp",
		"http://127.0.0.1:3035/":   "http://127.0.0.1:3035/mcp",
		"https://nas.local/mcp":    "https://nas.local/mcp",
		"https://example.com/bdb/": "https://example.com/bdb/mcp",
		"http://[::1]:3035/mcp/":   "http://[::1]:3035/mcp",
	} {
		if got, err := MCPEndpoint(base); err != nil || got != want {
			t.Errorf("MCPEndpoint(%q) = %q, %v; want %q", base, got, err, want)
		}
	}
	for _, base := range []string{"127.0.0.1:3035", "ftp://nas.local", "http://", "http://nas.local/?a=1", ""} {
		if _, err := MCPEndpoint(base); !errors.Is(err, ErrInvalidServeURL) {
			t.Errorf("MCPEndpoint(%q) = %v, want ErrInvalidServeURL", base, err)
		}
	}
}

// TestMCPServerKey_As checks the entry names of an account acted as: the base name followed by the email's local part,
// lowercased, each run of other characters turned into one -, or _ for Vibe; the base alone for no local part; and a
// long one cut to a name every client takes.
func TestMCPServerKey_As(t *testing.T) {
	for _, tc := range []struct {
		client, as, local, serve string
	}{
		{MCPClientClaude, "Alice.Smith@example.com", "bdb-local-alice-smith", "bdb-serve-alice-smith"},
		{MCPClientVibe, "alice.smith+bdb@example.com", "bdb_local_alice_smith_bdb", "bdb_serve_alice_smith_bdb"},
		{MCPClientCodex, "@example.com", "bdb-local", "bdb-serve"},
		{MCPClientCodex, "", "bdb-local", "bdb-serve"},
	} {
		if got := MCPServerKey(tc.client, tc.as); got != tc.local {
			t.Errorf("MCPServerKey(%q, %q) = %q, want %q", tc.client, tc.as, got, tc.local)
		}
		if got := MCPServeKey(tc.client, tc.as); got != tc.serve {
			t.Errorf("MCPServeKey(%q, %q) = %q, want %q", tc.client, tc.as, got, tc.serve)
		}
	}
	long := MCPServerKey(MCPClientClaude, strings.Repeat("a.", 40)+"@example.com")
	if err := ValidateMCPServerName(long); err != nil {
		t.Errorf("the key of a long local part, %q: %v", long, err)
	}
}
