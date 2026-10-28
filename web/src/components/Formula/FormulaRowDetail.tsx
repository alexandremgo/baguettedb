/**
 * Shows a clicked row's detail in a modal: runs the parent view's row-detail formula with the
 * row's rowid and renders the result. A detail view carrying its own display config renders
 * interactively (a checklist or card grid of the row's children, with the parent row bound as
 * :parent_rowid to slotted actions declaring it); otherwise a single row renders as a record card
 * (field: value) and several rows as a table. Stays mounted with rowId=null while closed so the
 * run only fires on a click. An interactive detail filling its own detail slot renders this
 * component recursively — each drill level stacks its dialog in the browser top layer.
 */

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type { Formula } from "../../api/formulas";
import { markdownColumns, roleColumn, runFormula } from "../../api/formulas";
import { Markdown } from "../Markdown";
import { formatCellValue, isNullValue, parseStructuredValue } from "../../utils/cellValue";
import { Dialog } from "../Dialog";
import { QueryResultTable } from "../QueryResultTable";
import { SkeletonRows } from "../Skeleton";
import { StructuredCell } from "../StructuredCell";
import { useFormulas } from "../../hooks/useFormulas";
import { useLaneLayout } from "../../hooks/useLaneLayout";
import { formulaTitle } from "./selectors";
import { FormulaBoardView } from "./FormulaBoardView";
import { FormulaCalendarView } from "./FormulaCalendarView";
import { FormulaCardsView } from "./FormulaCardsView";
import { FormulaChecklistView } from "./FormulaChecklistView";
import { windowRunParams } from "./params";
import { initialWindowOf, type DayWindow } from "../../utils/calendar";
import styles from "./FormulaRowDetail.module.css";

interface FormulaRowDetailProps {
  bdbId: string;
  formula: Formula;
  rowId: number | null;
  onClose: () => void;
  /** Whether the member can run the detail's slotted mutations; null while the bdb loads. */
  canWrite: boolean | null;
}

export function FormulaRowDetail({ bdbId, formula, rowId, onClose, canWrite }: FormulaRowDetailProps) {
  // A calendar detail's visible days, bound to its window pair like a top-level calendar's.
  const [calendarWindow, setCalendarWindow] = useState<DayWindow>(() =>
    initialWindowOf(formula.display?.window, new Date()),
  );
  const [laneLayout] = useLaneLayout(formula.id);
  const query = useQuery({
    queryKey: ["formula-detail", bdbId, formula.id, rowId, calendarWindow],
    queryFn: () =>
      runFormula(bdbId, formula.id, {
        params: { rowid: rowId, ...windowRunParams(formula, calendarWindow) },
      }),
    enabled: rowId !== null,
  });
  const { data: allFormulas = [] } = useFormulas(bdbId);

  const result = query.data;
  const errorMessage = query.error instanceof Error ? query.error.message : null;

  // An interactive detail activates like a top-level styled mode: config present and its columns
  // still in the result; a drifted config degrades to the static rendering.
  const display = formula.display;
  const styledMode =
    display?.mode === "checklist" ||
    display?.mode === "cards" ||
    display?.mode === "board" ||
    display?.mode === "calendar"
      ? display.mode
      : null;
  const columns = result?.columns ?? [];
  const modeColumnsOk =
    styledMode != null &&
    columns.some((c) => c.name === roleColumn(display, "title")) &&
    (styledMode === "checklist"
      ? !roleColumn(display, "check") || columns.some((c) => c.name === roleColumn(display, "check"))
      : !roleColumn(display, "body") ||
        columns.some((c) => c.name === roleColumn(display, "body"))) &&
    (styledMode !== "board" || columns.some((c) => c.name === roleColumn(display, "group"))) &&
    (styledMode !== "calendar" || columns.some((c) => c.name === roleColumn(display, "day")));
  const activeMode = result && modeColumnsOk ? styledMode : null;
  const attached = allFormulas.filter((f) => f.attached_to_formula_id === formula.id);
  const rowIdColumn = columns.findIndex((c) => c.name === "rowid");
  const markdown = markdownColumns(formula.display);

  const childRef = display?.slots?.detail;
  const childView = childRef ? (attached.find((f) => f.id === childRef) ?? null) : null;
  const [childRowId, setChildRowId] = useState<number | null>(null);
  // The component stays mounted while closed, so a stale child drill would flash on reopen.
  if (rowId === null && childRowId !== null) setChildRowId(null);

  return (
    <Dialog open={rowId !== null} onClose={onClose} title={formulaTitle(formula)}>
      {query.isLoading && <SkeletonRows count={2} />}
      {errorMessage && <div className={styles.error}>{errorMessage}</div>}

      {result && activeMode === "checklist" && (
        <FormulaChecklistView
          bdbId={bdbId}
          formula={formula}
          columns={columns}
          rows={result.rows}
          rowIdColumn={rowIdColumn}
          attached={attached}
          canWrite={canWrite === true}
          hasDetail={childView != null}
          onOpenDetail={setChildRowId}
          parentRowId={rowId}
        />
      )}
      {result && activeMode === "cards" && (
        <FormulaCardsView
          bdbId={bdbId}
          formula={formula}
          columns={columns}
          rows={result.rows}
          rowIdColumn={rowIdColumn}
          attached={attached}
          canWrite={canWrite === true}
          hasDetail={childView != null}
          onOpenDetail={setChildRowId}
          parentRowId={rowId}
        />
      )}
      {result && activeMode === "board" && (
        <FormulaBoardView
          bdbId={bdbId}
          formula={formula}
          columns={columns}
          rows={result.rows}
          rowIdColumn={rowIdColumn}
          attached={attached}
          canWrite={canWrite === true}
          hasDetail={childView != null}
          onOpenDetail={setChildRowId}
          parentRowId={rowId}
          layout={laneLayout}
        />
      )}
      {result && activeMode === "calendar" && (
        <FormulaCalendarView
          bdbId={bdbId}
          formula={formula}
          columns={columns}
          rows={result.rows}
          rowIdColumn={rowIdColumn}
          attached={attached}
          canWrite={canWrite === true}
          hasDetail={childView != null}
          onOpenDetail={setChildRowId}
          parentRowId={rowId}
          window={calendarWindow}
          onWindowChange={setCalendarWindow}
          layout={laneLayout}
        />
      )}

      {result && activeMode == null && result.rows.length === 0 && (
        <p className={styles.status}>No detail for this row.</p>
      )}
      {result && activeMode == null && result.rows.length === 1 && (
        <dl className={styles.record}>
          {result.columns.map((col, colIndex) => {
            const cell = result.rows[0][colIndex];
            const isNull = !cell || isNullValue(cell.value);
            const asMarkdown = !isNull && markdown.has(col.name);
            const structured = isNull || asMarkdown ? null : parseStructuredValue(cell.value);
            return (
              <div key={`${col.name}-${colIndex}`} className={styles.field}>
                <dt className={styles.label}>{col.name}</dt>
                <dd className={isNull ? styles.null : styles.value}>
                  {isNull ? (
                    "NULL"
                  ) : asMarkdown ? (
                    <Markdown text={String(cell.value ?? "")} />
                  ) : structured ? (
                    <StructuredCell value={structured} wrap />
                  ) : (
                    formatCellValue(cell.value)
                  )}
                </dd>
              </div>
            );
          })}
        </dl>
      )}
      {result && activeMode == null && result.rows.length > 1 && (
        <QueryResultTable result={result} emptyMessage="No rows." hideRowIdColumn />
      )}

      {childView && (
        <FormulaRowDetail
          bdbId={bdbId}
          formula={childView}
          rowId={childRowId}
          onClose={() => setChildRowId(null)}
          canWrite={canWrite}
        />
      )}
    </Dialog>
  );
}
