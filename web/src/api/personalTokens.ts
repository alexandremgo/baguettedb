/** Personal access tokens API: the signed-in user's tokens, which scripts and MCP clients send as a bearer token. */

import { del, get, post } from "./client";

/** Kind prefix every personal access token's value starts with. */
export const PERSONAL_TOKEN_PREFIX = "bdbp_";

/** A personal access token as the API lists it, never with its value. */
export interface PersonalToken {
  id: string;
  name: string;
  /** Start of the value after PERSONAL_TOKEN_PREFIX, enough to tell the token apart. */
  token_prefix: string;
  created_at: string;
  last_used_at: string | null;
  /** When the token stops working; absent when it never does. */
  expires_at?: string;
}

/** A token just created, with its value: the only time the API returns it. */
export interface CreatedPersonalToken extends PersonalToken {
  token: string;
}

export async function listPersonalTokens(): Promise<PersonalToken[]> {
  const res = await get<{ tokens: PersonalToken[] }>("/api/auth/v1/personal-tokens");
  return res.tokens;
}

/** Creates a token named name, working until expiresAt (an ISO time), or until revoked when absent. */
export function createPersonalToken(name: string, expiresAt?: string): Promise<CreatedPersonalToken> {
  return post<CreatedPersonalToken>("/api/auth/v1/personal-tokens", { name, expires_at: expiresAt });
}

export function revokePersonalToken(id: string): Promise<void> {
  return del(`/api/auth/v1/personal-tokens/${encodeURIComponent(id)}`);
}
