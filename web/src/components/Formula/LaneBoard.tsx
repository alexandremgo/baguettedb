/**
 * Shared rendering for the lane-shaped display modes — a board's columns and a calendar's days:
 * cards laid side by side in lanes, dragged across lanes to run the move slot's action against the
 * dropped row and the destination lane's binding, and within a lane to run the reorder slot's
 * action with that lane's rowids in their new order. The caller decides what a lane is (an option
 * value, a day) and what a drop into it binds; this component owns the drag machinery, the
 * optimistic layout that keeps a dropped card in place while its run settles, the card chrome (title,
 * optional body, tap, `⋯` menu), the add bar above the lanes, and the stacked layout where lanes
 * pile vertically and collapse from their headers — forced on small screens, a per-view choice on
 * wide ones. Cards drag out of a non-droppable lane but never into one. Without move and reorder
 * slots, or for viewers, the lanes are read-only.
 */

import { useMemo, useState, type ReactNode } from "react";
import {
  DndContext,
  MouseSensor,
  TouchSensor,
  closestCorners,
  useDroppable,
  useSensor,
  useSensors,
  type DragEndEvent,
  type DragOverEvent,
  type DragStartEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import type { Column, Value } from "../../api/databases";
import type { Formula } from "../../api/formulas";
import { markdownColumns, roleColumn } from "../../api/formulas";
import { Markdown } from "../Markdown";
import { Button } from "../Button";
import { EmptyState } from "../EmptyState";
import { ChevronDown, ChevronRight, Inbox, Plus } from "../icons";
import { Menu } from "../Menu";
import { useMediaQuery } from "../../hooks/useMediaQuery";
import type { LaneLayout } from "../../hooks/useLaneLayout";
import { promptParamsOf } from "./actions";
import { quickAddParams } from "./params";
import { QuickAddForm } from "./QuickAddForm";
import { useSlotInteractions } from "./slotInteractions";
import styles from "./LaneBoard.module.css";

/** One lane of the board, as the caller defines it. */
export interface Lane {
  /** The value rows are grouped under and drops are keyed by; unique across lanes. */
  key: string;
  label: string;
  /** Accepts drops when the move slot is live; a lane cards may only leave otherwise. */
  droppable: boolean;
  /** Marks the lane visually (a calendar's today). */
  highlighted?: boolean;
  /** Field values a lane's own "+" seeds the add dialog with; absent, the lane has no "+". */
  addSeed?: Record<string, unknown>;
}

export interface LaneBoardProps {
  bdbId: string;
  /** The view being rendered; its display config names the columns and slots. */
  formula: Formula;
  columns: Column[];
  rows: Value[][];
  rowIdColumn: number;
  /** Every formula attached to this view, for slot resolution and the card menu. */
  attached: Formula[];
  canWrite: boolean;
  hasDetail: boolean;
  onOpenDetail: (rowId: number) => void;
  /** The parent row when this board renders inside a row-detail modal. */
  parentRowId?: number | null;
  /** Blocks order persistence while the caller knows the rows are a subset (a filter, an unloaded
   *  page) — renumbering what's visible would scramble it against the rest. Moving stays allowed;
   *  its run just omits the placement binding. */
  reorderDisabled?: boolean;
  /** The lanes in display order. */
  lanes: Lane[];
  /** The lane key a row belongs to; a key naming no lane leaves the row unrendered. */
  laneKeyOf: (row: Value[]) => string;
  /** The move action's binding for a drop into the given lane, or null when the move slot's
   *  target could not be resolved (drags then only reorder). */
  moveParamsFor: ((laneKey: string) => Record<string, unknown>) | null;
  /** Rendered between the add bar and the lanes — a calendar's navigation header. */
  toolbar?: ReactNode;
  /** The empty state's title when there is nothing to show. */
  emptyTitle: string;
  /** The wide-screen layout: lanes side by side (the default) or stacked; below the mobile breakpoint
   *  the lanes stack regardless. */
  layout?: LaneLayout;
}

/** The viewport below which the lanes always stack. */
export const STACKED_BREAKPOINT_QUERY = "(max-width: 768px)";

/** A lane layout: each lane's rowids in display order, keyed by lane. */
type Layout = Map<string, number[]>;

/** Returns the lane whose list currently holds the given rowid. */
function containerOf(layout: Layout, id: number): string | undefined {
  for (const [lane, ids] of layout) {
    if (ids.includes(id)) return lane;
  }
  return undefined;
}

/** One card: sortable while a drag could mean something, otherwise plain; the inner tap and menu
 *  stay live either way, with a small pointer distance separating a tap from a drag start. */
function LaneCard({
  rowId,
  draggable,
  children,
}: {
  rowId: number;
  draggable: boolean;
  children: ReactNode;
}) {
  const {
    attributes,
    listeners,
    setNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({
    id: rowId,
    disabled: !draggable,
  });
  return (
    <div
      ref={setNodeRef}
      className={[styles.card, isDragging ? styles.cardDragging : ""].join(" ")}
      style={{ transform: CSS.Translate.toString(transform), transition }}
      {...attributes}
      {...listeners}
    >
      {children}
    </div>
  );
}

/** One lane: a drop target while droppable, plain otherwise. In the stacked layout the header
 *  becomes a collapse toggle; a collapsed lane hides its cards but stays a drop target,
 *  so a drag can land on it without scrolling. */
function LaneColumn({
  lane,
  count,
  droppable,
  collapsible,
  collapsed,
  onToggleCollapse,
  onAdd,
  children,
}: {
  lane: Lane;
  count: number;
  droppable: boolean;
  collapsible: boolean;
  collapsed: boolean;
  onToggleCollapse: () => void;
  onAdd?: () => void;
  children: ReactNode;
}) {
  const { setNodeRef, isOver } = useDroppable({
    id: `lane:${lane.key}`,
    disabled: !droppable,
  });
  const header = (
    <>
      <span className={styles.laneTitle}>{lane.label}</span>
      <span className={styles.laneCount}>{count}</span>
    </>
  );
  return (
    <div
      ref={setNodeRef}
      className={[
        styles.lane,
        isOver ? styles.laneOver : "",
        collapsed ? styles.laneCollapsed : "",
        lane.highlighted ? styles.laneHighlighted : "",
      ].join(" ")}
    >
      <div className={styles.laneHeaderRow}>
        {collapsible ? (
          <button
            type="button"
            className={[styles.laneHeader, styles.laneToggle].join(" ")}
            aria-expanded={!collapsed}
            onClick={onToggleCollapse}
          >
            <span className={styles.laneChevron} aria-hidden>
              {collapsed ? (
                <ChevronRight size={14} />
              ) : (
                <ChevronDown size={14} />
              )}
            </span>
            {header}
          </button>
        ) : (
          <div className={styles.laneHeader}>{header}</div>
        )}
        {onAdd && (
          <button
            type="button"
            className={styles.laneAdd}
            aria-label={`Add to ${lane.label}`}
            title={`Add to ${lane.label}`}
            onClick={onAdd}
          >
            <Plus size={14} aria-hidden="true" />
          </button>
        )}
      </div>
      <div className={styles.laneCards}>{children}</div>
    </div>
  );
}

export function LaneBoard({
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
  lanes,
  laneKeyOf,
  moveParamsFor,
  toolbar,
  layout,
  emptyTitle,
}: LaneBoardProps) {
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
  const bodyColumn = roleColumn(display, "body");
  const markdown = markdownColumns(display);
  const titleIdx = columns.findIndex((c) => c.name === titleColumn);
  const bodyIdx = bodyColumn
    ? columns.findIndex((c) => c.name === bodyColumn)
    : -1;
  const bodyMarkdown = bodyColumn != null && markdown.has(bodyColumn);

  const moveTakesOrder =
    slot.moveAction?.params.some((p) => p.name === "ordered_rowids") ?? false;
  const canMove =
    canWrite &&
    slot.moveAction != null &&
    moveParamsFor != null &&
    rowIdColumn >= 0;
  const canReorder =
    canWrite &&
    slot.reorderAction != null &&
    !reorderDisabled &&
    rowIdColumn >= 0;

  const rowById = useMemo(() => {
    const map = new Map<number, Value[]>();
    if (rowIdColumn < 0) return map;
    for (const row of rows) {
      const id = row[rowIdColumn]?.value;
      if (typeof id === "number") map.set(id, row);
    }
    return map;
  }, [rows, rowIdColumn]);

  // The server's layout: each row in its raw lane, in query order.
  const baseLayout = useMemo(() => {
    const layout: Layout = new Map(lanes.map((l) => [l.key, []]));
    if (rowIdColumn >= 0) {
      for (const row of rows) {
        const id = row[rowIdColumn]?.value;
        if (typeof id !== "number") continue;
        layout.get(laneKeyOf(row))?.push(id);
      }
    }
    return layout;
  }, [lanes, rows, rowIdColumn, laneKeyOf]);

  // The layout committed by drops, applied over the query rows until they catch up — the refetch
  // lands in this same arrangement, so a drop never snaps back while its run settles. A failed run
  // restores the snapshot its drop replaced.
  const [localLayout, setLocalLayout] = useState<Layout | null>(null);
  // The in-flight arrangement while a card is being dragged (live cross-lane preview).
  const [dragLayout, setDragLayout] = useState<Layout | null>(null);
  const [dragOrigin, setDragOrigin] = useState<{
    id: number;
    lane: string;
    laneIds: number[];
  } | null>(null);

  // Effective layout: the drag preview wins, then the committed local arrangement reconciled with
  // the current rows (vanished ids dropped, new ids appended to their raw lane), then the base.
  const effectiveLayout = useMemo(() => {
    if (dragLayout) return dragLayout;
    if (!localLayout) return baseLayout;
    const layout: Layout = new Map();
    const placed = new Set<number>();
    for (const [lane] of baseLayout) {
      const kept = (localLayout.get(lane) ?? []).filter((id) =>
        rowById.has(id),
      );
      kept.forEach((id) => placed.add(id));
      layout.set(lane, kept);
    }
    for (const [lane, ids] of baseLayout) {
      for (const id of ids) {
        if (!placed.has(id)) layout.get(lane)?.push(id);
      }
    }
    return layout;
  }, [dragLayout, localLayout, baseLayout, rowById]);

  // A mouse drags after a small distance (a click stays a click); a finger lifts a card by
  // pressing and holding — a short swipe within the delay stays a scroll, which whole-card
  // dragging can't get any other way (touch-action: none on the cards would kill scrolling).
  // Below the app's mobile breakpoint the lanes stack vertically and collapse individually —
  // horizontal scrolling on a phone hides the board's shape, vertical scanning doesn't; a wide screen
  // stacks on the caller's say-so.
  const stacked =
    useMediaQuery(STACKED_BREAKPOINT_QUERY) || layout === "stacked";
  const [collapsedLanes, setCollapsedLanes] = useState<Set<string>>(new Set());

  const sensors = useSensors(
    useSensor(MouseSensor, { activationConstraint: { distance: 6 } }),
    useSensor(TouchSensor, {
      activationConstraint: { delay: 250, tolerance: 5 },
    }),
  );

  const laneByKey = useMemo(
    () => new Map(lanes.map((l) => [l.key, l])),
    [lanes],
  );

  /** Resolves a droppable/sortable id to the lane it stands for in the given layout. */
  function laneFor(
    layout: Layout,
    overId: string | number,
  ): string | undefined {
    if (typeof overId === "string" && overId.startsWith("lane:"))
      return overId.slice("lane:".length);
    return containerOf(layout, Number(overId));
  }

  function handleDragStart(event: DragStartEvent) {
    const id = Number(event.active.id);
    const lane = containerOf(effectiveLayout, id);
    if (lane == null) return;
    setDragOrigin({
      id,
      lane,
      laneIds: [...(effectiveLayout.get(lane) ?? [])],
    });
    // A fresh copy: the preview mutates per dragOver without touching the committed state.
    setDragLayout(
      new Map([...effectiveLayout].map(([lane, ids]) => [lane, [...ids]])),
    );
  }

  function handleDragOver(event: DragOverEvent) {
    const { active, over } = event;
    if (!over || !dragLayout) return;
    const id = Number(active.id);
    const from = containerOf(dragLayout, id);
    const to = laneFor(dragLayout, over.id);
    if (from == null || to == null || from === to) return;
    // The preview only crosses into real drop targets: a droppable lane (when moving is possible)
    // or back into the origin lane.
    const target = laneByKey.get(to);
    if (to !== dragOrigin?.lane && (!canMove || !target?.droppable)) return;
    if (stacked && collapsedLanes.has(to)) return;
    setDragLayout((prev) => {
      if (!prev) return prev;
      const next: Layout = new Map(
        [...prev].map(([lane, ids]) => [lane, [...ids]]),
      );
      const fromIds = next.get(from)!;
      fromIds.splice(fromIds.indexOf(id), 1);
      const toIds = next.get(to)!;
      const overIndex =
        typeof over.id === "number" ? toIds.indexOf(over.id) : toIds.length;
      toIds.splice(overIndex < 0 ? toIds.length : overIndex, 0, id);
      return next;
    });
  }

  function handleDragEnd(event: DragEndEvent) {
    const { active, over } = event;
    const origin = dragOrigin;
    const layout = dragLayout;
    setDragOrigin(null);
    setDragLayout(null);
    if (!over || !origin || !layout) return;
    const id = Number(active.id);
    const current = containerOf(layout, id);
    if (current == null) return;
    const overLane = laneFor(layout, over.id);
    const final: Layout = new Map(
      [...layout].map(([lane, ids]) => [lane, [...ids]]),
    );
    let destination = current;
    if (overLane != null && overLane !== current) {
      // The live preview skips collapsed targets, so this transfer lands only now, at the drop.
      const target = laneByKey.get(overLane);
      if (!canMove || !target?.droppable) return;
      const fromIds = final.get(current)!;
      fromIds.splice(fromIds.indexOf(id), 1);
      const toIds = final.get(overLane)!;
      const overIndex =
        typeof over.id === "number" ? toIds.indexOf(over.id) : toIds.length;
      toIds.splice(overIndex < 0 ? toIds.length : overIndex, 0, id);
      destination = overLane;
    } else if (typeof over.id === "number" && over.id !== id) {
      // A same-lane drop still needs its final index applied — dragOver only handles crossings.
      const ids = final.get(destination)!;
      const oldIndex = ids.indexOf(id);
      const newIndex = ids.indexOf(over.id);
      if (oldIndex >= 0 && newIndex >= 0)
        final.set(destination, arrayMove(ids, oldIndex, newIndex));
    }

    const previous = localLayout;
    const revert = () => setLocalLayout(previous);

    if (destination === origin.lane) {
      // Order within the origin lane changed (or nothing did).
      const after = final.get(destination) ?? [];
      if (!canReorder || origin.laneIds.join(",") === after.join(",")) return;
      setLocalLayout(final);
      slot.run.mutate(
        {
          ref: slot.reorderAction!.id,
          data: {
            params: {
              ...slot.selectionFor(slot.reorderAction!, null),
              ordered_rowids: after,
            },
          },
        },
        { onError: revert },
      );
      return;
    }

    // The card crossed into another lane: one move run re-homes it, carrying the destination's
    // post-drop order when the action asks for it and the rows are complete.
    if (!canMove) return;
    const params: Record<string, unknown> = {
      ...slot.selectionFor(slot.moveAction!, id),
      ...moveParamsFor!(destination),
    };
    if (moveTakesOrder && !reorderDisabled) {
      params.ordered_rowids = final.get(destination) ?? [];
    }
    setLocalLayout(final);
    slot.run.mutate(
      { ref: slot.moveAction!.id, data: { params } },
      { onError: revert },
    );
  }

  function rowValues(row: Value[]): Record<string, unknown> {
    return Object.fromEntries(
      columns.map((c, i) => [c.name, row[i]?.value ?? null]),
    );
  }

  const quickAdd = slot.addAction
    ? quickAddParams(promptParamsOf(slot.addAction))
    : null;
  const draggable = canMove || canReorder;

  function renderCard(rowId: number) {
    const row = rowById.get(rowId);
    if (!row) return null;
    const title = String(row[titleIdx]?.value ?? "");
    const body = bodyIdx >= 0 ? String(row[bodyIdx]?.value ?? "") : "";
    const menuItems = slot.menuItemsFor(rowId, row);
    // The card tap opens the detail when one is slotted (a read, so viewers get it too), else the
    // seeded edit dialog, else the card is inert.
    const onTap = hasDetail
      ? () => onOpenDetail(rowId)
      : slot.editAction
        ? () => slot.openDialog(slot.editAction!, rowId, rowValues(row))
        : undefined;

    const text = (
      <>
        <span className={styles.cardTitle}>{title}</span>
        {body &&
          (bodyMarkdown ? (
            <span className={styles.cardBodyMarkdown}>
              <Markdown text={body} />
            </span>
          ) : (
            <span className={styles.cardBody}>{body}</span>
          ))}
      </>
    );

    return (
      <LaneCard key={rowId} rowId={rowId} draggable={draggable}>
        {onTap ? (
          <button className={styles.cardMain} onClick={onTap}>
            {text}
          </button>
        ) : (
          <div className={styles.cardMain}>{text}</div>
        )}
        {menuItems.length > 0 && (
          <div className={styles.cardMenu}>
            <Menu items={menuItems} aria-label={`Actions for ${title}`} />
          </div>
        )}
      </LaneCard>
    );
  }

  return (
    <div className={styles.wrap}>
      {slot.errorMessage && !slot.dialogOpen && (
        <div className={styles.error}>{slot.errorMessage}</div>
      )}

      {slot.addAction && quickAdd && (
        <QuickAddForm
          bdbId={bdbId}
          action={slot.addAction}
          params={quickAdd}
          selection={slot.addSelection}
          run={slot.run}
          onOpenDialog={(typed) =>
            slot.openDialog(slot.addAction!, null, typed)
          }
        />
      )}
      {slot.addAction && !quickAdd && (
        <div className={styles.addBar}>
          <Button
            size="sm"
            variant="secondary"
            onClick={() => slot.openDialog(slot.addAction!, null)}
          >
            + Add
          </Button>
        </div>
      )}

      {toolbar}

      {rows.length === 0 && lanes.length === 0 && (
        <EmptyState compact icon={Inbox} title={emptyTitle} />
      )}

      <DndContext
        sensors={sensors}
        collisionDetection={closestCorners}
        onDragStart={handleDragStart}
        onDragOver={handleDragOver}
        onDragEnd={handleDragEnd}
        onDragCancel={() => {
          setDragOrigin(null);
          setDragLayout(null);
        }}
      >
        <div
          className={[styles.board, stacked ? styles.boardStacked : ""]
            .filter(Boolean)
            .join(" ")}
        >
          {lanes.map((lane) => {
            const ids = effectiveLayout.get(lane.key) ?? [];
            return (
              <LaneColumn
                key={lane.key}
                lane={lane}
                count={ids.length}
                droppable={canMove && lane.droppable}
                collapsible={stacked}
                collapsed={stacked && collapsedLanes.has(lane.key)}
                onToggleCollapse={() =>
                  setCollapsedLanes((prev) => {
                    const next = new Set(prev);
                    if (next.has(lane.key)) {
                      next.delete(lane.key);
                    } else {
                      next.add(lane.key);
                    }
                    return next;
                  })
                }
                onAdd={
                  slot.addAction && lane.addSeed
                    ? () => slot.openDialog(slot.addAction!, null, lane.addSeed)
                    : undefined
                }
              >
                <SortableContext
                  items={ids}
                  strategy={verticalListSortingStrategy}
                >
                  {ids.map((id) => renderCard(id))}
                </SortableContext>
              </LaneColumn>
            );
          })}
        </div>
      </DndContext>

      {slot.dialogNode}
    </div>
  );
}
