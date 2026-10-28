import { createContext } from "react";
import type { User } from "../api/auth";

/** Shared shape for the auth context — holds user state and auth actions. */
export interface AuthContextValue {
  user: User | null;
  isAuthenticated: boolean;
  isLoading: boolean;
  /** Starts signing in: the OAuth redirect, or the sign-in page in token mode. */
  login: () => Promise<void>;
  logout: () => Promise<void>;
  handleCallback: (code: string) => Promise<void>;
  /** Token mode: signs in with an email and a password, then loads the user. */
  signInWithPassword: (email: string, password: string) => Promise<void>;
}

export const AuthContext = createContext<AuthContextValue | null>(null);
