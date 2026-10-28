/**
 * Renders a view's rows as a kanban-style board: one lane per value of the group-role column. The
 * move slot's action defines the lanes — its target parameter's labeled options, in declared
 * order — and a drop into another lane binds that option value to the target. Rows whose group
 * value matches no declared option render in extra lanes appended after the declared ones (cards
 * drag out of them, never into them — the run layer only accepts declared option values). The
 * drag machinery, card chrome, and add bar are the shared lane board's.
 */

import { useCallback, useMemo } from "react";
import type { Column, Value } from "../../api/databases";
import type { Formula } from "../../api/formulas";
import { roleColumn } from "../../api/formulas";
import type { LaneLayout } from "../../hooks/useLaneLayout";
import { LaneBoard, type Lane } from "./LaneBoard";

interface FormulaBoardViewProps {
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
  /** The wide-screen layout, side by side or stacked; small screens always stack. */
  layout?: LaneLayout;
}

export function FormulaBoardView(props: FormulaBoardViewProps) {
  const { formula, columns, rows, attached } = props;
  const groupColumn = roleColumn(formula.display, "group");
  const groupIdx = groupColumn ? columns.findIndex((c) => c.name === groupColumn) : -1;

  // The move action resolves un-gated here: its target parameter's options define the lanes for
  // every member, viewers included — only dragging is write-gated, inside the lane board.
  const moveRef = formula.display?.slots?.move;
  const moveFormula = moveRef ? (attached.find((f) => f.id === moveRef) ?? null) : null;
  const moveTarget =
    moveFormula?.params.find(
      (p) =>
        (p.source === "prompt" || p.source === "lookup") &&
        !p.optional &&
        (p.options?.length ?? 0) > 0,
    ) ?? null;

  // Declared options lead, in their declared order; group values they don't cover append as extra
  // read-only-target lanes in first-appearance order, so no row ever disappears.
  const lanes = useMemo<Lane[]>(() => {
    const declared: Lane[] = (moveTarget?.options ?? []).map((o) => ({
      key: o.value,
      label: o.label || o.value,
      droppable: true,
    }));
    const known = new Set(declared.map((l) => l.key));
    const extras: Lane[] = [];
    if (groupIdx >= 0) {
      for (const row of rows) {
        const raw = String(row[groupIdx]?.value ?? "");
        if (!known.has(raw)) {
          known.add(raw);
          extras.push({ key: raw, label: raw === "" ? "—" : raw, droppable: false });
        }
      }
    }
    return [...declared, ...extras];
  }, [moveTarget, rows, groupIdx]);

  const laneKeyOf = useCallback(
    (row: Value[]) => (groupIdx >= 0 ? String(row[groupIdx]?.value ?? "") : ""),
    [groupIdx],
  );
  const moveParamsFor = useMemo(
    () => (moveTarget ? (laneKey: string) => ({ [moveTarget.name]: laneKey }) : null),
    [moveTarget],
  );

  return (
    <LaneBoard
      {...props}
      lanes={lanes}
      laneKeyOf={laneKeyOf}
      moveParamsFor={moveParamsFor}
      emptyTitle="No cards yet."
    />
  );
}
