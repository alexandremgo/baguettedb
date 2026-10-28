/** Remembers, per view, whether its lanes lay out side by side or stack vertically on wide screens. The
 *  choice lives in this browser only; a browser that refuses storage falls back to columns, and the toggle
 *  then lasts the session. */

import { useCallback, useState } from "react";

export type LaneLayout = "columns" | "stacked";

const STORAGE_PREFIX = "lane-layout:";

function storedLayout(formulaId: string): LaneLayout {
  try {
    return localStorage.getItem(STORAGE_PREFIX + formulaId) === "stacked"
      ? "stacked"
      : "columns";
  } catch {
    return "columns";
  }
}

export function useLaneLayout(
  formulaId: string,
): [LaneLayout, (next: LaneLayout) => void] {
  // Keyed by the view so a switch to another view reads that view's own choice instead of carrying
  // this one's over.
  const [stored, setStored] = useState(() => ({
    formulaId,
    layout: storedLayout(formulaId),
  }));
  const layout =
    stored.formulaId === formulaId ? stored.layout : storedLayout(formulaId);

  const setLayout = useCallback(
    (next: LaneLayout) => {
      setStored({ formulaId, layout: next });
      try {
        localStorage.setItem(STORAGE_PREFIX + formulaId, next);
      } catch {
        // Storage refused: the choice still holds for the session.
      }
    },
    [formulaId],
  );

  return [layout, setLayout];
}
