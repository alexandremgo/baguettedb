/** Returns the toast function. Must be used within ToastProvider. */

import { useContext } from "react";
import { ToastContext } from "./context";

export function useToast() {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error("useToast must be used within ToastProvider");
  return ctx;
}
