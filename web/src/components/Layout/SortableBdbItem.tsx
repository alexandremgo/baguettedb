/** A bdb row in the sidebar's reorder mode: inert label plus the drag handle that moves it. */

import { useSortable } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { GripVertical } from "../icons";
import styles from "./Sidebar.module.css";

interface SortableBdbItemProps {
  id: string;
  name: string;
}

export function SortableBdbItem({ id, name }: SortableBdbItemProps) {
  const {
    attributes,
    listeners,
    setNodeRef,
    setActivatorNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id });

  // Pinned to the vertical axis. A row that also followed the pointer sideways would overflow the
  // nav column, whose overflow-x computes to auto because the other axis isn't visible — and in a
  // left-to-right layout, overflow past the right edge grows a scroll region, so the row could be
  // dragged away without bound (overflow past the left edge is merely clipped, hence the asymmetry).
  const style = {
    transform: CSS.Transform.toString(transform && { ...transform, x: 0 }),
    transition,
  };

  return (
    <div
      ref={setNodeRef}
      className={[styles.sortableRow, isDragging ? styles.sortableRowDragging : ""].join(" ")}
      style={style}
    >
      <span className={styles.sortableName}>{name}</span>
      {/* The handle is the activator, so keyboard sorting and the aria attributes belong on it
          rather than on the row. */}
      <button
        type="button"
        ref={setActivatorNodeRef}
        className={styles.dragHandle}
        aria-label={`Reorder ${name}`}
        {...attributes}
        {...listeners}
      >
        <GripVertical size={16} aria-hidden="true" focusable="false" />
      </button>
    </div>
  );
}
