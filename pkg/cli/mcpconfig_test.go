package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// TestRenderMCPConfig checks each client's snippet for a binary path with a space and a data directory with a single
// quote: the Claude Desktop entry is valid JSON with the command and arguments as given and names the config file of
// the operating system (none on Linux, where only the Claude Code part is printed), the one-liners quote for the
// shell, and the TOML strings are quoted.
func TestRenderMCPConfig(t *testing.T) {
	server := domain.NewMCPServerCommand("/Users/me/My Tools/bdb", "/Users/me/it's", "")
	quoted := `'/Users/me/My Tools/bdb' mcp --data-dir '/Users/me/it'\''s'`
	claudeCode := "claude mcp add --scope user bdb-local -- " + quoted + "\n"

	t.Run("claude on macOS", func(t *testing.T) {
		out, err := renderMCPConfig(domain.MCPClientClaude, "bdb-local", server, "darwin")
		if err != nil {
			t.Fatalf("renderMCPConfig: %v", err)
		}
		if !strings.Contains(out, "merge into ~/Library/Application Support/Claude/claude_desktop_config.json") ||
			!strings.Contains(out, claudeCode) {
			t.Errorf("snippet lacks the Claude Desktop config path or the Claude Code command:\n%s", out)
		}
		desktop := out[strings.Index(out, "{"):strings.Index(out, "\n\nClaude Code")]
		var cfg struct {
			MCPServers map[string]struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(desktop), &cfg); err != nil {
			t.Fatalf("the Claude Desktop entry is not JSON: %v\n%s", err, desktop)
		}
		got := cfg.MCPServers["bdb-local"]
		if got.Command != server.Path || strings.Join(got.Args, " ") != strings.Join(server.Args, " ") {
			t.Errorf("entry bdb-local = %+v, want %+v", got, server)
		}
	})

	t.Run("claude on Linux", func(t *testing.T) {
		out, err := renderMCPConfig(domain.MCPClientClaude, "bdb-local", server, "linux")
		if err != nil {
			t.Fatalf("renderMCPConfig: %v", err)
		}
		if strings.Contains(out, "Claude Desktop") || !strings.Contains(out, claudeCode) {
			t.Errorf("want only the Claude Code command on Linux:\n%s", out)
		}
	})

	toml := "command = \"/Users/me/My Tools/bdb\"\nargs = [\"mcp\", \"--data-dir\", \"/Users/me/it's\"]\n"
	for _, tt := range []struct {
		client string
		want   []string
	}{
		{
			domain.MCPClientCodex,
			[]string{"codex mcp add bdb-local -- " + quoted + "\n", "[mcp_servers.bdb-local]\n" + toml},
		},
		{domain.MCPClientVibe, []string{"[[mcp_servers]]\nname = \"bdb_local\"\ntransport = \"stdio\"\n" + toml}},
	} {
		t.Run(tt.client, func(t *testing.T) {
			out, err := renderMCPConfig(tt.client, domain.MCPServerKey(tt.client, ""), server, "darwin")
			if err != nil {
				t.Fatalf("renderMCPConfig: %v", err)
			}
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("snippet lacks %q:\n%s", want, out)
				}
			}
		})
	}

	t.Run("unknown client", func(t *testing.T) {
		if _, err := renderMCPConfig("cursor", "bdb-local", server, "darwin"); err == nil {
			t.Error("renderMCPConfig accepted an unknown client")
		}
	})
}

// TestRenderMCPHTTPConfig checks each client's snippet for reaching a bdb serve over HTTP: how to create the token,
// then the Claude Code command and its JSON entry (valid JSON with the endpoint and the Authorization header, the
// placeholder unescaped), the Claude Desktop note only where Claude Desktop runs, and the Codex and Vibe TOML.
func TestRenderMCPHTTPConfig(t *testing.T) {
	const endpoint = "http://127.0.0.1:3035/mcp"
	intro := "`bdb tokens create <token-name>`, and put it in place of <token>."

	t.Run("claude on macOS", func(t *testing.T) {
		out, err := renderMCPHTTPConfig(domain.MCPClientClaude, "bdb-serve", endpoint, "bdb", "", "darwin")
		if err != nil {
			t.Fatalf("renderMCPHTTPConfig: %v", err)
		}
		command := "claude mcp add --scope user --transport http bdb-serve " + endpoint +
			" --header 'Authorization: Bearer <token>'\n"
		if !strings.Contains(out, intro) || !strings.Contains(out, command) ||
			!strings.Contains(out, "register it over stdio instead, `bdb mcp config --client claude`") {
			t.Errorf("snippet lacks the token line, the Claude Code command or the Claude Desktop note:\n%s", out)
		}
		entry := out[strings.Index(out, "{") : strings.LastIndex(out, "}")+1]
		var cfg struct {
			MCPServers map[string]struct {
				Type    string            `json:"type"`
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(entry), &cfg); err != nil || strings.Contains(entry, `\u003c`) {
			t.Fatalf("the Claude Code entry is not plain JSON: %v\n%s", err, entry)
		}
		if got := cfg.MCPServers["bdb-serve"]; got.Type != "http" || got.URL != endpoint ||
			got.Headers["Authorization"] != "Bearer <token>" {
			t.Errorf("entry bdb-serve = %+v, want type http, the endpoint and the Authorization header", got)
		}
	})

	t.Run("another account", func(t *testing.T) {
		out, err := renderMCPHTTPConfig(domain.MCPClientCodex, "bdb-serve-alice", endpoint, "bdb", "alice@example.com",
			"darwin")
		want := "`bdb --as alice@example.com tokens create <token-name>`"
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("renderMCPHTTPConfig = %v; want the token created as alice, %q:\n%s", err, want, out)
		}
	})

	t.Run("claude on Linux", func(t *testing.T) {
		out, err := renderMCPHTTPConfig(domain.MCPClientClaude, "bdb-serve", endpoint, "bdb", "", "linux")
		if err != nil || strings.Contains(out, "Claude Desktop") {
			t.Errorf("renderMCPHTTPConfig = %v; want no Claude Desktop note on Linux:\n%s", err, out)
		}
	})

	for _, tt := range []struct {
		client, want string
	}{
		{domain.MCPClientCodex, "[mcp_servers.bdb-serve]\nurl = \"" + endpoint + "\"\n" +
			"http_headers = { Authorization = \"Bearer <token>\" }\n"},
		{domain.MCPClientVibe, "[[mcp_servers]]\nname = \"bdb_serve\"\ntransport = \"streamable-http\"\nurl = \"" +
			endpoint + "\"\nheaders = { Authorization = \"Bearer <token>\" }\n"},
	} {
		t.Run(tt.client, func(t *testing.T) {
			out, err := renderMCPHTTPConfig(tt.client, domain.MCPServeKey(tt.client, ""), endpoint, "bdb", "", "darwin")
			if err != nil || !strings.Contains(out, intro) || !strings.Contains(out, tt.want) {
				t.Errorf("renderMCPHTTPConfig = %v; want the token line and %q:\n%s", err, tt.want, out)
			}
		})
	}
}
