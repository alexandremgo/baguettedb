/** Shared value model for the formula param mini-form. */

import type { FormulaParam } from "../../api/formulas";

/** One prompt parameter's pending value: the typed (or picked) text, plus — for a group param —
 *  its pending rows, each a map of field name to typed text, and — for a multi-select lookup —
 *  its checked values. `values` undefined means the multi-select is untouched: the run omits the
 *  param, so the server binds its default ("all" resolves to every lookup value) exactly as the
 *  untouched control displays. */
export interface ParamFieldValue {
  raw: string;
  rows?: Record<string, string>[];
  values?: string[];
}

export const emptyParamField: ParamFieldValue = { raw: "" };

/** The params an add action's quick-add form renders, or null when the form cannot render — no
 *  param is marked inline, or a required param is unmarked, so the form could never submit and the
 *  add falls back to the + Add dialog. A when-gated required param hidden under the untouched
 *  form's state (defaults only) doesn't block: it is not required then, and an omitting run binds
 *  its default, else NULL. Unmarked optional params are simply absent from the result: the form
 *  omits them and the run binds SQL NULL. */
export function quickAddParams(
  promptParams: FormulaParam[],
): FormulaParam[] | null {
  const marked = promptParams.filter((p) => p.inline);
  if (marked.length === 0) return null;
  const fields = defaultFields(promptParams);
  // A multi-select with a default never blocks either: omitting it is always a valid submit.
  if (
    promptParams.some(
      (p) =>
        !p.optional &&
        !p.inline &&
        !(p.multiple && hasMultiDefault(p)) &&
        paramVisible(p, promptParams, fields),
    )
  ) {
    return null;
  }
  return marked;
}

/** The run params binding a calendar's visible week to the view's window pair, when the formula
 *  declares it: window_start and window_end as ISO days. Empty for any other formula, so callers
 *  can spread it unconditionally. */
export function windowRunParams(
  formula: FormulaParam[] | { params: FormulaParam[] },
  window: { start: string; end: string } | null,
): Record<string, string> {
  const params = Array.isArray(formula) ? formula : formula.params;
  if (!window || !params.some((p) => p.source === "window")) return {};
  return { window_start: window.start, window_end: window.end };
}

/** Pre-fills a form's fields from the params' declared defaults, so an untouched form shows
 *  exactly what an omitting run binds. Params without a default start empty, except a required
 *  group, which opens with one blank row — the run needs a row anyway, so the form shows it rather
 *  than an empty list and an add button. A stored default is always valid for its param (enforced
 *  at create time), so no re-checking here. */
export function defaultFields(
  promptParams: FormulaParam[],
): Record<string, ParamFieldValue> {
  const out: Record<string, ParamFieldValue> = {};
  for (const p of promptParams) {
    if (p.type === "group" && !p.optional) {
      out[p.name] = { raw: "", rows: [{}] };
      continue;
    }
    // A multi-select's default stays server-side: its field starts untouched (values undefined), the
    // panel shows the default checked, and the omitting run binds it.
    if (p.default != null && !p.multiple) {
      out[p.name] = { raw: toInputForm(p.default, p.type) };
    }
  }
  return out;
}

/** Whether a multi-select declares any default an omitting run would bind: the "all" literal or a
 *  subset in defaults. */
export function hasMultiDefault(param: FormulaParam): boolean {
  return param.default === "all" || (param.defaults?.length ?? 0) > 0;
}

/** The values a multi-select's declared default checks while the field is untouched: "all" means
 *  every option (the caller resolves it against the loaded options), a defaults subset means exactly
 *  those values in their string form, and no default means none. */
export function multiDefaultValues(
  param: FormulaParam,
  allValues: string[],
): string[] {
  if (param.default === "all") return allValues;
  return (param.defaults ?? []).map((v) => String(v));
}

/** Option-select params carrying a default: their select drops its blank entry, since the
 *  pre-selected default already expresses what an omission binds. A stored default is always one
 *  of its param's options (enforced at create time), so presence is all that needs checking. */
export function defaultedSelects(promptParams: FormulaParam[]): Set<string> {
  return new Set(
    promptParams
      .filter((p) => p.default != null && p.options != null)
      .map((p) => p.name),
  );
}

/** Whether a param's field currently renders: unconditional without a when clause, else its
 *  controller's current value must compare true against the condition's value — eq/ne as exact
 *  strings (a boolean controller normalized to "true"/"false"), the ordered ops numerically for
 *  numeric controllers and as canonical strings for date/time ones, false while the controller is
 *  empty or unparseable. A hidden field submits nothing, whatever was typed. */
export function paramVisible(
  param: FormulaParam,
  promptParams: FormulaParam[],
  fields: Record<string, ParamFieldValue>,
): boolean {
  const when = param.when;
  if (!when) return true;
  const controller = promptParams.find((p) => p.name === when.param);
  let raw = (fields[when.param] ?? emptyParamField).raw;
  // The datetime control holds the "T" form; conditions are authored in the canonical space form.
  if (controller?.type === "datetime") raw = raw.replace("T", " ");

  const op = when.op ?? "eq";
  if (op === "eq" || op === "ne") {
    const actual =
      controller?.type === "boolean"
        ? raw === "true"
          ? "true"
          : "false"
        : raw;
    return op === "eq" ? actual === when.value : actual !== when.value;
  }
  if (raw.trim() === "") return false;
  let cmp: number;
  if (controller?.type === "integer" || controller?.type === "float") {
    const a = Number(raw);
    const b = Number(when.value);
    if (!Number.isFinite(a) || !Number.isFinite(b)) return false;
    cmp = a === b ? 0 : a < b ? -1 : 1;
  } else {
    // Canonical date/datetime/time forms order correctly as plain strings.
    cmp = raw === when.value ? 0 : raw < when.value ? -1 : 1;
  }
  switch (op) {
    case "lt":
      return cmp < 0;
    case "lte":
      return cmp <= 0;
    case "gt":
      return cmp > 0;
    case "gte":
      return cmp >= 0;
    default:
      return false;
  }
}

/** The params whose fields currently render, given the form's pending values. A form rendering
 *  only a subset (the quick-add bar) passes the action's full param list as controllers, so a
 *  condition can read a controller the subset doesn't carry. */
export function visibleParams(
  promptParams: FormulaParam[],
  fields: Record<string, ParamFieldValue>,
  controllers: FormulaParam[] = promptParams,
): FormulaParam[] {
  return promptParams.filter((p) => paramVisible(p, controllers, fields));
}

/** Whether a currently visible required field is still empty: a boolean is never empty (unchecked
 *  means false), a group needs at least one row with something in it (a blank opening row counts
 *  for nothing), everything else typed or picked text. A hidden field never blocks — the run
 *  doesn't require it while its condition is off. */
export function missingVisibleRequired(
  promptParams: FormulaParam[],
  fields: Record<string, ParamFieldValue>,
): boolean {
  return visibleParams(promptParams, fields).some((p) => {
    if (p.optional || p.type === "boolean") return false;
    const field = fields[p.name] ?? emptyParamField;
    if (p.type === "group") {
      return !(field.rows ?? []).some((row) =>
        Object.values(row).some((v) => String(v ?? "").trim() !== ""),
      );
    }
    // An untouched multi-select is missing only without a default — omitting binds the default;
    // an explicit empty selection always blocks, since the server rejects it on a required param.
    if (p.multiple) {
      return field.values === undefined
        ? !hasMultiDefault(p)
        : field.values.length === 0;
    }
    return field.raw.trim() === "";
  });
}

/** Whether a param's value travels as its raw string rather than through a numeric parse. Dates and
 *  times join text here: "2026-08-12" is not a number, and letting it fall through to the numeric
 *  branch would rely on the non-finite fallback to send it intact. */
function sendsAsText(type: string): boolean {
  return (
    type === "text" || type === "date" || type === "datetime" || type === "time"
  );
}

/** One picked or typed value in its run form: text-shaped types pass through, numbers parse with
 *  the non-finite fallback so the server rejects garbage with a clear type error. */
function parseScalar(type: string, raw: string): unknown {
  if (sendsAsText(type)) return raw;
  const n = Number(raw);
  return Number.isFinite(n) ? n : raw;
}

/** Converts a stored or authored value to the form the native input control accepts: a datetime's
 *  canonical "YYYY-MM-DD HH:MM:SS" becomes the control's "YYYY-MM-DDTHH:MM:SS" (a space-separated
 *  value renders as an empty picker); every other type passes through. The run sends the "T" form
 *  back and the server re-canonicalizes, so display never changes what gets stored. */
function toInputForm(raw: string, type: string): string {
  if (type === "datetime") return raw.replace(" ", "T");
  return raw;
}

/** Starting text for a pre-filled field: a boolean reads "true"/"", everything else the value's
 *  string form (a JSON-array column like `tags` arrives as its raw text, ready to edit). */
export function seedRaw(value: unknown, type: string): string {
  if (value == null) return "";
  if (type === "boolean") {
    return value === 1 || value === "1" || value === true || value === "true"
      ? "true"
      : "";
  }
  return toInputForm(String(value), type);
}

/** The current values of a lookup param's scope siblings, keyed by sibling name and normalized to
 *  the stored forms the option query compares against (a boolean to 0/1, a datetime to its space
 *  form). Empty fields are left out — an unset sibling adds no constraint — and the result is
 *  undefined when the lookup declares no scope at all. */
export function scopeRawValues(
  param: FormulaParam,
  promptParams: FormulaParam[],
  fields: Record<string, ParamFieldValue>,
): Record<string, string> | undefined {
  const scope = param.lookup?.scope;
  if (!scope) return undefined;
  const out: Record<string, string> = {};
  for (const name of Object.keys(scope)) {
    const sibling = promptParams.find((p) => p.name === name);
    let raw = (fields[name] ?? emptyParamField).raw.trim();
    if (sibling?.type === "boolean") raw = raw === "true" ? "1" : "";
    if (sibling?.type === "datetime") raw = raw.replace("T", " ");
    if (raw !== "") out[name] = raw;
  }
  return out;
}

/** Pre-fills a form's fields from one row's values, matching param names to column names. A group
 *  param seeds from a same-named column holding a JSON array of row objects (keys = the group's
 *  field names, e.g. a lookup field takes the value_column id); a column not shaped that way just
 *  leaves the group empty. */
export function seedFieldsFromRow(
  promptParams: FormulaParam[],
  rowValues: Record<string, unknown>,
): Record<string, ParamFieldValue> {
  const seed: Record<string, ParamFieldValue> = {};
  for (const p of promptParams) {
    if (!(p.name in rowValues)) continue;
    if (p.type === "group") {
      const rows = seedGroupRows(p, rowValues[p.name]);
      if (rows && rows.length > 0) seed[p.name] = { raw: "", rows };
    } else if (p.multiple) {
      const values = seedMultiValues(rowValues[p.name]);
      if (values && values.length > 0) seed[p.name] = { raw: "", values };
    } else {
      seed[p.name] = { raw: seedRaw(rowValues[p.name], p.type) };
    }
  }
  return seed;
}

/** Turns a multi-select param's seed value — a JSON array (or its string form) of scalars — into
 *  checked values in their string form. Returns undefined when the value is absent or not
 *  array-shaped, so the control simply opens in its untouched state. */
function seedMultiValues(value: unknown): string[] | undefined {
  let arr: unknown = value;
  if (typeof value === "string") {
    const trimmed = value.trim();
    if (trimmed === "") return undefined;
    try {
      arr = JSON.parse(trimmed);
    } catch {
      return undefined;
    }
  }
  if (!Array.isArray(arr)) return undefined;
  return arr
    .filter((v) => v != null && typeof v !== "object")
    .map((v) => String(v));
}

/** Turns a group param's seed value — a JSON array (or its string form) of row objects — into
 *  pending rows, each field coerced to its string form via seedRaw. Returns undefined when the
 *  value is absent or not array-shaped, so the group simply opens empty. */
function seedGroupRows(
  param: FormulaParam,
  value: unknown,
): Record<string, string>[] | undefined {
  let arr: unknown = value;
  if (typeof value === "string") {
    const trimmed = value.trim();
    if (trimmed === "") return undefined;
    try {
      arr = JSON.parse(trimmed);
    } catch {
      return undefined;
    }
  }
  if (!Array.isArray(arr)) return undefined;
  const fields = param.fields ?? [];
  return arr.map((row) => {
    const obj = (row ?? {}) as Record<string, unknown>;
    const out: Record<string, string> = {};
    for (const field of fields) {
      out[field.name] = seedRaw(obj[field.name], field.type);
    }
    return out;
  });
}

/**
 * Builds the CSV-export query-string values from the form fields, mirroring buildRunParams: a boolean
 * always sends "true"/"false", text is sent as typed, an empty optional field is omitted (SQL NULL), an
 * empty required number is omitted so the server reports it as a missing value, and a conditionally
 * hidden field is omitted whatever it holds.
 */
export function buildExportParams(
  promptParams: FormulaParam[],
  fields: Record<string, ParamFieldValue>,
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const param of promptParams) {
    // A hidden field submits nothing — whatever was typed before its condition stopped holding.
    if (!paramVisible(param, promptParams, fields)) continue;
    const field = fields[param.name] ?? emptyParamField;
    if (param.type === "group") {
      // A query string cannot carry group rows; the server rejects group params on this surface.
      continue;
    }
    if (param.multiple) {
      // A multi-select travels a query string as a JSON array literal; untouched means omitted,
      // so the server resolves the "all" default exactly like a run.
      if (field.values !== undefined) {
        out[param.name] = JSON.stringify(
          field.values.map((v) => parseScalar(param.type, v)),
        );
      }
      continue;
    }
    if (param.type === "boolean") {
      out[param.name] = field.raw === "true" ? "true" : "false";
    } else if (param.optional && field.raw.trim() === "") {
      // Omitted on purpose: an empty optional field means "no value".
    } else if (sendsAsText(param.type)) {
      out[param.name] = field.raw;
    } else if (field.raw.trim() !== "") {
      out[param.name] = field.raw;
    }
  }
  return out;
}

/**
 * Builds the run-params object from the form fields. A boolean always sends true/false (a checkbox
 * is never empty); an empty optional field is omitted so the server binds SQL NULL in its place;
 * an empty required number is omitted so the server reports it as a missing value; a conditionally
 * hidden field is omitted whatever it holds. A form building only a subset (the quick-add bar)
 * passes the action's full param list as controllers.
 */
export function buildRunParams(
  promptParams: FormulaParam[],
  fields: Record<string, ParamFieldValue>,
  controllers: FormulaParam[] = promptParams,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const param of promptParams) {
    // A hidden field submits nothing — whatever was typed before its condition stopped holding.
    if (!paramVisible(param, controllers, fields)) continue;
    const field = fields[param.name] ?? emptyParamField;
    if (param.type === "group") {
      const rows = (field.rows ?? []).map((row) => buildGroupRow(param, row));
      // A group with no rows is omitted either way: the server binds '[]' for an optional one
      // and reports a required one as missing.
      if (rows.length > 0) {
        out[param.name] = rows;
      }
    } else if (param.multiple) {
      // Untouched means omitted — the server binds the declared default ("all" or an array) when
      // there is one, else '[]' for an optional param. A touched selection always travels, [] included: unchecking everything
      // is a deliberate "none", distinct from omission when a default exists.
      if (field.values !== undefined) {
        out[param.name] = field.values.map((v) => parseScalar(param.type, v));
      }
    } else if (param.type === "boolean") {
      // A checkbox is never "empty": unchecked means false, so the value is always sent.
      out[param.name] = field.raw === "true";
    } else if (param.optional && field.raw.trim() === "") {
      // Omitted on purpose: an empty optional field means "no value".
    } else if (sendsAsText(param.type)) {
      out[param.name] = field.raw;
    } else if (field.raw.trim() !== "") {
      const n = Number(field.raw);
      // A non-finite number (e.g. "1e999" → Infinity) would JSON-serialize as null; send the raw
      // text instead so the server rejects it with a clear type error.
      out[param.name] = Number.isFinite(n) ? n : field.raw;
    }
  }
  return out;
}

/** Builds one group row's run value, coercing each field like a scalar param: booleans always
 *  send true/false, empty fields are omitted (the server nulls an optional one and names a
 *  required one per row), numbers parse with the same non-finite fallback. */
function buildGroupRow(
  param: FormulaParam,
  row: Record<string, string>,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const field of param.fields ?? []) {
    const raw = row[field.name] ?? "";
    if (field.type === "boolean") {
      out[field.name] = raw === "true";
    } else if (raw.trim() === "") {
      // Omitted on purpose — the server decides between NULL and a per-row error.
    } else if (sendsAsText(field.type)) {
      out[field.name] = raw;
    } else {
      const n = Number(raw);
      out[field.name] = Number.isFinite(n) ? n : raw;
    }
  }
  return out;
}
