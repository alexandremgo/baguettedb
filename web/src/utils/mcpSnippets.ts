/**
 * Snippets registering the MCP endpoint of an instance served by `bdb serve` in a client, with a personal access token
 * as its bearer token: the same as `bdb mcp config --http` prints.
 */

/** Name the entry is registered under, apart from `bdb-local`, the stdio entry of the serving machine. */
export const SERVE_ENTRY_NAME = "bdb-serve";

/** Returns the Claude Code command registering mcpUrl, for every project, with token. */
export function claudeCodeCommand(mcpUrl: string, token: string): string {
  return (
    `claude mcp add --scope user --transport http ${SERVE_ENTRY_NAME} ${mcpUrl} ` +
    `--header "Authorization: Bearer ${token}"`
  );
}

/** Returns the JSON entry registering mcpUrl with token, for a configuration file such as `~/.claude.json`. */
export function mcpConfigEntry(mcpUrl: string, token: string): string {
  const server = { type: "http", url: mcpUrl, headers: { Authorization: `Bearer ${token}` } };
  return JSON.stringify({ mcpServers: { [SERVE_ENTRY_NAME]: server } }, null, 2);
}
