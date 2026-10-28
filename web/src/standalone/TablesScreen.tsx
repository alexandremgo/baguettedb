/** Standalone table list — tap a table to browse its rows read-only. */

import { Link, useParams } from "react-router-dom";
import { EmptyState } from "../components/EmptyState";
import { ChevronRight, Table2 } from "../components/icons";
import { useTables } from "../hooks/useTables";
import { SkeletonRows } from "../components/Skeleton";
import styles from "./Standalone.module.css";

export function TablesScreen() {
  const { bdbId } = useParams<{ bdbId: string }>();
  const { data: tables = [], isLoading, error } = useTables(bdbId);

  if (isLoading) return <SkeletonRows />;
  if (error) return <p className={styles.error}>Couldn't load tables.</p>;
  if (tables.length === 0) {
    return (
      <EmptyState
        icon={Table2}
        title="No tables yet."
        hint="Ask your assistant to set this bdb up."
      />
    );
  }

  return (
    <ul className={styles.list}>
      {tables.map((t) => (
        <li key={t}>
          <Link to={`/app/bdbs/${bdbId}/tables/${encodeURIComponent(t)}`} className={styles.item}>
            <span className={styles.itemTitle}>{t}</span>
            <ChevronRight size={16} className={styles.itemChevron} aria-hidden="true" />
          </Link>
        </li>
      ))}
    </ul>
  );
}
