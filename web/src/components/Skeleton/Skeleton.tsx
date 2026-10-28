/** Skeleton renders a shimmering placeholder block; the caller sizes it via className. Purely decorative. */

import styles from "./Skeleton.module.css";

export function Skeleton({ className }: { className?: string }) {
  return (
    <span className={[styles.skeleton, className ?? ""].filter(Boolean).join(" ")} aria-hidden="true" />
  );
}
