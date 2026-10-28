/** Multi-select picker: a collapsed select-style trigger summarizing the checked set ("All
 *  members", "Alice, Bob", "3 selected"), opening a top-layer checkbox panel that stays open
 *  across toggles, with an All/None header and — past a handful of options — a filter input.
 *  `values` undefined means untouched: the default state (the defaultValues, else nothing), so
 *  the caller can keep omitting the param until the user actually picks. */

import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { showAnchoredPopover } from "../../utils/anchoredPopover";
import { ChevronDown } from "../icons";
import styles from "./MultiSelect.module.css";

export interface MultiSelectOption {
  value: string;
  label: string;
}

/** Options count past which the panel grows a filter input. */
const SEARCH_THRESHOLD = 10;

interface MultiSelectProps {
  id?: string;
  className?: string;
  /** The checked values; undefined = untouched (the defaultValues, else none). */
  values: string[] | undefined;
  /** What the untouched state checks — the param's declared default, which an omitting run binds:
   *  every option for an "all" default, the declared subset for an array one. */
  defaultValues?: string[];
  options: MultiSelectOption[];
  /** Plural noun for the summary label ("members" → "All members"). */
  noun?: string;
  /** The option page stopped at its cap — filtering alone cannot reach every row. */
  truncated?: boolean;
  loading?: boolean;
  error?: string;
  onChange: (values: string[]) => void;
  /** Reports the raw filter text so the caller can narrow the page server-side (debounced there). */
  onSearchChange?: (term: string) => void;
}

export function MultiSelect({
  id,
  className,
  values,
  defaultValues,
  options,
  noun,
  truncated,
  loading,
  error,
  onChange,
  onSearchChange,
}: MultiSelectProps) {
  const [open, setOpen] = useState(false);
  const [term, setTerm] = useState("");
  const rootRef = useRef<HTMLDivElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);

  const allValues = options.map((o) => o.value);
  const checked = values ?? defaultValues ?? [];
  const checkedSet = new Set(checked);

  const lowered = term.trim().toLowerCase();
  const shown =
    lowered === "" ? options : options.filter((o) => o.label.toLowerCase().includes(lowered));

  useLayoutEffect(() => {
    const root = rootRef.current;
    const panel = panelRef.current;
    if (!open || !root || !panel) return;
    showAnchoredPopover(root, panel, "left");
    panel.style.minWidth = `${root.getBoundingClientRect().width}px`;
  }, [open, shown.length, truncated]);

  useEffect(() => {
    if (!open) return;

    function onPointerDown(e: PointerEvent) {
      // The panel stays a DOM child of the root, so this covers clicks inside it too.
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    }
    // The panel sits at viewport coordinates; an outside scroll would leave it floating detached.
    function onScroll(e: Event) {
      if (panelRef.current && panelRef.current.contains(e.target as Node)) {
        return;
      }
      setOpen(false);
    }
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("scroll", onScroll, true);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("scroll", onScroll, true);
    };
  }, [open]);

  function closePanel() {
    setOpen(false);
    setTerm("");
    onSearchChange?.("");
  }

  function toggle(value: string) {
    onChange(checkedSet.has(value) ? checked.filter((v) => v !== value) : [...checked, value]);
  }

  // The summary reads from the checked set — the default while untouched, the selection after —
  // so an untouched all-default is "All <noun>" even while options load, and a default subset
  // names itself like a pick would.
  const summary = (() => {
    const allNoun = noun ? `All ${noun}` : "All";
    if (checked.length === 0) return "None";
    if (options.length > 0 && checked.length === options.length) return allNoun;
    const labels = checked.map((v) => options.find((o) => o.value === v)?.label ?? v);
    if (checked.length <= 2) return labels.join(", ");
    return `${checked.length} selected`;
  })();

  return (
    <div className={[styles.root, className ?? ""].filter(Boolean).join(" ")} ref={rootRef}>
      <button
        id={id}
        type="button"
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-invalid={error ? true : undefined}
        className={[styles.trigger, error ? styles.triggerError : ""].filter(Boolean).join(" ")}
        onClick={() => (open ? closePanel() : setOpen(true))}
        onKeyDown={(e) => {
          if (e.key === "Escape" && open) {
            // preventDefault keeps a surrounding native <dialog> from treating this Escape as its
            // own cancel; the panel just closes.
            e.preventDefault();
            closePanel();
          }
        }}
      >
        {loading && checked.length === 0 ? "Loading…" : summary}
        <ChevronDown size={16} className={styles.chevron} aria-hidden="true" />
      </button>

      {open && (
        <div className={styles.panel} ref={panelRef} popover="manual" role="dialog">
          <div className={styles.header}>
            {/* "All" enumerates the loaded page, so a truncated page hides it — it could not mean
                everything. "None" is always exact. */}
            {!truncated && (
              <button type="button" className={styles.headerAction} onClick={() => onChange(allValues)}>
                All
              </button>
            )}
            <button type="button" className={styles.headerAction} onClick={() => onChange([])}>
              None
            </button>
          </div>
          {(options.length > SEARCH_THRESHOLD || truncated || term !== "") && (
            <input
              type="text"
              className={styles.search}
              placeholder="Filter…"
              value={term}
              autoFocus
              onChange={(e) => {
                setTerm(e.target.value);
                onSearchChange?.(e.target.value);
              }}
              onKeyDown={(e) => {
                if (e.key === "Escape") {
                  e.preventDefault();
                  closePanel();
                }
              }}
            />
          )}
          <div className={styles.list}>
            {shown.map((option) => (
              <label key={option.value} className={styles.option}>
                <input
                  type="checkbox"
                  checked={checkedSet.has(option.value)}
                  onChange={() => toggle(option.value)}
                />
                <span>{option.label}</span>
              </label>
            ))}
            {shown.length === 0 && (
              <div className={styles.note}>{loading ? "Loading…" : "No matches."}</div>
            )}
            {truncated && (
              <div className={styles.note}>Showing the first matches — keep typing to narrow.</div>
            )}
          </div>
        </div>
      )}

      {error && (
        <p className={styles.errorText} role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
