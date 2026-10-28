/**
 * Renders a view's rows as a Keep-style grid of note cards, driven by the view's display config:
 * each card shows the title column and a truncated body preview. Tapping a card opens the slotted
 * row detail when set, else the seeded edit dialog, else the card is inert. A `⋯` menu per card
 * mirrors the checklist's (details, seeded edit, unslotted single-row actions, delete-with-undo),
 * and an add slot renders as a quick-add form above the cards when its action marks params inline,
 * else as an Add button opening the param form. A reorder slot makes the cards draggable in place —
 * a mouse drags after a small distance, a finger by press-and-hold — and a drop runs the slot's
 * action once with every rowid in its new order.
 */

import { useMemo, useState } from "react";
import {
  DndContext,
  MouseSensor,
  TouchSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  rectSortingStrategy,
  useSortable,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import type { Column, Value } from "../../api/databases";
import type { Formula } from "../../api/formulas";
import { markdownColumns, roleColumn } from "../../api/formulas";
import { Markdown } from "../Markdown";
import { Button } from "../Button";
import { EmptyState } from "../EmptyState";
import { Inbox } from "../icons";
import { Menu } from "../Menu";
import { promptParamsOf } from "./actions";
import { quickAddParams } from "./params";
import { QuickAddForm } from "./QuickAddForm";
import { useSlotInteractions } from "./slotInteractions";
import styles from "./FormulaCardsView.module.css";

interface FormulaCardsViewProps {
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
  /** The parent row when this grid renders inside a row-detail modal. */
  parentRowId?: number | null;
  /** Blocks order persistence while the caller knows the rows are a subset (a filter) —
   *  renumbering what's visible would scramble it against the rest. */
  reorderDisabled?: boolean;
}

/** One grid card: sortable while a reorder is possible, otherwise plain; the inner tap and menu
 *  stay live either way, with the drag claimed only by a mouse distance or a touch hold. */
function SortableCard({
  rowId,
  draggable,
  children,
}: {
  rowId: number;
  draggable: boolean;
  children: React.ReactNode;
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

export function FormulaCardsView({
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
}: FormulaCardsViewProps) {
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

  function rowValues(row: Value[]): Record<string, unknown> {
    return Object.fromEntries(
      columns.map((c, i) => [c.name, row[i]?.value ?? null]),
    );
  }

  const quickAdd = slot.addAction
    ? quickAddParams(promptParamsOf(slot.addAction))
    : null;

  const canReorder =
    canWrite &&
    slot.reorderAction != null &&
    !reorderDisabled &&
    rowIdColumn >= 0;
  // The order committed by the last drop, applied over the query rows until they catch up — the
  // refetched rows land in this same order, so a drop never snaps back; a failed run reverts it.
  const [localOrder, setLocalOrder] = useState<number[] | null>(null);

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

  // A mouse drags after a small distance (a click stays a click); a finger lifts a card by
  // pressing and holding, so a short swipe keeps scrolling.
  const sensors = useSensors(
    useSensor(MouseSensor, { activationConstraint: { distance: 6 } }),
    useSensor(TouchSensor, {
      activationConstraint: { delay: 250, tolerance: 5 },
    }),
  );

  function handleDragEnd(event: DragEndEvent) {
    const { active, over } = event;
    if (!over || active.id === over.id) return;
    const from = sortableIds.indexOf(Number(active.id));
    const to = sortableIds.indexOf(Number(over.id));
    if (from === -1 || to === -1) return;

    const previous = localOrder;
    const next = arrayMove(sortableIds, from, to);
    setLocalOrder(next);
    slot.run.mutate(
      {
        ref: slot.reorderAction!.id,
        data: {
          params: {
            ...slot.selectionFor(slot.reorderAction!, null),
            ordered_rowids: next,
          },
        },
      },
      { onError: () => setLocalOrder(previous) },
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

      {rows.length === 0 && <EmptyState compact icon={Inbox} title="No cards yet." />}

      <DndContext
        sensors={sensors}
        collisionDetection={closestCenter}
        onDragEnd={handleDragEnd}
      >
        <SortableContext items={sortableIds} strategy={rectSortingStrategy}>
          <div className={[styles.cards, canReorder ? styles.cardsReorderable : ""].join(" ")}>
            {orderedRows.map((row, i) => {
              const rowIdValue =
                rowIdColumn >= 0 ? row[rowIdColumn]?.value : undefined;
              const rowId = typeof rowIdValue === "number" ? rowIdValue : null;
              const title = String(row[titleIdx]?.value ?? "");
              const body =
                bodyIdx >= 0 ? String(row[bodyIdx]?.value ?? "") : "";
              const menuItems =
                rowId != null ? slot.menuItemsFor(rowId, row) : [];
              // The card tap opens the detail when one is slotted (a read, so viewers get it too),
              // else the seeded edit dialog, else the card is inert.
              const onTap =
                rowId == null
                  ? undefined
                  : hasDetail
                    ? () => onOpenDetail(rowId)
                    : slot.editAction
                      ? () =>
                          slot.openDialog(
                            slot.editAction!,
                            rowId,
                            rowValues(row),
                          )
                      : undefined;

              const content = (
                <>
                  {onTap ? (
                    <button className={styles.cardMain} onClick={onTap}>
                      <span className={styles.cardTitle}>{title}</span>
                      {body &&
                        (bodyMarkdown ? (
                          <span className={styles.cardBodyMarkdown}>
                            <Markdown text={body} />
                          </span>
                        ) : (
                          <span className={styles.cardBody}>{body}</span>
                        ))}
                    </button>
                  ) : (
                    <div className={styles.cardMain}>
                      <span className={styles.cardTitle}>{title}</span>
                      {body &&
                        (bodyMarkdown ? (
                          <span className={styles.cardBodyMarkdown}>
                            <Markdown text={body} />
                          </span>
                        ) : (
                          <span className={styles.cardBody}>{body}</span>
                        ))}
                    </div>
                  )}
                  {menuItems.length > 0 && (
                    <div className={styles.cardMenu}>
                      <Menu
                        items={menuItems}
                        aria-label={`Actions for ${title}`}
                      />
                    </div>
                  )}
                </>
              );

              if (rowId == null) {
                return (
                  <div key={`card-${i}`} className={styles.card}>
                    {content}
                  </div>
                );
              }
              return (
                <SortableCard key={rowId} rowId={rowId} draggable={canReorder}>
                  {content}
                </SortableCard>
              );
            })}
          </div>
        </SortableContext>
      </DndContext>

      {slot.dialogNode}
    </div>
  );
}
