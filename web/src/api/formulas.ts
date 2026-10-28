/** Formulas API — client for per-bdb named, parameterized SQL artifacts (views + actions). */

import { get, post } from "./client";
import type { QueryResult } from "./databases";

export type FormulaKind = "view" | "action";

/** The statement's head verb, derived server-side. */
export type StatementVerb = "select" | "insert" | "update" | "delete";

export type ParamType =
  | "text"
  | "integer"
  | "float"
  | "boolean"
  | "date"
  | "datetime"
  | "time"
  | "group";

export type ParamSource =
  | "prompt"
  | "selection-single"
  | "selection-bulk"
  | "pagination"
  | "lookup"
  | "reorder"
  | "window";

/** Options source for a lookup param: the label column is shown, the value column is bound. */
export interface LookupConfig {
  table: string;
  label_column: string;
  value_column: string;
  /** Drops the acting row from the picker's options — for lookups reading the same table the
   *  selected row lives in (e.g. picking which duplicate a merge absorbs). Display-only. */
  exclude_selection?: boolean;
  /** Also accepts text typed past the option list — pick to reuse, type to create. The formula's
   *  SQL should resolve or create the typed value; the param is always text. */
  allow_new?: boolean;
  /** Narrows the picker's options by sibling params' current values: sibling param name → column of
   *  the lookup table compared against it; a sibling left empty adds no constraint. Display-only —
   *  the run still binds exactly what the form sends. */
  scope?: Record<string, string>;
  /** Copies columns of the picked row into sibling params: sibling param name → column of the
   *  lookup table whose value the pick writes there. A pick overwrites the sibling; typed text
   *  fills nothing; clearing leaves siblings alone. Display-only — the run binds what the form
   *  sends. */
  fill?: Record<string, string>;
}

/** Visibility condition: the field renders only while the named sibling param's current value
 *  compares true against the given value — op "eq" (the default) or "ne" on any controller,
 *  "lt"/"lte"/"gt"/"gte" on numeric, date, and time ones. The run mirrors the form: while the
 *  condition is false the param is not required, so a hidden field submitting nothing binds its
 *  default, else NULL — required params may gate. */
export interface ParamCondition {
  param: string;
  op?: "eq" | "ne" | "lt" | "lte" | "gt" | "gte";
  value: string;
}

/** One closed-set entry: value is what a run binds (the param type's string form), label what the
 *  form shows in its place ("September" shown, "9" bound). Reads always carry both. */
export interface ParamOption {
  value: string;
  label: string;
}

export interface FormulaParam {
  name: string;
  type: ParamType;
  source: ParamSource;
  /** An optional param may be left empty; the run binds SQL NULL (a group binds zero rows). */
  optional?: boolean;
  /** Set only for source "lookup" params. */
  lookup?: LookupConfig;
  /** Display-only placeholder for the input (e.g. "matches title or tags"). Never affects binding. */
  hint?: string;
  /** Hides the field — and lifts its requiredness — unless the condition holds against the form's
   *  current values. */
  when?: ParamCondition;
  /** Renders a prompt text param as a textarea (note bodies, long descriptions). Display-only. */
  multiline?: boolean;
  /** Places the param in the view's quick-add form when its action fills an add slot. Display-only. */
  inline?: boolean;
  /** Marks a lookup param, or an options param, multi-select: the run value is an array of picked
   *  values (no duplicates). Omitted, it binds its default — `default: "all"` (every value, resolved
   *  server-side) or the `defaults` subset, allowed even on a required param — else '[]' when
   *  optional. */
  multiple?: boolean;
  /** Closed set of accepted values for a typed-in prompt param (text, integer, float, date); the
   *  form renders a select showing each label and submitting its value. */
  options?: ParamOption[];
  /** The value an omitted optional param binds instead of NULL, as its string form; on a multi-select
   *  only the literal "all". The forms prefill from it, so what the user sees is what an omitting
   *  caller gets. */
  default?: string;
  /** A multi-select's declared subset — the values an omitted param binds; the panel opens with them
   *  checked. Exclusive with default. */
  defaults?: Array<string | number>;
  /** A "group" param's per-row schema: scalar prompt/lookup fields, one level deep. Its run value
   *  is an array of objects keyed by these field names. */
  fields?: FormulaParam[];
}

export type DisplayMode = "table" | "checklist" | "cards" | "board" | "calendar";

/** A display's interaction slots, each holding the UUID of a formula attached to the view: add /
 *  toggle / edit / delete name actions, detail names a row-detail view, reorder names the update
 *  action a checklist's drag-to-reorder runs with :ordered_rowids, move names the update action a
 *  board's drag-across-columns runs with :rowid + the destination column's value. bulk and
 *  row_actions are ordered lists (array order = render order): bulk fills the selection toolbar
 *  with :rowids actions, row_actions adds extra single-row actions to the per-row ⋯ menu. */
export interface FormulaDisplaySlots {
  add?: string;
  toggle?: string;
  edit?: string;
  delete?: string;
  detail?: string;
  reorder?: string;
  move?: string;
  bulk?: string[];
  row_actions?: string[];
}

/** One result column's presentation: its role in the mode's rendering and/or a value format, or
 *  hidden to drop it from the table grid while keeping it in the row data. */
export interface DisplayColumn {
  column: string;
  /** title = row's primary text; body = card preview; check = checklist checkbox; group = the
   *  value placing each row in a board column; day = the day placing each row on a calendar. */
  role?: "title" | "body" | "check" | "group" | "day";
  /** markdown renders the value as simple markdown; absent = plain text. */
  format?: "markdown";
  /** Dropped from the table grid but kept in the row data — e.g. a machine column seeding an edit
   *  slot's group param. Exclusive with role/format. */
  hidden?: boolean;
}

export type Weekday =
  | "monday"
  | "tuesday"
  | "wednesday"
  | "thursday"
  | "friday"
  | "saturday"
  | "sunday";

/** The days a calendar shows, one of two kinds. A weekly window names its first and last weekday
 *  (absent: Monday, and a full week from the first; the pair may wrap) and repeats every 7 days. A
 *  fixed range names its first day and a length (absent: 7) and steps by that length. Absent
 *  altogether, the window is the Monday-to-Sunday week. */
export interface FormulaDisplayWindow {
  week_start?: Weekday;
  week_end?: Weekday;
  /** ISO day, "YYYY-MM-DD". */
  start?: string;
  days?: number;
}

/** A view's optional presentation config; null renders the default table with no declared slots. */
export interface FormulaDisplay {
  mode: DisplayMode;
  columns?: DisplayColumn[];
  slots?: FormulaDisplaySlots;
  /** Calendar mode only. */
  window?: FormulaDisplayWindow;
}

/** roleColumn returns the column name holding the given role, if assigned. */
export function roleColumn(
  display: FormulaDisplay | null | undefined,
  role: "title" | "body" | "check" | "group" | "day",
): string | undefined {
  return display?.columns?.find((c) => c.role === role)?.column;
}

/** markdownColumns returns the set of column names rendered as markdown. */
export function markdownColumns(display: FormulaDisplay | null | undefined): Set<string> {
  return new Set(
    (display?.columns ?? []).filter((c) => c.format === "markdown").map((c) => c.column),
  );
}

/** hiddenColumns returns the set of column names the table grid should not render. */
export function hiddenColumns(display: FormulaDisplay | null | undefined): Set<string> {
  return new Set((display?.columns ?? []).filter((c) => c.hidden).map((c) => c.column));
}

export interface Formula {
  id: string;
  name: string;
  /** Human name shown instead of the slug; absent means the name is shown. Never a reference. */
  label?: string;
  description: string;
  sql: string;
  kind: FormulaKind;
  statement_verb: StatementVerb;
  params: FormulaParam[];
  display: FormulaDisplay | null;
  /** An assistant-only formula: omitted from the members' listing by default. */
  hidden: boolean;
  /** UUID of the view an action attaches to, or null for a top-level formula. */
  attached_to_formula_id: string | null;
  display_order: number;
  created_at: string;
  updated_at: string;
}

/** Keyset-pagination metadata returned for a paginated view run. */
export interface FormulaPagination {
  has_more: boolean;
  /** The :cursor value to pass for the next page, or null when the page can't be resumed by cursor. */
  next_cursor: number | null;
}

/** A formula run result: the query result, plus pagination metadata for a paginated view. The
 *  statement count says how many statements ran — the affected-row count sums across all of them. */
export interface FormulaRunResult extends QueryResult {
  statement_count?: number;
  pagination?: FormulaPagination;
}

export interface RunFormulaRequest {
  /**
   * Every declared parameter value keyed by name: scalars for prompt params, selection-single
   * (`rowid`) and pagination controls, plus an array of row IDs for a bulk selection (`rowids`).
   */
  params?: Record<string, unknown>;
}

function basePath(bdbId: string): string {
  return `/api/databases/v1/bdbs/${bdbId}/formulas`;
}

export function listFormulas(bdbId: string): Promise<Formula[]> {
  return get<Formula[]>(basePath(bdbId));
}

export function getFormula(bdbId: string, ref: string): Promise<Formula> {
  return get<Formula>(`${basePath(bdbId)}/${encodeURIComponent(ref)}`);
}

export function runFormula(
  bdbId: string,
  ref: string,
  data: RunFormulaRequest,
): Promise<FormulaRunResult> {
  return post<FormulaRunResult>(`${basePath(bdbId)}/${encodeURIComponent(ref)}/run`, data);
}
