/** Modal dialog using the native <dialog> element. Closes on backdrop click or Escape. */

import { type ReactNode, useEffect, useRef } from "react";
import styles from "./Dialog.module.css";

interface DialogProps {
  open: boolean;
  onClose: () => void;
  title?: string;
  children: ReactNode;
  className?: string;
}

export function Dialog({ open, onClose, title, children, className }: DialogProps) {
  const dialogRef = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;

    if (open && !dialog.open) {
      dialog.showModal();
    } else if (!open && dialog.open) {
      dialog.close();
    }
  }, [open]);

  return (
    <dialog
      ref={dialogRef}
      className={[styles.dialog, className ?? ""].filter(Boolean).join(" ")}
      onClose={(e) => {
        // React's synthetic onClose propagates up the component tree (the native close event does
        // not bubble), so a nested dialog closing — e.g. an action dialog inside an interactive
        // row detail — must not close this one: only react to this dialog's own close.
        if (e.target === dialogRef.current) {
          onClose();
        }
      }}
      onClick={(e) => {
        if (e.target === dialogRef.current) {
          onClose();
        }
      }}
    >
      <div className={styles.content}>
        {title && (
          <div className={styles.header}>
            <h2 className={styles.title}>{title}</h2>
            <button
              className={styles.close}
              onClick={onClose}
              aria-label="Close dialog"
            >
              &times;
            </button>
          </div>
        )}
        <div className={styles.body}>{children}</div>
      </div>
    </dialog>
  );
}
