package cli

import (
	"fmt"
	"strings"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// mcpClientList returns one indented line per client, its --client value then the products it stands for.
func mcpClientList() string {
	var b strings.Builder
	for _, client := range domain.MCPClients() {
		fmt.Fprintf(&b, "  %-16s%s\n", client, domain.MCPClientProduct(client))
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// shellCommand returns cmd as a POSIX shell command line.
func shellCommand(cmd domain.MCPServerCommand) string {
	return shellJoin(append([]string{cmd.Path}, cmd.Args...))
}

// renderMCPConfig returns what to register in client, under the name key, so it starts the local instance with cmd,
// on the operating system goos (a runtime.GOOS value): for each product, a short instruction line, then the snippet.
// It returns an error for a client it does not know.
func renderMCPConfig(client, key string, cmd domain.MCPServerCommand, goos string) (string, error) {
	switch client {
	case domain.MCPClientClaude:
		code := "Claude Code: run, to register it for every project:\n\n" +
			"claude mcp add --scope user " + key + " -- " + shellCommand(cmd) + "\n"
		configPath := domain.ClaudeDesktopConfigPath(goos)
		if configPath == "" {
			return code, nil
		}
		type server struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		entry, err := jsonEntry(map[string]map[string]server{"mcpServers": {key: {Command: cmd.Path, Args: cmd.Args}}})
		if err != nil {
			return "", fmt.Errorf("encoding the Claude Desktop entry: %w", err)
		}
		return "Claude Desktop: merge into " + configPath + ", then restart Claude Desktop:\n\n" + entry + "\n" + code,
			nil
	case domain.MCPClientCodex:
		return "Run:\n\n" +
			"codex mcp add " + key + " -- " + shellCommand(cmd) + "\n\n" +
			"or add to ~/.codex/config.toml:\n\n" +
			"[mcp_servers." + key + "]\n" +
			"command = " + tomlString(cmd.Path) + "\n" +
			"args = " + tomlStrings(cmd.Args) + "\n", nil
	case domain.MCPClientVibe:
		return "Add to ~/.vibe/config.toml:\n\n" +
			"[[mcp_servers]]\n" +
			"name = " + tomlString(key) + "\n" +
			"transport = \"stdio\"\n" +
			"command = " + tomlString(cmd.Path) + "\n" +
			"args = " + tomlStrings(cmd.Args) + "\n", nil
	default:
		return "", fmt.Errorf("unknown client %q: want one of %s", client, strings.Join(domain.MCPClients(), ", "))
	}
}

// tokenPlaceholder stands for the personal access token in the snippets renderMCPHTTPConfig returns.
const tokenPlaceholder = "<token>"

// renderMCPHTTPConfig returns what to register in client, under the name key, so it reaches the MCP endpoint of a bdb
// serve over HTTP with a personal access token, on the operating system goos (a runtime.GOOS value): how to create the
// token with the command line named bin, for the account as names (the owner when empty), then for each product a
// short instruction line and the snippet, the token left as tokenPlaceholder. It returns an error for a client it does
// not know.
func renderMCPHTTPConfig(client, key, endpoint, bin, as, goos string) (string, error) {
	authorization := "Bearer " + tokenPlaceholder
	run := bin
	if as != "" {
		run += " --as " + as
	}
	intro := "Create a token on the machine running " + bin + " serve, `" + run + " tokens create <token-name>`, and " +
		"put it in place of " + tokenPlaceholder + ".\n\n"

	switch client {
	case domain.MCPClientClaude:
		type server struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		}
		entry, err := jsonEntry(map[string]map[string]server{"mcpServers": {key: {
			Type: "http", URL: endpoint, Headers: map[string]string{"Authorization": authorization},
		}}})
		if err != nil {
			return "", fmt.Errorf("encoding the Claude Code entry: %w", err)
		}
		out := intro + "Claude Code: run, to register it for every project:\n\n" +
			shellJoin([]string{"claude", "mcp", "add", "--scope", "user", "--transport", "http", key, endpoint,
				"--header", "Authorization: " + authorization}) + "\n\n" +
			"or merge into ~/.claude.json (every project) or a project's .mcp.json:\n\n" + entry
		if domain.ClaudeDesktopConfigPath(goos) != "" {
			out += "\nClaude Desktop reaches servers over HTTP through its connectors, which can't send a token: on " +
				"the machine running " + bin + " serve, register it over stdio instead, `" + run +
				" mcp config --client claude`.\n"
		}
		return out, nil
	case domain.MCPClientCodex:
		return intro + "Add to ~/.codex/config.toml:\n\n" +
			"[mcp_servers." + key + "]\n" +
			"url = " + tomlString(endpoint) + "\n" +
			"http_headers = { Authorization = " + tomlString(authorization) + " }\n", nil
	case domain.MCPClientVibe:
		return intro + "Add to ~/.vibe/config.toml:\n\n" +
			"[[mcp_servers]]\n" +
			"name = " + tomlString(key) + "\n" +
			"transport = \"streamable-http\"\n" +
			"url = " + tomlString(endpoint) + "\n" +
			"headers = { Authorization = " + tomlString(authorization) + " }\n", nil
	default:
		return "", fmt.Errorf("unknown client %q: want one of %s", client, strings.Join(domain.MCPClients(), ", "))
	}
}

// jsonEntry returns v as indented JSON followed by a newline, with <, > and & kept as they are.
func jsonEntry(v any) (string, error) {
	var b strings.Builder
	if err := writeJSON(&b, v); err != nil {
		return "", err
	}
	return b.String(), nil
}
