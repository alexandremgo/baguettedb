/** Date formatting helpers shared across pages. */

/**
 * formatPurgeDate renders an ISO timestamp as a locale-formatted date-only string, e.g. "Mar 6, 2026". Time of day is
 * omitted because the purge sweep precision is ± 1h and exposing seconds would be misleading.
 */
export function formatPurgeDate(iso: string | undefined): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return d.toLocaleDateString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
}

/** formatDate renders an ISO timestamp as a locale-formatted date-only string, e.g. "Mar 6, 2026". */
export function formatDate(iso: string): string {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}
