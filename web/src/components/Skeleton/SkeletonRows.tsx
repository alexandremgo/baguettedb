/** SkeletonRows renders a column of shimmering row placeholders while list-shaped content loads. */

import { Skeleton } from "./Skeleton";
import styles from "./Skeleton.module.css";

export function SkeletonRows({ count = 3 }: { count?: number }) {
  return (
    <div className={styles.list} aria-label="Loading…">
      {Array.from({ length: count }, (_, i) => (
        <Skeleton key={i} className={styles.row} />
      ))}
    </div>
  );
}
