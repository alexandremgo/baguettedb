import { useContext } from "react";
import { AuthContext } from "./AuthContext";

/** Returns the current auth state and actions. Must be used within AuthProvider. */
export function useAuth() {
  const ctx = useContext(AuthContext);
  if (!ctx) {
    throw new Error("useAuth must be used within AuthProvider");
  }
  return ctx;
}
