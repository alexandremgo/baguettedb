/** StructuredCell renders a flat JSON aggregate (object entries or array items) as small chips —
 *  the chip shape signals structured data without exposing the raw JSON syntax. Long chip texts
 *  ellipsize, and past `maxVisible` chips the rest collapse behind a "+N" chip that opens a
 *  top-layer popover listing everything (closing on outside click, Escape, or scroll). */

import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { showAnchoredPopover } from "../../utils/anchoredPopover";
import type { StructuredValue } from "../../utils/cellValue";
import styles from "./StructuredCell.module.css";

interface StructuredCellProps {
  value: StructuredValue;
  /** Chips shown inline before the rest collapse behind the "+N" chip. Defaults to 3. */
  maxVisible?: number;
  /** Lets the inline chips wrap onto several lines. Off by default so a table cell stays one line
   *  tall; turn on where vertical space is free (a record card). */
  wrap?: boolean;
}

interface Chip {
  key?: string;
  text: string;
}

function renderChip(chip: Chip, i: number) {
  return (
    <span key={i} className={styles.chip} title={chip.key ? `${chip.key}: ${chip.text}` : chip.text}>
      {chip.key !== undefined && <span className={styles.chipKey}>{chip.key}</span>}
      <span className={styles.chipValue}>{chip.text}</span>
    </span>
  );
}

export function StructuredCell({ value, maxVisible = 3, wrap = false }: StructuredCellProps) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLSpanElement>(null);
  const popoverRef = useRef<HTMLDivElement>(null);

  const chips: Chip[] =
    value.kind === "pairs"
      ? value.entries.map((entry) => ({ key: entry.key, text: entry.value }))
      : value.items.map((text) => ({ text }));
  const overflow = chips.length - maxVisible;

  useLayoutEffect(() => {
    if (open && rootRef.current && popoverRef.current) {
      showAnchoredPopover(rootRef.current, popoverRef.current, "left");
    }
  }, [open]);

  useEffect(() => {
    if (!open) return;

    function handleClickOutside(e: MouseEvent) {
      // The popover stays a DOM child of the root, so this covers clicks inside it too.
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    }

    function handleEscape(e: KeyboardEvent) {
      if (e.key === "Escape") setOpen(false);
    }

    // The popover sits at viewport coordinates; a scroll would leave it floating detached.
    function handleScroll() {
      setOpen(false);
    }

    document.addEventListener("mousedown", handleClickOutside);
    document.addEventListener("keydown", handleEscape);
    document.addEventListener("scroll", handleScroll, true);
    return () => {
      document.removeEventListener("mousedown", handleClickOutside);
      document.removeEventListener("keydown", handleEscape);
      document.removeEventListener("scroll", handleScroll, true);
    };
  }, [open]);

  return (
    <span className={[styles.chips, wrap ? styles.chipsWrap : ""].filter(Boolean).join(" ")} ref={rootRef}>
      {chips.slice(0, maxVisible).map(renderChip)}
      {overflow > 0 && (
        <button
          type="button"
          className={styles.moreChip}
          aria-label={`Show ${overflow} more`}
          onClick={(e) => {
            // The cell may live inside a clickable row; expanding must not also trigger the row.
            e.stopPropagation();
            setOpen(!open);
          }}
        >
          +{overflow}
        </button>
      )}

      {open && (
        <div
          className={styles.popover}
          ref={popoverRef}
          popover="manual"
          onClick={(e) => e.stopPropagation()}
        >
          {chips.map((chip, i) => (
            <div key={i} className={styles.popoverRow}>
              {chip.key !== undefined && <span className={styles.popoverKey}>{chip.key}</span>}
              <span className={styles.popoverValue}>{chip.text}</span>
            </div>
          ))}
        </div>
      )}
    </span>
  );
}
