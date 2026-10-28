/** A bdb row in the standalone list's reorder mode: inert card plus the drag handle that moves it. */

import { useSortable } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { GripVertical } from "../components/icons";
import styles from "./Standalone.module.css";

interface SortableBdbListItemProps {
  id: string;
  name: string;
  role?: string;
}

export function SortableBdbListItem({ id, name, role }: SortableBdbListItemProps) {
  const {
    attributes,
    listeners,
    setNodeRef,
    setActivatorNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id });

  // Pinned to the vertical axis: a row that also followed the pointer sideways would overflow its
  // column and, in a left-to-right layout, grow a scroll region to the right that lets it travel
  // without bound (overflow past the left edge is merely clipped, hence the asymmetry).
  const style = {
    transform: CSS.Transform.toString(transform && { ...transform, x: 0 }),
    transition,
  };

  return (
    <li ref={setNodeRef} style={style}>
      <div className={[styles.item, isDragging ? styles.itemDragging : ""].join(" ")}>
        <span className={styles.itemTitle}>{name}</span>
        <span className={styles.itemTrailing}>
          {role && <span className={styles.badge}>{role}</span>}
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
        </span>
      </div>
    </li>
  );
}
