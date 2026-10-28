/** Display helpers for query-result cell values, shared across the table views. */

/** Reports whether a query-result value holds SQL NULL (or is absent). */
export function isNullValue(value: unknown): boolean {
  return value === null || value === undefined;
}

/**
 * Stringifies a query-result value for display. A NULL value renders as `nullText`
 * (defaults to "NULL"); pass "" for views that show blanks instead.
 */
export function formatCellValue(value: unknown, nullText = "NULL"): string {
  return isNullValue(value) ? nullText : String(value);
}

/** A flat JSON value broken into displayable parts: an object's key/value entries or an array's items. */
export type StructuredValue =
  | { kind: "pairs"; entries: { key: string; value: string }[] }
  | { kind: "items"; items: string[] };

function isScalar(v: unknown): boolean {
  return v === null || typeof v === "string" || typeof v === "number" || typeof v === "boolean";
}

function formatScalar(v: unknown): string {
  return v === null ? "NULL" : String(v);
}

/**
 * Parses a cell holding a flat JSON object or array of scalars (the shape SQLite's json_group_*
 * aggregates produce) into displayable parts. Returns null for anything else — plain text, nested
 * structures, empty containers — so callers fall back to the literal value.
 */
export function parseStructuredValue(value: unknown): StructuredValue | null {
  if (typeof value !== "string") return null;
  const trimmed = value.trim();
  if (!trimmed.startsWith("{") && !trimmed.startsWith("[")) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch {
    return null;
  }
  if (Array.isArray(parsed)) {
    if (parsed.length === 0 || !parsed.every(isScalar)) return null;
    return { kind: "items", items: parsed.map(formatScalar) };
  }
  if (parsed !== null && typeof parsed === "object") {
    const entries = Object.entries(parsed);
    if (entries.length === 0 || !entries.every(([, v]) => isScalar(v))) return null;
    return { kind: "pairs", entries: entries.map(([key, v]) => ({ key, value: formatScalar(v) })) };
  }
  return null;
}
