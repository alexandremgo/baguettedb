/** Standalone landing — a switcher listing the user's bdbs; tapping one opens its formulas. */

import { Link } from "react-router-dom";
import { DndContext, closestCenter } from "@dnd-kit/core";
import { SortableContext, verticalListSortingStrategy } from "@dnd-kit/sortable";
import { useBdbs } from "../hooks/useBdbs";
import { useBdbReorder } from "../hooks/useBdbReorder";
import { ReorderToggleIcon } from "../components";
import { EmptyState } from "../components/EmptyState";
import { Database } from "../components/icons";
import { SortableBdbListItem } from "./SortableBdbListItem";
import { badgedRole, bdbTitle } from "../utils/bdbDisplay";
import { SkeletonRows } from "../components/Skeleton";
import styles from "./Standalone.module.css";

export function StandaloneHome() {
  const { data: bdbs = [], isLoading, error } = useBdbs();
  const { reordering, toggleReordering, canReorder, sensors, handleDragEnd, sortableIds } =
    useBdbReorder(bdbs);

  if (isLoading) return <SkeletonRows />;
  if (error) return <p className={styles.error}>Couldn't load your bdbs.</p>;

  return (
    <>
      {bdbs.length === 0 ? (
        <EmptyState
          icon={Database}
          title="You're not in any bdbs yet."
          hint="Create your first one below."
        />
      ) : reordering ? (
        <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={handleDragEnd}>
          <SortableContext items={sortableIds} strategy={verticalListSortingStrategy}>
            <ul className={styles.list}>
              {bdbs.map((c) => (
                <SortableBdbListItem key={c.id} id={c.id} name={c.name} role={badgedRole(c.my_role)} />
              ))}
            </ul>
          </SortableContext>
        </DndContext>
      ) : (
        <ul className={styles.list}>
          {bdbs.map((c) => (
            <li key={c.id}>
              <Link to={`/app/bdbs/${c.id}/formulas`} className={styles.item} title={bdbTitle(c)}>
                <span className={styles.itemTitle}>{c.name}</span>
                {badgedRole(c.my_role) && <span className={styles.badge}>{c.my_role}</span>}
              </Link>
            </li>
          ))}
        </ul>
      )}
      {/* Both list-level actions share one row, so reordering costs no vertical space of its own. */}
      <div className={styles.listActions}>
        <Link to="/app/bdbs/new" className={styles.newBdb}>
          + New Bdb
        </Link>
        {canReorder && (
          <button
            type="button"
            className={styles.reorderToggle}
            onClick={toggleReordering}
            aria-label={reordering ? "Done reordering" : "Reorder bdbs"}
            aria-pressed={reordering}
          >
            <ReorderToggleIcon active={reordering} />
          </button>
        )}
      </div>
    </>
  );
}
