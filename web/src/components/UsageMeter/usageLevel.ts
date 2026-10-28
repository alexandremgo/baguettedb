export type UsageLevel = "ok" | "warning" | "danger";

/** Maps a used/limit ratio to its display level: warning at 80% of the limit, danger at 95%. */
export function usageLevel(usedBytes: number, limitBytes: number): UsageLevel {
  if (limitBytes <= 0) return "ok";
  const ratio = usedBytes / limitBytes;
  if (ratio >= 0.95) return "danger";
  if (ratio >= 0.8) return "warning";
  return "ok";
}
