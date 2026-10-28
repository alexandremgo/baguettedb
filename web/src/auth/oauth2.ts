/**
 * OAuth2 Authorization Code flow with PKCE (RFC 7636).
 *
 * Flow from the SPA's perspective:
 *   1. startLogin() — generates PKCE params, redirects browser to Hydra
 *   2. User authenticates on Go login/consent pages (server-rendered)
 *   3. Hydra redirects back to /callback?code=...&state=...
 *   4. exchangeCode() — sends the auth code + PKCE verifier to the BFF token
 *      endpoint, which exchanges with Hydra and sets httpOnly cookies
 *
 * All URLs come from /config.json (loaded at startup), not build-time env vars.
 */

import { getConfig } from "../config";

export interface TokenExchangeResponse {
  expires_in: number;
}

/** Generates a cryptographically random string from the RFC 3986 unreserved character set. */
function randomString(length: number): string {
  const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~";
  const values = crypto.getRandomValues(new Uint8Array(length));
  return Array.from(values, (v) => chars[v % chars.length]).join("");
}

/** Computes the SHA-256 hash of a string using the Web Crypto API. */
async function sha256(input: string): Promise<ArrayBuffer> {
  const encoder = new TextEncoder();
  return crypto.subtle.digest("SHA-256", encoder.encode(input));
}

/** Encodes a buffer as a base64url string (no padding), per RFC 7636. */
function base64UrlEncode(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer);
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/** Generates a 128-character PKCE code verifier (stored in sessionStorage during the flow). */
export function generateCodeVerifier(): string {
  return randomString(128);
}

/** Derives the S256 code challenge from a code verifier — sent to the authorization server. */
export async function generateCodeChallenge(verifier: string): Promise<string> {
  const hash = await sha256(verifier);
  return base64UrlEncode(hash);
}

/** Generates a random state parameter to prevent CSRF on the OAuth callback. */
export function generateState(): string {
  return randomString(32);
}

/**
 * Initiates the OAuth2 login flow.
 * Generates PKCE params, stores them in sessionStorage, and redirects the browser
 * to Hydra's authorization endpoint. The user will land on the Go login page.
 */
export async function startLogin(): Promise<void> {
  const { oauthClientId, oauthAuthorizeUrl, oauthRedirectUri } = getConfig();
  const verifier = generateCodeVerifier();
  const challenge = await generateCodeChallenge(verifier);
  const state = generateState();

  sessionStorage.setItem("pkce_code_verifier", verifier);
  sessionStorage.setItem("pkce_state", state);

  const params = new URLSearchParams({
    response_type: "code",
    client_id: oauthClientId,
    redirect_uri: oauthRedirectUri,
    code_challenge: challenge,
    code_challenge_method: "S256",
    scope: "openid offline",
    state,
    prompt: "login",
  });

  window.location.href = `${oauthAuthorizeUrl}?${params.toString()}`;
}

/**
 * Exchanges an authorization code for tokens via the BFF token endpoint.
 * The server exchanges with Hydra and sets httpOnly cookies — no tokens
 * are returned in the response body.
 */
export async function exchangeCode(code: string): Promise<TokenExchangeResponse> {
  const { oauthClientId, oauthRedirectUri, apiUrl } = getConfig();
  const verifier = sessionStorage.getItem("pkce_code_verifier");
  if (!verifier) {
    throw new Error("Missing PKCE code verifier");
  }

  const res = await fetch(`${apiUrl}/api/auth/v1/token`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "include",
    body: JSON.stringify({
      code,
      code_verifier: verifier,
      client_id: oauthClientId,
      redirect_uri: oauthRedirectUri,
    }),
  });

  if (!res.ok) {
    const text = await res.text();
    throw new Error(`Token exchange failed: ${text}`);
  }

  sessionStorage.removeItem("pkce_code_verifier");
  sessionStorage.removeItem("pkce_state");

  return res.json();
}
