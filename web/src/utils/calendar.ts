/**
 * Calendar-day arithmetic for the calendar display: ISO "YYYY-MM-DD" strings in and out, windows
 * shaped by a view's display config (a weekly span or a fixed range), ISO-8601 week numbering.
 * Everything works on local calendar days — a day column holds a date, not an instant, so no time
 * zone conversion belongs here.
 */

import type { FormulaDisplayWindow, Weekday } from "../api/formulas";

/** A visible range, both bounds inclusive, as ISO days. */
export interface DayWindow {
  start: string;
  end: string;
}

/** Each weekday's `Date.getDay()` index, which counts Sunday as 0. */
const WEEKDAY_INDEX: Record<Weekday, number> = {
  sunday: 0,
  monday: 1,
  tuesday: 2,
  wednesday: 3,
  thursday: 4,
  friday: 5,
  saturday: 6,
};

const MS_PER_DAY = 24 * 3600 * 1000;

const DAY_PATTERN = /^\d{4}-\d{2}-\d{2}/;

/** The ISO day a cell value places its row on: the leading "YYYY-MM-DD" of a date or datetime
 *  string, undefined for NULL or anything that isn't one. */
export function dayOf(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  return DAY_PATTERN.test(value) ? value.slice(0, 10) : undefined;
}

/** Formats a local date as an ISO day. */
export function toISODay(date: Date): string {
  const y = date.getFullYear();
  const m = String(date.getMonth() + 1).padStart(2, "0");
  const d = String(date.getDate()).padStart(2, "0");
  return `${y}-${m}-${d}`;
}

/** Parses an ISO day into a local date at midnight; undefined when it isn't one. */
export function fromISODay(day: string): Date | undefined {
  if (!DAY_PATTERN.test(day)) return undefined;
  const [y, m, d] = day.slice(0, 10).split("-").map(Number);
  const date = new Date(y, m - 1, d);
  return Number.isNaN(date.getTime()) ? undefined : date;
}

/** The date `days` days after the given one (negative counts back). */
export function addDays(date: Date, days: number): Date {
  const next = new Date(date);
  next.setDate(next.getDate() + days);
  return next;
}

/** The Monday of the week containing the given date. */
export function startOfWeek(date: Date): Date {
  // getDay() counts Sunday as 0; Monday-first weeks put Sunday at the end.
  const offset = (date.getDay() + 6) % 7;
  return addDays(date, -offset);
}

/** The Monday-to-Sunday window containing the given date. */
export function weekWindowOf(date: Date): DayWindow {
  return windowOf(undefined, date);
}

/** Whether the config describes a fixed range rather than a weekly window. */
export function isFixedRange(config: FormulaDisplayWindow | undefined): boolean {
  return config?.start !== undefined || config?.days !== undefined;
}

/** The number of days the arrows move the window by: a fixed range's own length, a week otherwise. */
export function stepDays(config: FormulaDisplayWindow | undefined): number {
  return isFixedRange(config) ? (config?.days ?? 7) : 7;
}

/** Whether the config is a weekly window starting on Monday — the only shape whose span is an ISO
 *  week, so the only one an ISO week number labels truthfully. */
export function isMondayWeek(config: FormulaDisplayWindow | undefined): boolean {
  return !isFixedRange(config) && (config?.week_start ?? "monday") === "monday";
}

/** The window containing the given date under the config: for a weekly window, the span from the
 *  most recent week_start (Monday absent) through week_end (absent: a full week from week_start),
 *  wrapping across the week boundary; for a fixed range, the range reached from start by whole steps
 *  of its length, so a date before start lands in an earlier range. A fixed range whose start is not
 *  a day falls back to the weekly default. */
export function windowOf(config: FormulaDisplayWindow | undefined, date: Date): DayWindow {
  const anchor = new Date(date.getFullYear(), date.getMonth(), date.getDate());
  const rangeStart = config?.start !== undefined ? fromISODay(config.start) : undefined;
  if (isFixedRange(config) && rangeStart) {
    const length = config?.days ?? 7;
    const elapsed = Math.round((anchor.getTime() - rangeStart.getTime()) / MS_PER_DAY);
    const start = addDays(rangeStart, Math.floor(elapsed / length) * length);
    return { start: toISODay(start), end: toISODay(addDays(start, length - 1)) };
  }
  const first = WEEKDAY_INDEX[config?.week_start ?? "monday"];
  const length = config?.week_end ? ((WEEKDAY_INDEX[config.week_end] - first + 7) % 7) + 1 : 7;
  const start = addDays(anchor, -((anchor.getDay() - first + 7) % 7));
  return { start: toISODay(start), end: toISODay(addDays(start, length - 1)) };
}

/** The window a calendar opens on: a fixed range as authored — its own days, whatever today is — and
 *  otherwise the weekly window around today. */
export function initialWindowOf(config: FormulaDisplayWindow | undefined, today: Date): DayWindow {
  const rangeStart = config?.start !== undefined ? fromISODay(config.start) : undefined;
  if (isFixedRange(config) && rangeStart) {
    const length = config?.days ?? 7;
    return { start: toISODay(rangeStart), end: toISODay(addDays(rangeStart, length - 1)) };
  }
  return windowOf(config, today);
}

/** The window one step before or after the given one, the step being the config's. */
export function shiftWindow(
  window: DayWindow,
  config: FormulaDisplayWindow | undefined,
  direction: 1 | -1,
): DayWindow {
  const start = fromISODay(window.start);
  const end = fromISODay(window.end);
  if (!start || !end) return window;
  const step = direction * stepDays(config);
  return { start: toISODay(addDays(start, step)), end: toISODay(addDays(end, step)) };
}

/** Every ISO day of the window, first to last inclusive. */
export function daysOf(window: DayWindow): string[] {
  const start = fromISODay(window.start);
  const end = fromISODay(window.end);
  if (!start || !end) return [];
  const length = Math.round((end.getTime() - start.getTime()) / MS_PER_DAY) + 1;
  if (length < 1) return [];
  return Array.from({ length }, (_, i) => toISODay(addDays(start, i)));
}

/** The ISO-8601 week number and week-based year of the given date: weeks start on Monday, and
 *  week 1 is the one holding the year's first Thursday. */
export function isoWeek(date: Date): { week: number; year: number } {
  // Shift to the Thursday of the same week: its calendar year is the ISO week-based year.
  const thursday = addDays(startOfWeek(date), 3);
  const year = thursday.getFullYear();
  const firstThursday = addDays(startOfWeek(new Date(year, 0, 4)), 3);
  const week = 1 + Math.round((thursday.getTime() - firstThursday.getTime()) / (7 * 24 * 3600 * 1000));
  return { week, year };
}

/** Short weekday and day-of-month label for a day header ("Mon 7"). */
export function dayLabel(day: string): string {
  const date = fromISODay(day);
  if (!date) return day;
  return `${date.toLocaleDateString(undefined, { weekday: "short" })} ${date.getDate()}`;
}

/** The window's span as a compact label ("7 – 13 Sep 2026", crossing months as "28 Sep – 4 Oct 2026"). */
export function windowLabel(window: DayWindow): string {
  const start = fromISODay(window.start);
  const end = fromISODay(window.end);
  if (!start || !end) return `${window.start} – ${window.end}`;
  const month = (d: Date) => d.toLocaleDateString(undefined, { month: "short" });
  if (start.getMonth() === end.getMonth()) {
    return `${start.getDate()} – ${end.getDate()} ${month(end)} ${end.getFullYear()}`;
  }
  return `${start.getDate()} ${month(start)} – ${end.getDate()} ${month(end)} ${end.getFullYear()}`;
}
