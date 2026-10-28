/** Truncated text cell with click-to-expand popover and optional copy support. The expansion
 *  shows as a top-layer popover anchored to the cell, so a clipping ancestor (a table's scroll
 *  wrapper) can never cut it off; it closes on outside click, Escape, or scroll. */

import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { showAnchoredPopover } from "../../utils/anchoredPopover";
import { Check, Copy, X } from "../icons";
import styles from "./TruncatedCell.module.css";

interface TruncatedCellProps {
  /** Text to display (truncated by default). */
  children: string;
  /** Maximum number of characters before truncation. Defaults to 40. */
  maxLength?: number;
  /** If true, shows a copy button inside the popover. Defaults to false. */
  copyable?: boolean;
  /** Optional CSS class name. */
  className?: string;
}

export function TruncatedCell({
  children,
  maxLength = 40,
  copyable = false,
  className,
}: TruncatedCellProps) {
  const [open, setOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  const rootRef = useRef<HTMLSpanElement>(null);
  const popoverRef = useRef<HTMLDivElement>(null);
  const isTruncated = children.length > maxLength;

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

  async function handleCopy() {
    try {
      await navigator.clipboard.writeText(children);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard API may fail in insecure contexts.
    }
  }

  if (!isTruncated && !copyable) {
    return <span className={className}>{children}</span>;
  }

  return (
    <span
      className={[styles.cell, isTruncated ? styles.interactive : "", className ?? ""]
        .filter(Boolean)
        .join(" ")}
      ref={rootRef}
    >
      <span
        className={styles.trigger}
        onClick={(e) => {
          if (isTruncated) {
            // A truncated cell may live inside a clickable row; expanding it must not also
            // trigger the row's own click action.
            e.stopPropagation();
            setOpen(!open);
          }
        }}
      >
        {isTruncated ? children.slice(0, maxLength) + "..." : children}
      </span>

      {open && (
        <div
          className={styles.popover}
          ref={popoverRef}
          popover="manual"
          onClick={(e) => e.stopPropagation()}
        >
          <div className={styles.popoverHeader}>
            {copyable && (
              <button
                className={[styles.iconBtn, copied ? styles.iconBtnActive : ""].filter(Boolean).join(" ")}
                onClick={handleCopy}
                title={copied ? "Copied!" : "Copy to clipboard"}
              >
                {copied ? <Check size={14} aria-hidden="true" /> : <Copy size={14} aria-hidden="true" />}
              </button>
            )}
            <button className={styles.iconBtn} onClick={() => setOpen(false)} title="Close">
              <X size={14} aria-hidden="true" />
            </button>
          </div>
          <div className={styles.popoverContent}>{children}</div>
        </div>
      )}
    </span>
  );
}
