/** EmptyState explains an empty surface: an icon, a one-line title, an optional hint and action. The regular
 *  size owns a whole panel or screen; compact sits inside content areas (a formula's empty result, a bare
 *  table). Copy stays instructional or derived from the user's own names — never invented content. */

import type { ReactNode } from "react";
import type { LucideIcon } from "../icons";
import styles from "./EmptyState.module.css";

interface EmptyStateProps {
  icon: LucideIcon;
  title: string;
  hint?: string;
  action?: ReactNode;
  compact?: boolean;
  className?: string;
}

export function EmptyState({ icon: Icon, title, hint, action, compact, className }: EmptyStateProps) {
  return (
    <div
      className={[styles.root, compact ? styles.compact : "", className ?? ""].filter(Boolean).join(" ")}
    >
      <span className={styles.iconWrap}>
        <Icon size={compact ? 18 : 22} aria-hidden="true" />
      </span>
      <p className={styles.title}>{title}</p>
      {hint && <p className={styles.hint}>{hint}</p>}
      {action && <div className={styles.action}>{action}</div>}
    </div>
  );
}
