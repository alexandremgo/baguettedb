/** Labeled storage-usage progress bar with threshold colors; hidden when the limit is unlimited (0). */

import { formatBytes } from "../../utils/bytes";
import { usageLevel } from "./usageLevel";
import styles from "./UsageMeter.module.css";

interface UsageMeterProps {
  label: string;
  usedBytes: number;
  /** 0 means unlimited: the meter renders nothing. */
  limitBytes: number;
  className?: string;
}

export function UsageMeter({ label, usedBytes, limitBytes, className }: UsageMeterProps) {
  if (limitBytes <= 0) return null;

  const level = usageLevel(usedBytes, limitBytes);
  const pct = Math.min((usedBytes / limitBytes) * 100, 100);

  return (
    <div className={[styles.meter, className ?? ""].filter(Boolean).join(" ")}>
      <div className={styles.header}>
        <span className={styles.label}>{label}</span>
        <span className={styles.values}>
          {formatBytes(usedBytes)} / {formatBytes(limitBytes)}
        </span>
      </div>
      <div
        className={styles.track}
        role="progressbar"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={limitBytes}
        aria-valuenow={Math.min(usedBytes, limitBytes)}
      >
        <div className={[styles.fill, styles[level]].join(" ")} style={{ width: `${pct}%` }} />
      </div>
    </div>
  );
}
