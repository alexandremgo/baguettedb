import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { User } from "../api/auth";
import { getMe, logout as apiLogout, signIn } from "../api/auth";
import { isTokenMode } from "../config";
import { startLogin, exchangeCode } from "./oauth2";
import { AuthContext } from "./AuthContext";

/**
 * Provides auth state to the app. On mount, checks if the user has a valid
 * session by calling GET /api/auth/v1/me (cookies are sent automatically).
 * Exposes login (PKCE redirect, or the sign-in page in token mode), logout
 * (server-side cookie clear), handleCallback (code → BFF token exchange), and,
 * in token mode, signInWithPassword.
 */
export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  // The OAuth callback page mints the session itself (code exchange → getMe → setUser), so the mount probe below
  // skips it — probing there guarantees a 401 (+ a doomed refresh attempt) racing the exchange. With no probe to
  // wait for, the callback page starts with isLoading already false.
  const [isLoading, setIsLoading] = useState(() => window.location.pathname !== "/callback");

  useEffect(() => {
    let cancelled = false;

    if (window.location.pathname === "/callback") {
      return;
    }

    async function init() {
      try {
        const me = await getMe({ skipRedirect: true });
        if (!cancelled) setUser(me);
      } catch {
        // No valid session — user is not authenticated.
      }
    }

    init().finally(() => {
      if (!cancelled) setIsLoading(false);
    });

    return () => {
      cancelled = true;
    };
  }, []);

  const login = useCallback(async () => {
    if (isTokenMode()) {
      window.location.href = "/sign-in";
      return;
    }
    await startLogin();
  }, []);

  const logout = useCallback(async () => {
    await apiLogout();
    setUser(null);
    window.location.href = "/";
  }, []);

  const handleCallback = useCallback(async (code: string) => {
    await exchangeCode(code);
    const me = await getMe();
    setUser(me);
  }, []);

  const signInWithPassword = useCallback(async (email: string, password: string) => {
    await signIn(email, password);
    const me = await getMe();
    setUser(me);
  }, []);

  return (
    <AuthContext.Provider
      value={{
        user,
        isAuthenticated: user !== null,
        isLoading,
        login,
        logout,
        handleCallback,
        signInWithPassword,
      }}
    >
      {children}
    </AuthContext.Provider>
  );
}
