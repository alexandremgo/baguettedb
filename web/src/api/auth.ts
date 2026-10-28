/** Auth API — profile, password management, and session lifecycle. */

import { getConfig } from "../config";
import { ApiError, post, get, type RequestOptions } from "./client";

export interface User {
  id: string;
  email: string;
  first_name: string;
  last_name: string;
  roles?: string[];
  created_at: string;
  is_active: boolean;
}

export interface ChangePasswordRequest {
  old_password: string;
  new_password: string;
  /** Ends every session of the user, this browser's included; absent means true. */
  end_sessions?: boolean;
}

export function getMe(options?: RequestOptions): Promise<User> {
  return get<User>("/api/auth/v1/me", options);
}

export function changePassword(oldPassword: string, newPassword: string, endSessions?: boolean): Promise<void> {
  const body: ChangePasswordRequest = {
    old_password: oldPassword,
    new_password: newPassword,
    end_sessions: endSessions,
  };
  return post("/api/auth/v1/password", body);
}

/**
 * Calls the server to sign the browser out and expire its auth cookies: it revokes the refresh token with Hydra in
 * OAuth mode, and the browser's own personal access token in token mode.
 */
export async function logout(): Promise<void> {
  const { apiUrl } = getConfig();
  await fetch(`${apiUrl}/api/auth/v1/logout`, {
    method: "POST",
    credentials: "include",
  });
}

/**
 * Signs the browser in with its user's email and password: the server checks them and sets a session as an httpOnly
 * cookie. Token mode only. Throws an ApiError with status 401 when the email and password match no account, whichever
 * of them is wrong.
 */
export function signIn(email: string, password: string): Promise<void> {
  return postSession("/api/auth/v1/session", { email, password });
}

/**
 * Posts body to a route that signs the browser in, outside the API client: a 401 there means the credential was
 * refused, not that a session expired, so it neither refreshes nor redirects.
 */
async function postSession(path: string, body: unknown): Promise<void> {
  const { apiUrl } = getConfig();
  const res = await fetch(`${apiUrl}${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "include",
    body: JSON.stringify(body),
  });
  if (res.ok) return;

  let message = `Request failed with status ${res.status}`;
  try {
    const err: { error?: string } = await res.json();
    if (err.error) message = err.error;
  } catch {
    // Response body wasn't JSON
  }
  throw new ApiError(res.status, message);
}
