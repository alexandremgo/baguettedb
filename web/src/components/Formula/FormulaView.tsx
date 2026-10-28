/**
 * Runs and renders a single formula. A view auto-runs (when every prompt param is optional, with
 * the params acting as an inline filter over the default run) and renders
 * its rows with keyset "Load more" paging; if the view exposes `rowid` and its display declares a
 * `bulk` list, its rows become selectable and those actions appear as a toolbar; a slotted
 * `detail` view opens from a row click in a modal. An action formula runs from its own runner.
 * Mount this with `key={formula.id}` so switching formulas starts fresh.
 */

import { useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { bdbCanWrite } from "../../api/bdbs";
import { ApiError } from "../../api/client";
import type { Formula } from "../../api/formulas";
import { roleColumn, runFormula } from "../../api/formulas";
import { Badge } from "../Badge";
import { Button } from "../Button";
import { ChevronDown, ChevronRight, Columns3, Info, Rows3 } from "../icons";
import { ExportFormulaDialog } from "../Export";
import { Menu } from "../Menu";
import { Skeleton } from "../Skeleton";
import { QueryResultTable } from "../QueryResultTable";
import { formulaTitle } from "./selectors";
import { FormulaActionRunner } from "./FormulaActionRunner";
import { FormulaBoardView } from "./FormulaBoardView";
import { FormulaCalendarView } from "./FormulaCalendarView";
import { FormulaCardsView } from "./FormulaCardsView";
import { FormulaChecklistView } from "./FormulaChecklistView";
import { FormulaTableView } from "./FormulaTableView";
import { FormulaParamForm } from "./FormulaParamForm";
import { FormulaRowDetail } from "./FormulaRowDetail";
import { FormulaSelectionActions } from "./FormulaSelectionActions";
import { useBdb } from "../../hooks/useBdb";
import { useFormulas } from "../../hooks/useFormulas";
import { useLaneLayout } from "../../hooks/useLaneLayout";
import { useMediaQuery } from "../../hooks/useMediaQuery";
import { STACKED_BREAKPOINT_QUERY } from "./LaneBoard";
import {
  buildExportParams,
  buildRunParams,
  defaultFields,
  defaultedSelects,
  visibleParams,
  windowRunParams,
  type ParamFieldValue,
} from "./params";
import { initialWindowOf, type DayWindow } from "../../utils/calendar";
import styles from "./FormulaView.module.css";

interface FormulaViewProps {
  bdbId: string;
  formula: Formula;
  /** "compact" strips the reading matter for app-like shells: no name/kind header (the shell's own header
   *  carries the name), the description behind an info toggle, Export CSV in an overflow menu, and no row
   *  counter unless more rows are loadable. Defaults to the browser shell's "full" layout. */
  chrome?: "full" | "compact";
}

export function FormulaView({
  bdbId,
  formula,
  chrome = "full",
}: FormulaViewProps) {
  const compact = chrome === "compact";
  const promptParams = formula.params.filter(
    (p) => p.source === "prompt" || p.source === "lookup",
  );
  const isView = formula.kind === "view";
  const autoRun = isView && promptParams.every((p) => p.optional);

  // Only an editor/owner of a live bdb can run actions; a viewer sees views but no action controls.
  // null while the bdb is still loading, so gated UI holds off instead of flashing the viewer state.
  const { data: bdb } = useBdb(bdbId);
  const canWrite = bdb ? bdbCanWrite(bdb) : null;

  // Every attached formula (for display-slot resolution), the declared bulk list (the selection
  // toolbar, in array order), and the slotted row-detail view — an attached formula renders only
  // where the display config names it, in any mode.
  const { data: allFormulas = [] } = useFormulas(bdbId);
  const attached = allFormulas.filter(
    (f) => f.attached_to_formula_id === formula.id,
  );
  const bulkActions = (formula.display?.slots?.bulk ?? [])
    .map((ref) => attached.find((f) => f.id === ref))
    .filter((f): f is Formula => f != null);
  const detailRef = formula.display?.slots?.detail;
  const detailView = detailRef
    ? (attached.find((f) => f.id === detailRef) ?? null)
    : null;

  // An untouched filter panel holds the params' declared defaults — the view's run binds them when
  // the fields are omitted, so showing them keeps the visible filter state truthful.
  const [fields, setFields] = useState<Record<string, ParamFieldValue>>(() =>
    defaultFields(promptParams),
  );
  // The params committed by the last Run (or {} for an auto-running view). null means "not run yet".
  const [committedParams, setCommittedParams] = useState<Record<
    string,
    unknown
  > | null>(autoRun ? {} : null);
  // Bumped on each Run so re-running with identical params still refetches fresh rows.
  const [runNonce, setRunNonce] = useState(0);
  // A calendar's visible days, bound to the view's window pair on every run; it opens on the display
  // config's own range, or on the week around today, and the header moves it.
  const [calendarWindow, setCalendarWindow] = useState<DayWindow>(() =>
    initialWindowOf(formula.display?.window, new Date()),
  );
  const [selected, setSelected] = useState<Set<number>>(new Set());
  // The clicked row's rowid while its detail modal is open; null when closed.
  const [detailRowId, setDetailRowId] = useState<number | null>(null);
  const [exportDialogOpen, setExportDialogOpen] = useState(false);
  // The filter panel starts collapsed so the data leads; collapsing hides the controls, never
  // clears them — typed values and the applied filtering persist until a reload resets the page.
  const [filtersOpen, setFiltersOpen] = useState(false);
  // Compact chrome hides the description until the info toggle opens it.
  const [infoOpen, setInfoOpen] = useState(false);

  // Fields holding a value, counted so the collapsed header says a hidden filter is narrowing the
  // data; conditionally hidden params don't count — their values never reach the run.
  const activeFilterCount = visibleParams(promptParams, fields).filter((p) => {
    const field = fields[p.name];
    if (!field) return false;
    if (p.type === "boolean") return field.raw === "true";
    if (p.type === "group") return (field.rows?.length ?? 0) > 0;
    // A touched multi-select counts even at zero checked — an empty selection narrows to nothing.
    if (p.multiple) return field.values !== undefined;
    return field.raw.trim() !== "";
  }).length;

  const query = useInfiniteQuery({
    queryKey: [
      "formula-run",
      bdbId,
      formula.id,
      committedParams,
      runNonce,
      calendarWindow,
    ],
    queryFn: ({ pageParam }) => {
      const params = {
        ...(committedParams ?? {}),
        ...windowRunParams(formula, calendarWindow),
      };
      if (pageParam != null) {
        params.cursor = pageParam;
      }
      return runFormula(bdbId, formula.id, { params });
    },
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (last) =>
      last.pagination?.has_more
        ? (last.pagination.next_cursor ?? undefined)
        : undefined,
    enabled: isView && committedParams !== null,
    // The global retry would replay a timed-out run, doubling the wait before its message shows, and a result
    // over the row cap fails the same way every time.
    retry: (failureCount, error) =>
      failureCount < 1 &&
      !(error instanceof ApiError && (error.status === 504 || error.status === 413)),
  });

  function handleRun() {
    setCommittedParams(buildRunParams(promptParams, fields));
    setRunNonce((nonce) => nonce + 1);
    setSelected(new Set());
  }

  const pages = query.data?.pages ?? [];
  const columns = pages[0]?.columns ?? [];
  const rows = pages.flatMap((page) => page.rows);
  const errorMessage =
    query.error instanceof Error ? query.error.message : null;

  const rowIdColumn = columns.findIndex((c) => c.name === "rowid");

  // A styled rendering (checklist / cards) activates when the display config's columns are still
  // present in the result; a drifted config (e.g. the SQL lost the title column) degrades to the
  // table with a notice rather than erroring.
  const display = formula.display;
  const styledMode =
    display?.mode === "checklist" ||
    display?.mode === "cards" ||
    display?.mode === "board" ||
    display?.mode === "calendar"
      ? display.mode
      : null;
  const modeColumnsOk =
    styledMode != null &&
    columns.some((c) => c.name === roleColumn(display, "title")) &&
    (styledMode === "checklist"
      ? !roleColumn(display, "check") ||
        columns.some((c) => c.name === roleColumn(display, "check"))
      : !roleColumn(display, "body") ||
        columns.some((c) => c.name === roleColumn(display, "body"))) &&
    (styledMode !== "board" ||
      columns.some((c) => c.name === roleColumn(display, "group"))) &&
    (styledMode !== "calendar" ||
      columns.some((c) => c.name === roleColumn(display, "day")));
  const activeMode = modeColumnsOk ? styledMode : null;
  const modeDegraded =
    styledMode != null && columns.length > 0 && !modeColumnsOk;

  const selectable =
    isView &&
    activeMode == null &&
    rowIdColumn >= 0 &&
    bulkActions.length > 0 &&
    canWrite === true;
  // Row detail is a read: viewers get it too, so no canWrite gate (unlike selection actions).
  const clickable = isView && rowIdColumn >= 0 && detailView != null;
  const rowIds = selectable
    ? rows
        .map((r) => r[rowIdColumn]?.value)
        .filter((v): v is number => typeof v === "number")
    : [];
  // Rows can refresh underneath the selection (e.g. a reconnect refetch) and SQLite reuses rowids,
  // so only ticks whose row is currently rendered count — a stale id must never reach an action run.
  const rowIdSet = new Set(rowIds);
  const activeSelected = new Set(
    [...selected].filter((id) => rowIdSet.has(id)),
  );
  const allSelected =
    rowIds.length > 0 && rowIds.every((id) => activeSelected.has(id));

  function toggleRow(id: number) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  }

  function toggleAll() {
    setSelected((prev) =>
      rowIds.every((id) => prev.has(id)) ? new Set() : new Set(rowIds),
    );
  }

  // Shared by both layouts. A view's description sits behind the info toggle (data leads, prose on
  // demand); an action keeps it inline — the sparse form's main context. The overflow menu carries
  // the row-level commands so buttons never crowd the header.
  const infoToggle =
    isView && formula.description ? (
      <button
        type="button"
        className={[styles.infoToggle, infoOpen ? styles.infoToggleActive : ""]
          .filter(Boolean)
          .join(" ")}
        aria-expanded={infoOpen}
        aria-label="About this formula"
        onClick={() => setInfoOpen((open) => !open)}
      >
        <Info size={16} aria-hidden="true" />
      </button>
    ) : null;
  // Lane-shaped modes can stack their lanes on a wide screen too, a per-view choice this browser
  // remembers, toggled from the header beside the other screen-level controls; below the breakpoint
  // stacking is forced, so the toggle would be a no-op and hides.
  const [laneLayout, setLaneLayout] = useLaneLayout(formula.id);
  const narrow = useMediaQuery(STACKED_BREAKPOINT_QUERY);
  const laneMode = activeMode === "board" || activeMode === "calendar";
  const layoutToggle =
    laneMode && !narrow ? (
      <button
        type="button"
        className={styles.infoToggle}
        aria-pressed={laneLayout === "stacked"}
        aria-label={
          laneLayout === "stacked" ? "Lanes side by side" : "Stack lanes"
        }
        title={laneLayout === "stacked" ? "Lanes side by side" : "Stack lanes"}
        onClick={() =>
          setLaneLayout(laneLayout === "stacked" ? "columns" : "stacked")
        }
      >
        {laneLayout === "stacked" ? (
          <Columns3 size={16} aria-hidden="true" />
        ) : (
          <Rows3 size={16} aria-hidden="true" />
        )}
      </button>
    ) : null;
  const actionsMenu = isView ? (
    <Menu
      aria-label="Formula actions"
      items={[
        { label: "Export CSV", onSelect: () => setExportDialogOpen(true) },
      ]}
    />
  ) : null;

  // Shared by both layouts: the full layout boxes it with the open filter panel, the compact one
  // seats it in the toolbar row so controls share a single line.
  const filtersToggle =
    isView && promptParams.length > 0 ? (
      <button
        type="button"
        className={styles.paramsToggle}
        aria-expanded={filtersOpen}
        onClick={() => setFiltersOpen((open) => !open)}
      >
        <span className={styles.paramsChevron} aria-hidden>
          {filtersOpen ? <ChevronDown size={16} /> : <ChevronRight size={16} />}
        </span>
        Filters
        {activeFilterCount > 0 && (
          <span className={styles.paramsCount}>{activeFilterCount}</span>
        )}
      </button>
    ) : null;

  return (
    <div className={styles.view}>
      {compact ? (
        // The toolbar row is a view concern (filters, export, dense results); an action screen is a
        // sparse form whose description is its main context, so it renders no toolbar at all.
        isView && (
          <div className={styles.compactToolbar}>
            {filtersToggle}
            <span className={styles.compactSpacer} />
            {layoutToggle}
            {infoToggle}
            {actionsMenu}
          </div>
        )
      ) : (
        <div className={styles.header}>
          <h3 className={styles.name}>{formulaTitle(formula)}</h3>
          <span className={styles.kindBadge}>
            <Badge variant={isView ? "default" : "editor"}>
              {formula.kind}
            </Badge>
          </span>
          <span className={styles.compactSpacer} />
          {layoutToggle}
          {infoToggle}
          {actionsMenu}
        </div>
      )}
      {formula.description && (!isView || infoOpen) && (
        <p className={styles.description}>{formula.description}</p>
      )}

      {!isView ? (
        <FormulaActionRunner
          bdbId={bdbId}
          formula={formula}
          canWrite={canWrite}
        />
      ) : (
        <>
          {/* The toggle lives in the compact toolbar row or in a slim row of its own; the boxed
              panel exists only while open, so a collapsed filter never costs an empty box. */}
          {promptParams.length > 0 && !compact && (
            <div className={styles.filtersRow}>{filtersToggle}</div>
          )}
          {promptParams.length > 0 && filtersOpen && (
            <div className={styles.params}>
              <FormulaParamForm
                bdbId={bdbId}
                params={promptParams}
                values={fields}
                seededSelects={defaultedSelects(promptParams)}
                booleanControl="switch"
                onChange={(name, value) =>
                  setFields((prev) => ({ ...prev, [name]: value }))
                }
              />
              <Button
                size="sm"
                className={styles.runButton}
                onClick={handleRun}
                loading={query.isLoading}
              >
                Run
              </Button>
            </div>
          )}

          {errorMessage && <div className={styles.error}>{errorMessage}</div>}

          {query.isLoading && (
            <div className={styles.loadingRows} aria-label="Running…">
              <Skeleton className={styles.loadingRow} />
              <Skeleton className={styles.loadingRow} />
              <Skeleton className={styles.loadingRow} />
            </div>
          )}

          {query.data && (
            <div className={styles.results}>
              {modeDegraded && (
                <div className={styles.notice}>
                  This view's display config no longer matches its columns —
                  showing the table.
                </div>
              )}

              {selectable && (
                <div className={styles.selectionBar}>
                  <label className={styles.selectAll}>
                    <input
                      type="checkbox"
                      checked={allSelected}
                      onChange={toggleAll}
                    />
                    {activeSelected.size > 0
                      ? `${activeSelected.size} selected`
                      : query.hasNextPage
                        ? "Select all loaded"
                        : "Select all"}
                  </label>
                  <FormulaSelectionActions
                    bdbId={bdbId}
                    actions={bulkActions}
                    selectedIds={[...activeSelected]}
                    onRan={() => setSelected(new Set())}
                  />
                </div>
              )}

              {activeMode === "checklist" ? (
                <FormulaChecklistView
                  bdbId={bdbId}
                  formula={formula}
                  columns={columns}
                  rows={rows}
                  rowIdColumn={rowIdColumn}
                  attached={attached}
                  canWrite={canWrite === true}
                  hasDetail={detailView != null}
                  onOpenDetail={setDetailRowId}
                  reorderDisabled={activeFilterCount > 0}
                />
              ) : activeMode === "cards" ? (
                <FormulaCardsView
                  bdbId={bdbId}
                  formula={formula}
                  columns={columns}
                  rows={rows}
                  rowIdColumn={rowIdColumn}
                  attached={attached}
                  canWrite={canWrite === true}
                  hasDetail={detailView != null}
                  onOpenDetail={setDetailRowId}
                  reorderDisabled={activeFilterCount > 0}
                />
              ) : activeMode === "board" ? (
                <FormulaBoardView
                  bdbId={bdbId}
                  formula={formula}
                  columns={columns}
                  rows={rows}
                  rowIdColumn={rowIdColumn}
                  attached={attached}
                  canWrite={canWrite === true}
                  hasDetail={detailView != null}
                  onOpenDetail={setDetailRowId}
                  reorderDisabled={
                    activeFilterCount > 0 || query.hasNextPage === true
                  }
                  layout={laneLayout}
                />
              ) : activeMode === "calendar" ? (
                <FormulaCalendarView
                  bdbId={bdbId}
                  formula={formula}
                  columns={columns}
                  rows={rows}
                  rowIdColumn={rowIdColumn}
                  attached={attached}
                  canWrite={canWrite === true}
                  hasDetail={detailView != null}
                  onOpenDetail={setDetailRowId}
                  reorderDisabled={activeFilterCount > 0}
                  window={calendarWindow}
                  onWindowChange={setCalendarWindow}
                  layout={laneLayout}
                />
              ) : display?.mode === "table" ? (
                <FormulaTableView
                  bdbId={bdbId}
                  formula={formula}
                  columns={columns}
                  rows={rows}
                  rowIdColumn={rowIdColumn}
                  attached={attached}
                  canWrite={canWrite === true}
                  hasDetail={detailView != null}
                  onOpenDetail={setDetailRowId}
                  selection={
                    selectable
                      ? {
                          rowIdColumn,
                          selected: activeSelected,
                          onToggle: toggleRow,
                        }
                      : undefined
                  }
                />
              ) : (
                // A NULL display or a degraded checklist/cards config: the bare grid, with only
                // the detail slot's row click (a drifted config keeps its detail reachable).
                <QueryResultTable
                  result={{ columns, rows, affected_row_count: 0 }}
                  emptyMessage="No rows match."
                  hideRowIdColumn
                  rowClick={
                    clickable
                      ? { rowIdColumn, onOpen: setDetailRowId }
                      : undefined
                  }
                />
              )}

              {detailView && (
                <FormulaRowDetail
                  bdbId={bdbId}
                  formula={detailView}
                  rowId={detailRowId}
                  onClose={() => setDetailRowId(null)}
                  canWrite={canWrite}
                />
              )}

              {/* Compact chrome drops the counter as reading matter; it stays while more rows are
                  loadable, where it gives "Load more" its meaning. */}
              {(!compact || query.hasNextPage) && (
                <div className={styles.resultsFooter}>
                  <span className={styles.rowCount}>
                    {rows.length} row{rows.length === 1 ? "" : "s"}
                    {query.hasNextPage ? " so far" : ""}
                  </span>
                  {query.hasNextPage && (
                    <Button
                      size="sm"
                      variant="secondary"
                      onClick={() => query.fetchNextPage()}
                      loading={query.isFetchingNextPage}
                    >
                      Load more
                    </Button>
                  )}
                </div>
              )}
            </div>
          )}
        </>
      )}

      {isView && (
        <ExportFormulaDialog
          bdbId={bdbId}
          formula={formula}
          params={buildExportParams(promptParams, fields)}
          open={exportDialogOpen}
          onClose={() => setExportDialogOpen(false)}
        />
      )}
    </div>
  );
}
