/**
 * HTTP client for the BaguetteDB REST API.
 * Sends httpOnly cookies automatically and handles 401 with silent token refresh, or, in token mode, which has no
 * refresh, by sending the browser back to the sign-in page.
 */

import { getConfig, isTokenMode } from "../config";

interface ApiErrorResponse {
  error: string;
}

export interface RequestOptions {
  /**
   * Skips the automatic redirect on 401 (to / in OAuth mode, to the sign-in page in token mode). Useful for session
   * checks where a 401 is expected.
   */
  skipRedirect?: boolean;
}

export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

/**
 * Singleton refresh promise — prevents multiple concurrent refresh calls
 * when several requests get 401 at the same time.
 */
let refreshPromise: Promise<boolean> | null = null;

function tryRefresh(): Promise<boolean> {
  if (refreshPromise) return refreshPromise;

  const { apiUrl } = getConfig();
  refreshPromise = fetch(`${apiUrl}/api/auth/v1/token/refresh`, {
    method: "POST",
    credentials: "include",
  })
    .then((res) => res.ok)
    .catch(() => false)
    .finally(() => {
      refreshPromise = null;
    });

  return refreshPromise;
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  options?: RequestOptions,
): Promise<T> {
  const { apiUrl } = getConfig();

  const res = await fetch(`${apiUrl}${path}`, {
    method,
    headers: { "Content-Type": "application/json" },
    credentials: "include",
    body: body ? JSON.stringify(body) : undefined,
  });

  if (res.status === 401 && isTokenMode()) {
    if (!options?.skipRedirect) {
      window.location.href = "/sign-in?ended=1";
    }
    throw new ApiError(401, "Unauthorized");
  }

  if (res.status === 401) {
    const refreshed = await tryRefresh();
    if (refreshed) {
      const retry = await fetch(`${apiUrl}${path}`, {
        method,
        headers: { "Content-Type": "application/json" },
        credentials: "include",
        body: body ? JSON.stringify(body) : undefined,
      });

      if (retry.ok) {
        if (retry.status === 204) return undefined as T;
        return retry.json();
      }
    }

    if (!options?.skipRedirect) {
      window.location.href = "/";
    }
    throw new ApiError(401, "Unauthorized");
  }

  if (!res.ok) {
    let message = `Request failed with status ${res.status}`;
    try {
      const err: ApiErrorResponse = await res.json();
      message = err.error;
    } catch {
      // Response body wasn't JSON
    }
    throw new ApiError(res.status, message);
  }

  if (res.status === 204) {
    return undefined as T;
  }

  return res.json();
}

export function get<T>(path: string, options?: RequestOptions): Promise<T> {
  return request<T>("GET", path, undefined, options);
}

export function post<T>(path: string, body?: unknown, options?: RequestOptions): Promise<T> {
  return request<T>("POST", path, body, options);
}

export function patch<T>(path: string, body?: unknown, options?: RequestOptions): Promise<T> {
  return request<T>("PATCH", path, body, options);
}

export function put<T>(path: string, body?: unknown, options?: RequestOptions): Promise<T> {
  return request<T>("PUT", path, body, options);
}

export function del(path: string, options?: RequestOptions): Promise<void> {
  return request<void>("DELETE", path, undefined, options);
}
