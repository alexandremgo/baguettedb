import { useSyncExternalStore } from "react";

/** useMediaQuery reports whether the given CSS media query currently matches, re-rendering on
 *  changes (a resize across the breakpoint, a rotation). */
export function useMediaQuery(query: string): boolean {
  return useSyncExternalStore(
    (onChange) => {
      const mql = window.matchMedia(query);
      mql.addEventListener("change", onChange);
      return () => mql.removeEventListener("change", onChange);
    },
    () => window.matchMedia(query).matches,
  );
}
