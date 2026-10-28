package domain

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// MCP clients the command line writes registration snippets for, in the order the help lists them.
const (
	MCPClientClaude = "claude"
	MCPClientCodex  = "codex"
	MCPClientVibe   = "vibe"
)

// MCPClients returns the clients the command line knows, in the order the help lists them.
func MCPClients() []string {
	return []string{MCPClientClaude, MCPClientCodex, MCPClientVibe}
}

// MCPClientProduct returns the products client stands for, or "" for a client the command line does not know.
func MCPClientProduct(client string) string {
	switch client {
	case MCPClientClaude:
		return "Claude Desktop and Claude Code"
	case MCPClientCodex:
		return "OpenAI Codex CLI"
	case MCPClientVibe:
		return "Mistral Vibe"
	default:
		return ""
	}
}

// MCPServerKey returns the name the local instance is registered under in client: bdb-local, followed by the local
// part of as when it names the account to act as, so each person on one machine has an entry of their own. Vibe
// prefixes every tool name with the server's name, and reads better with underscores there.
func MCPServerKey(client, as string) string {
	return serverKey(client, "bdb-local", as)
}

// MCPServeKey returns the name an instance served over HTTP by bdb serve is registered under in client: bdb-serve,
// apart from MCPServerKey's so registering both forms of one data directory keeps both, followed by as's local part as
// MCPServerKey says.
func MCPServeKey(client, as string) string {
	return serverKey(client, "bdb-serve", as)
}

// serverKey returns base, followed by the local part of the email as when it is not empty, each run of characters a
// server name can't hold turned into one -, then cut to 64 characters; with every - an _ for Vibe.
func serverKey(client, base, as string) string {
	key := base
	if local, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(as)), "@"); local != "" {
		words := strings.FieldsFunc(local, func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < '0' || r > '9')
		})
		if len(words) > 0 {
			key += "-" + strings.Join(words, "-")
		}
	}
	key = key[:min(len(key), 64)]
	if client == MCPClientVibe {
		key = strings.ReplaceAll(key, "-", "_")
	}
	return strings.TrimRight(key, "-_")
}

// mcpServerName matches the names an MCP server may be registered under: every client takes them, as a shell word, a
// JSON key and a bare TOML key alike.
var mcpServerName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ErrInvalidMCPServerName reports a name an MCP server can't be registered under.
var ErrInvalidMCPServerName = errors.New("want 1 to 64 letters, digits, - or _")

// ValidateMCPServerName returns an error wrapping ErrInvalidMCPServerName when name isn't one to register an MCP
// server under.
func ValidateMCPServerName(name string) error {
	if !mcpServerName.MatchString(name) {
		return fmt.Errorf("%q: %w", name, ErrInvalidMCPServerName)
	}
	return nil
}

// ErrInvalidServeURL reports a URL a server can't be reached at: not http or https, or without a host.
var ErrInvalidServeURL = errors.New(
	"want the http or https URL the server is reached at, such as http://127.0.0.1:3035")

// MCPEndpoint returns the URL of the MCP endpoint of the server reached at base: base followed by /mcp, or base itself
// when it already ends with /mcp. A base that isn't an http or https URL with a host is an ErrInvalidServeURL.
func MCPEndpoint(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" ||
		u.Fragment != "" {
		return "", fmt.Errorf("%q: %w", base, ErrInvalidServeURL)
	}
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/mcp") + "/mcp"
	return u.String(), nil
}

// MCPServerCommand is the command line an MCP client runs to start the local instance.
type MCPServerCommand struct {
	Path string
	Args []string
}

// NewMCPServerCommand returns the command that serves MCP from the binary at path. dataDir is the data directory to
// pass with --data-dir, or "" to let the started binary resolve its default the same way; as is the email of the
// account to act as, passed with --as, or "" for the owner.
func NewMCPServerCommand(path, dataDir, as string) MCPServerCommand {
	cmd := MCPServerCommand{Path: path, Args: []string{"mcp"}}
	if dataDir != "" {
		cmd.Args = append(cmd.Args, "--data-dir", dataDir)
	}
	if as != "" {
		cmd.Args = append(cmd.Args, "--as", as)
	}
	return cmd
}

// ClaudeDesktopConfigPath returns where Claude Desktop keeps its configuration on the operating system goos (a
// runtime.GOOS value), or "" where Claude Desktop does not run.
func ClaudeDesktopConfigPath(goos string) string {
	switch goos {
	case "darwin":
		return "~/Library/Application Support/Claude/claude_desktop_config.json"
	case "windows":
		return `%APPDATA%\Claude\claude_desktop_config.json`
	default:
		return ""
	}
}
