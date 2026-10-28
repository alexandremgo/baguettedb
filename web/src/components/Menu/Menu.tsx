/** Small dropdown menu of actions behind a trigger button. The open list shows as a top-layer
 *  popover anchored to the trigger, so a clipping ancestor (a table's scroll wrapper, a modal)
 *  can never cut it off. Closes on outside click, Escape, scroll, or select. */

import { Fragment, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { showAnchoredPopover } from "../../utils/anchoredPopover";
import { Button } from "../Button";
import { Ellipsis } from "../icons";
import styles from "./Menu.module.css";

export interface MenuItem {
  label: ReactNode;
  onSelect: () => void;
  /** Renders the item in the destructive color (e.g. Delete). */
  destructive?: boolean;
  /** Draws a rule above the item, opening a new group. */
  separatorBefore?: boolean;
}

interface MenuProps {
  /** Trigger label; defaults to an ellipsis glyph. */
  label?: ReactNode;
  items: MenuItem[];
  "aria-label"?: string;
}

export function Menu({
  label = <Ellipsis size={16} aria-hidden="true" />,
  items,
  "aria-label": ariaLabel,
}: MenuProps) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    if (open && rootRef.current && listRef.current) {
      showAnchoredPopover(rootRef.current, listRef.current, "right");
    }
  }, [open]);

  useEffect(() => {
    if (!open) return;

    function onPointerDown(e: PointerEvent) {
      // The popover list stays a DOM child of the root, so this covers clicks inside it too.
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    }
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === "Escape") {
        setOpen(false);
      }
    }
    // The list sits at viewport coordinates; a scroll would leave it floating detached.
    function onScroll() {
      setOpen(false);
    }
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    document.addEventListener("scroll", onScroll, true);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
      document.removeEventListener("scroll", onScroll, true);
    };
  }, [open]);

  return (
    <div className={styles.root} ref={rootRef}>
      <Button
        size="sm"
        variant="ghost"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={ariaLabel}
        onClick={() => setOpen((prev) => !prev)}
      >
        {label}
      </Button>
      {open && (
        <div className={styles.list} role="menu" ref={listRef} popover="manual">
          {items.map((item, i) => (
            <Fragment key={i}>
              {item.separatorBefore && i > 0 && <hr className={styles.separator} role="separator" />}
              <button
                role="menuitem"
                className={[styles.item, item.destructive ? styles.destructive : ""].filter(Boolean).join(" ")}
                onClick={() => {
                  setOpen(false);
                  item.onSelect();
                }}
              >
                {item.label}
              </button>
            </Fragment>
          ))}
        </div>
      )}
    </div>
  );
}
