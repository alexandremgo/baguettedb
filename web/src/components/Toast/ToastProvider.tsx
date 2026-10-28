/** Toast notification system. Wrap app in ToastProvider, then call useToast().toast(). */

import { useCallback, useState, type ReactNode } from "react";
import { ToastContext, type ToastAction } from "./context";
import styles from "./Toast.module.css";

type ToastVariant = "default" | "success" | "error";

interface Toast {
  id: string;
  message: string;
  variant: ToastVariant;
  action?: ToastAction;
}

const TOAST_DURATION = 4000;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);

  const toast = useCallback(
    (
      message: string,
      variant: ToastVariant = "default",
      durationMs: number = TOAST_DURATION,
      action?: ToastAction,
    ) => {
      const id = crypto.randomUUID();
      setToasts((prev) => [...prev, { id, message, variant, action }]);

      setTimeout(() => {
        setToasts((prev) => prev.filter((t) => t.id !== id));
      }, durationMs);
    },
    [],
  );

  const dismiss = useCallback((id: string) => {
    setToasts((prev) => prev.filter((t) => t.id !== id));
  }, []);

  return (
    <ToastContext.Provider value={{ toast }}>
      {children}
      <div className={styles.container} aria-live="polite">
        {toasts.map((t) => (
          <div
            key={t.id}
            className={[styles.toast, styles[t.variant]].join(" ")}
          >
            <span>{t.message}</span>
            {t.action && (
              <button
                className={styles.action}
                onClick={() => {
                  t.action!.onClick();
                  dismiss(t.id);
                }}
              >
                {t.action.label}
              </button>
            )}
            <button
              className={styles.dismiss}
              onClick={() => dismiss(t.id)}
              aria-label="Dismiss"
            >
              &times;
            </button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}
