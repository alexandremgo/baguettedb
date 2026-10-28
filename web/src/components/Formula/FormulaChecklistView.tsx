/**
 * Renders a view's rows as a Keep-style checklist, driven by the view's display config: each row
 * shows the title column, a one-tap checkbox when a check column + toggle slot are set, and a `⋯`
 * menu (details, seeded edit, delete-with-undo, plus any attached single-row actions not claimed
 * by a slot). A tap flips the checkbox at once and holds it inert until its run settles, so a slow
 * connection shows the check immediately and a second tap cannot flip it back mid-flight. An add
 * slot renders as a quick-add form above the rows when its action marks params inline, else as a
 * + Add button in the top bar opening the param form. A reorder slot adds a toggle beside it that
 * swaps the rows for drag handles; a drop runs the slot's action once with every rowid in its new
 * order. Viewers get a read-only list; a row tap opens the detail when no toggle claims it.
 */

import { useMemo, useState } from "react";
import { useQueryClient, type InfiniteData, type QueryClient } from "@tanstack/react-query";
import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import type { Column, Value } from "../../api/databases";
import type { Formula, FormulaRunResult } from "../../api/formulas";
import { roleColumn } from "../../api/formulas";
import { Button } from "../Button";
import { EmptyState } from "../EmptyState";
import { GripVertical, Inbox } from "../icons";
import { Menu } from "../Menu";
import { ReorderToggleIcon } from "../ReorderToggleIcon";
import { promptParamsOf } from "./actions";
import { quickAddParams } from "./params";
import { QuickAddForm } from "./QuickAddForm";
import { useSlotInteractions } from "./slotInteractions";
import styles from "./FormulaChecklistView.module.css";

interface FormulaChecklistViewProps {
  bdbId: string;
  /** The view being rendered; its display config names the columns and slots. */
  formula: Formula;
  columns: Column[];
  rows: Value[][];
  rowIdColumn: number;
  /** Every formula attached to this view, for slot resolution and the row menu. */
  attached: Formula[];
  canWrite: boolean;
  hasDetail: boolean;
  onOpenDetail: (rowId: number) => void;
  /** The parent row when this checklist renders inside a row-detail modal. */
  parentRowId?: number | null;
  /** Blocks the reorder mode while the caller knows the rows are a filtered subset — a drag can
   *  only order rows it can see, and renumbering a subset would scramble it against the rest. */
  reorderDisabled?: boolean;
}

/** Reads a 0/1-ish cell as the checkbox state. */
function isChecked(value: unknown): boolean {
  return value === 1 || value === true || value === "1";
}

/** Writes a row's check cell into every cached run of the view — the paged top-level query and any
 *  row-detail run of the same formula — so the checkbox reflects a toggle before its run returns.
 *  The refetch that follows the run replaces the guess with the server's rows. */
function patchCheckCell(
  queryClient: QueryClient,
  bdbId: string,
  formulaId: string,
  rowId: number,
  rowIdColumn: number,
  checkIdx: number,
  checked: boolean,
) {
  const patch = (result: FormulaRunResult): FormulaRunResult => ({
    ...result,
    rows: result.rows.map((row) =>
      row[rowIdColumn]?.value === rowId
        ? row.map((cell, i) => (i === checkIdx ? { type: "integer", value: checked ? 1 : 0 } : cell))
        : row,
    ),
  });
  queryClient.setQueriesData<InfiniteData<FormulaRunResult>>(
    { queryKey: ["formula-run", bdbId, formulaId] },
    (data) => data && { ...data, pages: data.pages.map(patch) },
  );
  queryClient.setQueriesData<FormulaRunResult>(
    { queryKey: ["formula-detail", bdbId, formulaId] },
    (data) => data && patch(data),
  );
}

/** A checklist row in reorder mode: inert title plus the drag handle that moves it. */
function SortableChecklistRow({ id, title }: { id: number; title: string }) {
  const {
    attributes,
    listeners,
    setNodeRef,
    setActivatorNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id });

  // Pinned to the vertical axis so a row can't be dragged out of the list column sideways.
  const style = {
    transform: CSS.Transform.toString(transform && { ...transform, x: 0 }),
    transition,
  };

  return (
    <li
      ref={setNodeRef}
      className={[styles.row, isDragging ? styles.rowDragging : ""].join(" ")}
      style={style}
    >
      <div className={styles.rowMain}>
        <span className={styles.title}>{title}</span>
      </div>
      {/* The handle is the activator, so keyboard sorting and the aria attributes belong on it
          rather than on the row. */}
      <button
        type="button"
        ref={setActivatorNodeRef}
        className={styles.dragHandle}
        aria-label={`Reorder ${title}`}
        {...attributes}
        {...listeners}
      >
        <GripVertical size={16} aria-hidden="true" focusable="false" />
      </button>
    </li>
  );
}

export function FormulaChecklistView({
  bdbId,
  formula,
  columns,
  rows,
  rowIdColumn,
  attached,
  canWrite,
  hasDetail,
  onOpenDetail,
  parentRowId,
  reorderDisabled,
}: FormulaChecklistViewProps) {
  const slot = useSlotInteractions({
    bdbId,
    formula,
    attached,
    canWrite,
    hasDetail,
    onOpenDetail,
    columns,
    parentRowId,
  });

  const display = formula.display;
  const titleColumn = roleColumn(display, "title");
  const checkColumn = roleColumn(display, "check");
  const titleIdx = columns.findIndex((c) => c.name === titleColumn);
  const checkIdx = checkColumn
    ? columns.findIndex((c) => c.name === checkColumn)
    : -1;
  const togglable = slot.toggleAction != null && checkIdx >= 0 && rowIdColumn >= 0;

  const quickAdd = slot.addAction ? quickAddParams(promptParamsOf(slot.addAction)) : null;

  const queryClient = useQueryClient();
  // Rows whose toggle run is in flight, with the check state each shows meanwhile. The cache is
  // patched as well, so the flipped state outlives the run until the refetch lands; this local
  // entry keeps it through a refetch that another row's toggle triggered in between.
  const [pendingToggles, setPendingToggles] = useState<Map<number, boolean>>(new Map());

  const [reordering, setReordering] = useState(false);
  // The order committed by the last drop, applied over the query rows until the mode closes — the
  // refetched rows land in this same order, so the drop never snaps back while it persists.
  const [localOrder, setLocalOrder] = useState<number[] | null>(null);

  // A small distance before a drag starts keeps a tap on the handle from being read as one.
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  const orderedRows = useMemo(() => {
    if (localOrder == null || rowIdColumn < 0) return rows;
    const byId = new Map<number, Value[]>();
    for (const row of rows) {
      const id = row[rowIdColumn]?.value;
      if (typeof id === "number") byId.set(id, row);
    }
    const sorted: Value[][] = [];
    for (const id of localOrder) {
      const row = byId.get(id);
      if (row) {
        sorted.push(row);
        byId.delete(id);
      }
    }
    // Rows the drop didn't know about (e.g. added meanwhile) keep their query order at the end.
    for (const row of rows) {
      const id = row[rowIdColumn]?.value;
      if (typeof id !== "number" || byId.has(id)) {
        sorted.push(row);
      }
    }
    return sorted;
  }, [rows, localOrder, rowIdColumn]);

  const sortableIds = useMemo(
    () =>
      rowIdColumn >= 0
        ? orderedRows
            .map((r) => r[rowIdColumn]?.value)
            .filter((v): v is number => typeof v === "number")
        : [],
    [orderedRows, rowIdColumn],
  );

  const canReorder =
    slot.reorderAction != null &&
    rowIdColumn >= 0 &&
    !reorderDisabled &&
    (sortableIds.length > 1 || reordering);

  function handleDragEnd(event: DragEndEvent) {
    const { active, over } = event;
    if (!over || active.id === over.id) return;
    const from = sortableIds.indexOf(Number(active.id));
    const to = sortableIds.indexOf(Number(over.id));
    if (from === -1 || to === -1) return;

    const next = arrayMove(sortableIds, from, to);
    setLocalOrder(next);
    slot.run.mutate({
      ref: slot.reorderAction!.id,
      data: {
        params: { ...slot.selectionFor(slot.reorderAction!, null), ordered_rowids: next },
      },
    });
  }

  /** Records the check state a row shows while its toggle run is out; null clears the entry. */
  function setPendingToggle(rowId: number, checked: boolean | null) {
    setPendingToggles((prev) => {
      const next = new Map(prev);
      if (checked === null) {
        next.delete(rowId);
      } else {
        next.set(rowId, checked);
      }
      return next;
    });
  }

  /** Flips the row from its current state at once — locally and in the cached rows — then runs the
   *  toggle action; a failed run refetches so the server's rows win. */
  function runToggle(rowId: number, checked: boolean) {
    setPendingToggle(rowId, !checked);
    patchCheckCell(queryClient, bdbId, formula.id, rowId, rowIdColumn, checkIdx, !checked);
    slot.run.mutate(
      { ref: slot.toggleAction!.id, data: { params: slot.selectionFor(slot.toggleAction!, rowId) } },
      {
        // A failed run leaves the guessed cell in the cache; refetching restores the server's rows.
        onError: () => {
          void queryClient.invalidateQueries({ queryKey: ["formula-run", bdbId, formula.id] });
          void queryClient.invalidateQueries({ queryKey: ["formula-detail", bdbId, formula.id] });
        },
        onSettled: () => setPendingToggle(rowId, null),
      },
    );
  }

  return (
    <div className={styles.checklist}>
      {slot.errorMessage && !slot.dialogOpen && (
        <div className={styles.error}>{slot.errorMessage}</div>
      )}

      {(canReorder || (!reordering && slot.addAction && !quickAdd)) && (
        <div className={styles.topBar}>
          {!reordering && slot.addAction && !quickAdd && (
            <Button size="sm" variant="secondary" onClick={() => slot.openDialog(slot.addAction!, null)}>
              + Add
            </Button>
          )}
          {canReorder && (
            <button
              type="button"
              className={[styles.reorderToggle, reordering ? styles.reorderToggleActive : ""].join(" ")}
              aria-pressed={reordering}
              aria-label={reordering ? "Done reordering" : "Reorder items"}
              title={reordering ? "Done reordering" : "Reorder items"}
              onClick={() => {
                // Entering starts from the query's order; leaving keeps the last drop applied, so a
                // still-in-flight refetch can't snap the list back.
                if (!reordering) {
                  setLocalOrder(null);
                }
                setReordering(!reordering);
              }}
            >
              <ReorderToggleIcon active={reordering} />
            </button>
          )}
        </div>
      )}

      {!reordering && slot.addAction && quickAdd && (
        <QuickAddForm
          bdbId={bdbId}
          action={slot.addAction}
          params={quickAdd}
          selection={slot.addSelection}
          run={slot.run}
          onOpenDialog={(typed) => slot.openDialog(slot.addAction!, null, typed)}
        />
      )}

      {rows.length === 0 && <EmptyState compact icon={Inbox} title="No items yet." />}

      {reordering ? (
        <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={handleDragEnd}>
          <SortableContext items={sortableIds} strategy={verticalListSortingStrategy}>
            <ul className={styles.rows}>
              {orderedRows.map((row, i) => {
                const rowIdValue = row[rowIdColumn]?.value;
                const rowId = typeof rowIdValue === "number" ? rowIdValue : null;
                const title = String(row[titleIdx]?.value ?? "");
                if (rowId == null) {
                  return (
                    <li key={`row-${i}`} className={styles.row}>
                      <div className={styles.rowMain}>
                        <span className={styles.title}>{title}</span>
                      </div>
                    </li>
                  );
                }
                return <SortableChecklistRow key={rowId} id={rowId} title={title} />;
              })}
            </ul>
          </SortableContext>
        </DndContext>
      ) : (
        <ul className={styles.rows}>
          {orderedRows.map((row, i) => {
            const rowIdValue = rowIdColumn >= 0 ? row[rowIdColumn]?.value : undefined;
            const rowId = typeof rowIdValue === "number" ? rowIdValue : null;
            const title = String(row[titleIdx]?.value ?? "");
            const pendingCheck = rowId != null ? pendingToggles.get(rowId) : undefined;
            const checked = pendingCheck ?? (checkIdx >= 0 && isChecked(row[checkIdx]?.value));
            const menuItems = rowId != null ? slot.menuItemsFor(rowId, row) : [];
            // The primary tap toggles when a toggle is wired; otherwise it opens the detail (a
            // read, so viewers get it too).
            const tapOpensDetail = !togglable && hasDetail && rowId != null;

            return (
              <li key={rowId ?? `row-${i}`} className={styles.row}>
                {togglable && rowId != null ? (
                  <label className={styles.rowMain}>
                    <input
                      type="checkbox"
                      className={styles.checkbox}
                      checked={checked}
                      disabled={pendingCheck !== undefined}
                      onChange={() => runToggle(rowId, checked)}
                    />
                    <span className={[styles.title, checked ? styles.titleChecked : ""].join(" ")}>
                      {title}
                    </span>
                  </label>
                ) : tapOpensDetail ? (
                  <button className={styles.rowMain} onClick={() => onOpenDetail(rowId)}>
                    <span className={[styles.title, checked ? styles.titleChecked : ""].join(" ")}>
                      {title}
                    </span>
                  </button>
                ) : (
                  <div className={styles.rowMain}>
                    <span className={[styles.title, checked ? styles.titleChecked : ""].join(" ")}>
                      {title}
                    </span>
                  </div>
                )}
                {menuItems.length > 0 && (
                  <Menu items={menuItems} aria-label={`Actions for ${title}`} />
                )}
              </li>
            );
          })}
        </ul>
      )}

      {slot.dialogNode}
    </div>
  );
}
