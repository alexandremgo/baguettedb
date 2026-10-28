/** Colored pill label for roles (owner/editor/viewer) and status indicators. */

import type { ReactNode } from "react";
import styles from "./Badge.module.css";

type BadgeVariant = "default" | "owner" | "editor" | "viewer" | "destructive" | "success";

interface BadgeProps {
  variant?: BadgeVariant;
  children: ReactNode;
  className?: string;
}

export function Badge({ variant = "default", children, className }: BadgeProps) {
  return (
    <span
      className={[styles.badge, styles[variant], className ?? ""]
        .filter(Boolean)
        .join(" ")}
    >
      {children}
    </span>
  );
}
