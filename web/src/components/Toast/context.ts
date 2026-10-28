import { createContext } from "react";

/** An optional action button rendered inside a toast (e.g. Undo); selecting it dismisses the toast. */
export interface ToastAction {
  label: string;
  onClick: () => void;
}

export interface ToastContextValue {
  toast: (
    message: string,
    variant?: "default" | "success" | "error",
    durationMs?: number,
    action?: ToastAction,
  ) => void;
}

export const ToastContext = createContext<ToastContextValue | null>(null);
