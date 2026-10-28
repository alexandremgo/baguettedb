/** Runtime configuration loaded from /config.json before the app renders. */

export interface AppConfig {
  apiUrl: string;
  /**
   * How users sign in: "oauth" through the instance's OAuth flow, "token" with login links and personal access tokens
   * on an instance served by `bdb serve`. Absent from older servers, which are OAuth ones.
   */
  authMode?: "oauth" | "token";
  oauthClientId: string;
  oauthAuthorizeUrl: string;
  oauthRedirectUri: string;
  /** Absolute URL MCP clients connect to, on the instance's public origin. */
  mcpUrl: string;
  /** Email of the account owning an instance served by `bdb serve`; absent elsewhere. */
  ownerEmail?: string;
}

let config: AppConfig | null = null;

/** Fetches /config.json and stores it. Must be called before rendering. */
export async function loadConfig(): Promise<void> {
  const res = await fetch("/config.json");
  if (!res.ok) {
    throw new Error("Failed to load /config.json");
  }
  config = await res.json();
}

/** Reports whether users sign in with login links and personal access tokens rather than OAuth. */
export function isTokenMode(): boolean {
  return getConfig().authMode === "token";
}

/** Returns the runtime config. Throws if loadConfig() hasn't been called. */
export function getConfig(): AppConfig {
  if (!config) {
    throw new Error("Config not loaded — call loadConfig() first");
  }
  return config;
}
