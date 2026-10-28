/**
 * Renders a view's rows on a span of days: one lane per day, then an "Unscheduled" lane for rows
 * whose day-role column is NULL. The span is the display config's window — a weekly one (Monday to
 * Sunday unless it names other weekdays) or a fixed range with its own start and length. The window
 * the app shows is the caller's state — it binds the view's :window_start / :window_end on every
 * run — and the header moves it a step at a time (a week, or the range's length), jumps to today, or
 * to the span holding any day picked in a date picker. A drop onto a day binds that day to the
 * move slot's date target (cards leave the unscheduled lane but never land in it: a drop knows a
 * day, and "no day" is a row action's job); a drag within a day runs the reorder slot. When the add
 * slot's action declares a date parameter, each day header carries a "+" that seeds it with that
 * day. Rows keep the order the view returned them in within a day. The drag machinery, card chrome,
 * and add bar are the shared lane board's, which also stacks the days into an agenda on small
 * screens.
 */

import { useCallback, useMemo } from "react";
import type { Column, Value } from "../../api/databases";
import type { Formula } from "../../api/formulas";
import { roleColumn } from "../../api/formulas";
import { ChevronLeft, ChevronRight } from "../icons";
import {
  dayLabel,
  daysOf,
  dayOf,
  fromISODay,
  isMondayWeek,
  isoWeek,
  shiftWindow,
  stepDays,
  toISODay,
  windowLabel,
  windowOf,
  type DayWindow,
} from "../../utils/calendar";
import type { LaneLayout } from "../../hooks/useLaneLayout";
import { LaneBoard, type Lane } from "./LaneBoard";
import styles from "./FormulaCalendarView.module.css";

/** The lane key of rows with no day; also the placeholder the tray's lane uses. */
const UNSCHEDULED = "";

interface FormulaCalendarViewProps {
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
  /** The parent row when this calendar renders inside a row-detail modal. */
  parentRowId?: number | null;
  /** Blocks order persistence while the caller knows the rows are a subset (a filter) — renumbering
   *  what's visible would scramble it against the rest. Moving stays allowed. */
  reorderDisabled?: boolean;
  /** The days on show; the caller binds it to the view's window params on every run. */
  window: DayWindow;
  onWindowChange: (next: DayWindow) => void;
  /** The wide-screen layout, side by side or stacked; small screens always stack. */
  layout?: LaneLayout;
}

/** The add action's date parameter a day's "+" fills, if it declares one. */
function addDateParam(action: Formula | undefined) {
  return action?.params.find((p) => p.source === "prompt" && p.type === "date");
}

export function FormulaCalendarView(props: FormulaCalendarViewProps) {
  const { formula, columns, attached, canWrite, window, onWindowChange } = props;
  const dayColumn = roleColumn(formula.display, "day");
  const dayIdx = dayColumn ? columns.findIndex((c) => c.name === dayColumn) : -1;
  const today = toISODay(new Date());
  const windowConfig = formula.display?.window;
  const isCurrentWindow = window.start === windowOf(windowConfig, new Date()).start;

  // The move target resolves un-gated here so every member sees the same lanes; dragging itself
  // is write-gated inside the lane board.
  const moveRef = formula.display?.slots?.move;
  const moveFormula = moveRef ? (attached.find((f) => f.id === moveRef) ?? null) : null;
  const moveTarget =
    moveFormula?.params.find((p) => p.source === "prompt" && !p.optional && p.type === "date") ?? null;
  const addRef = formula.display?.slots?.add;
  const addAction = canWrite && addRef ? attached.find((f) => f.id === addRef) : undefined;
  const dateParam = addDateParam(addAction);

  // The days lead so the strip reads as a calendar, the tray closes it; a row whose day falls
  // outside the window lands nowhere — the server filtered on this window, so such a row is a
  // drifted view, not a lane to invent.
  const lanes = useMemo<Lane[]>(() => {
    const days = daysOf(window).map<Lane>((day) => ({
      key: day,
      label: dayLabel(day),
      droppable: true,
      highlighted: day === today,
      addSeed: dateParam ? { [dateParam.name]: day } : undefined,
    }));
    return [...days, { key: UNSCHEDULED, label: "Unscheduled", droppable: false }];
  }, [window, today, dateParam]);

  const laneKeyOf = useCallback(
    (row: Value[]) => (dayIdx >= 0 ? (dayOf(row[dayIdx]?.value) ?? UNSCHEDULED) : UNSCHEDULED),
    [dayIdx],
  );
  const moveParamsFor = useMemo(
    () => (moveTarget ? (laneKey: string) => ({ [moveTarget.name]: laneKey }) : null),
    [moveTarget],
  );

  const start = fromISODay(window.start);
  // An ISO week number only names a Monday-to-Sunday span; any other window shows its dates alone.
  const weekLabel = isMondayWeek(windowConfig) && start ? isoWeek(start) : null;
  const step = stepDays(windowConfig);
  const stepLabel = step === 7 ? "week" : step === 1 ? "day" : `${step} days`;

  const toolbar = (
    <div className={styles.header}>
      <div className={styles.nav}>
        <button
          type="button"
          className={styles.navButton}
          aria-label={`Previous ${stepLabel}`}
          title={`Previous ${stepLabel}`}
          onClick={() => onWindowChange(shiftWindow(window, windowConfig, -1))}
        >
          <ChevronLeft size={16} aria-hidden="true" />
        </button>
        <button
          type="button"
          className={styles.navButton}
          aria-label={`Next ${stepLabel}`}
          title={`Next ${stepLabel}`}
          onClick={() => onWindowChange(shiftWindow(window, windowConfig, 1))}
        >
          <ChevronRight size={16} aria-hidden="true" />
        </button>
        <button
          type="button"
          className={styles.todayButton}
          disabled={isCurrentWindow}
          onClick={() => onWindowChange(windowOf(windowConfig, new Date()))}
        >
          Today
        </button>
      </div>
      <div className={styles.title}>
        {weekLabel && (
          <span className={styles.week}>
            Week {weekLabel.week} · {weekLabel.year}
          </span>
        )}
        <span className={styles.range}>{windowLabel(window)}</span>
      </div>
      {/* Jumping to any span of any year: pick a day, the window holding it shows. */}
      <label className={styles.jump}>
        <span className={styles.jumpLabel}>Go to</span>
        <input
          type="date"
          className={styles.jumpInput}
          value={window.start}
          onChange={(e) => {
            const picked = fromISODay(e.target.value);
            if (picked) onWindowChange(windowOf(windowConfig, picked));
          }}
        />
      </label>
    </div>
  );

  return (
    <LaneBoard
      {...props}
      lanes={lanes}
      laneKeyOf={laneKeyOf}
      moveParamsFor={moveParamsFor}
      toolbar={toolbar}
      emptyTitle="Nothing planned yet."
    />
  );
}
