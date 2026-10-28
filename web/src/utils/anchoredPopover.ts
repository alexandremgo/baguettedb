/** Shows an element as a top-layer popover anchored to a reference element: promoted via the
 *  native Popover API — so no scroll container, table wrapper, or open dialog can clip or overlap
 *  it — and positioned at viewport coordinates just below the anchor, flipping above when the
 *  viewport bottom would cut it off. Viewport coordinates go stale the moment anything scrolls,
 *  so callers must close the popover on any scroll. */
export function showAnchoredPopover(
  anchor: HTMLElement,
  pop: HTMLElement,
  align: "left" | "right",
) {
  // Browsers without the Popover API keep the element in place; its fixed positioning still
  // escapes overflow clipping, only top-layer stacking (e.g. above a modal) is lost.
  if (typeof pop.showPopover === "function") {
    pop.showPopover();
  }
  const rect = anchor.getBoundingClientRect();
  pop.style.top = `${rect.bottom + 4}px`;
  if (align === "right") {
    pop.style.right = `${window.innerWidth - rect.right}px`;
    pop.style.left = "auto";
  } else {
    pop.style.left = `${rect.left}px`;
    pop.style.right = "auto";
  }
  const popRect = pop.getBoundingClientRect();
  if (popRect.bottom > window.innerHeight) {
    pop.style.top = `${Math.max(rect.top - popRect.height - 4, 0)}px`;
  }
}
