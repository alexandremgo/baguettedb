import { useState } from "react";
import {
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import { arrayMove, sortableKeyboardCoordinates } from "@dnd-kit/sortable";
import type { Bdb } from "../api/bdbs";
import { useReorderBdbs } from "./useBdbMutations";

/**
 * Drives the reorder mode shared by the shells that list bdbs: the toggle state, the dnd sensors,
 * and turning a drop into a persisted arrangement. Each shell supplies its own row rendering, since
 * a sidebar entry and a standalone list item look nothing alike.
 */
export function useBdbReorder(bdbs: Bdb[]) {
  const [reordering, setReordering] = useState(false);
  const reorderBdbs = useReorderBdbs();

  // A small distance before a drag starts keeps a tap on the handle from being read as one.
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  function handleDragEnd(event: DragEndEvent) {
    const { active, over } = event;
    if (!over || active.id === over.id) return;

    const from = bdbs.findIndex((bdb) => bdb.id === active.id);
    const to = bdbs.findIndex((bdb) => bdb.id === over.id);
    if (from === -1 || to === -1) return;

    reorderBdbs.mutate(arrayMove(bdbs, from, to).map((bdb) => bdb.id));
  }

  return {
    reordering,
    toggleReordering: () => setReordering((on) => !on),
    /** True once there is more than one bdb, i.e. once an arrangement can differ from the default. */
    canReorder: bdbs.length > 1,
    sensors,
    handleDragEnd,
    sortableIds: bdbs.map((bdb) => bdb.id),
  };
}
