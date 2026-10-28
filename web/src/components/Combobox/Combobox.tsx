/** Searchable picker: a text input filtering an option list, with a top-layer listbox (ARIA
 *  combobox pattern) so no dialog or scroll container clips it. Filtering is client-side over the
 *  given page; when the page is truncated at its cap the caller narrows it server-side from
 *  onSearchChange, and the open list shows a keep-typing hint. With allowNew, the filter text
 *  itself becomes a selectable trailing entry — pick to reuse an option, keep typing to create. */

import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { showAnchoredPopover } from "../../utils/anchoredPopover";
import { ChevronDown } from "../icons";
import styles from "./Combobox.module.css";

export interface ComboboxOption {
  value: string;
  label: string;
}

interface ComboboxProps {
  id?: string;
  /** Extra class for the root element (e.g. a width constraint from the surrounding form). */
  className?: string;
  /** The bound value; "" = unset. */
  value: string;
  options: ComboboxOption[];
  /** The option page stopped at its cap — filtering alone cannot reach every row. */
  truncated?: boolean;
  loading?: boolean;
  error?: string;
  /** Offers a clear entry so an optional value can go back to unset. */
  clearable?: boolean;
  /** Accepts the typed text as a value of its own when it matches no option's label — the list
   *  grows a trailing "Add …" entry. The bound value is the raw text, so only text-valued
   *  options make sense with it. */
  allowNew?: boolean;
  placeholder?: string;
  onChange: (value: string) => void;
  /** Reports the raw filter text so the caller can narrow the page server-side (debounced there). */
  onSearchChange?: (term: string) => void;
}

export function Combobox({
  id,
  className,
  value,
  options,
  truncated,
  loading,
  error,
  clearable,
  allowNew,
  placeholder,
  onChange,
  onSearchChange,
}: ComboboxProps) {
  const [open, setOpen] = useState(false);
  const [term, setTerm] = useState("");
  const [highlighted, setHighlighted] = useState(0);
  // The current selection's label, kept locally: a narrowed page may no longer contain the
  // selected option, and the closed input must still display it.
  const [selectedLabel, setSelectedLabel] = useState("");
  const rootRef = useRef<HTMLDivElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const listId = useId();

  // An allowNew value may match no option (it was typed, not picked); the raw value is its label.
  // An empty value always displays empty — the stored selectedLabel would otherwise linger after
  // the surrounding form clears the field (e.g. a quick-add resetting on success).
  const displayLabel =
    value === ""
      ? ""
      : (options.find((o) => o.value === value)?.label ??
        (selectedLabel !== "" ? selectedLabel : allowNew ? value : ""));

  const lowered = term.trim().toLowerCase();
  const items: ComboboxOption[] = [
    ...(clearable && value !== "" ? [{ value: "", label: "—" }] : []),
    ...(lowered === "" ? options : options.filter((o) => o.label.toLowerCase().includes(lowered))),
  ];
  // The typed text becomes a trailing "Add …" entry once it matches no option's label exactly —
  // an exact match means the thing already exists, and picking it is the same submission.
  const createTerm =
    allowNew && lowered !== "" && !options.some((o) => o.label.toLowerCase() === lowered)
      ? term.trim()
      : null;
  const itemCount = items.length + (createTerm !== null ? 1 : 0);

  useLayoutEffect(() => {
    const root = rootRef.current;
    const list = listRef.current;
    if (!open || !root || !list) return;
    showAnchoredPopover(root, list, "left");
    list.style.minWidth = `${root.getBoundingClientRect().width}px`;
  }, [open, items.length, truncated]);

  useEffect(() => {
    if (!open) return;

    function onPointerDown(e: PointerEvent) {
      // The popover list stays a DOM child of the root, so this covers clicks inside it too.
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) {
        dismiss();
      }
    }
    // The list sits at viewport coordinates; an outside scroll would leave it floating detached.
    // Scrolling inside the list itself (a long option page) must not close it.
    function onScroll(e: Event) {
      if (listRef.current && listRef.current.contains(e.target as Node)) {
        return;
      }
      dismiss();
    }
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("scroll", onScroll, true);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("scroll", onScroll, true);
    };
  });

  function openList() {
    // An allowNew value is text the user owns — reopening resumes editing it in place. A picked
    // option's label would only filter the list down to itself, so pick-only lookups reopen
    // cleared with the current pick shown as the placeholder.
    const seed = allowNew ? displayLabel : "";
    setTerm(seed);
    setHighlighted(0);
    setOpen(true);
    onSearchChange?.(seed);
  }

  function closeList() {
    setOpen(false);
    setTerm("");
    onSearchChange?.("");
  }

  // Dismissal (outside click, scroll, Tab) keeps a typed allowNew value — visible input must not
  // silently vanish. Escape stays an explicit discard via closeList.
  function dismiss() {
    if (allowNew && term.trim() !== "") {
      select({ value: term.trim(), label: term.trim() });
      return;
    }
    closeList();
  }

  function select(option: ComboboxOption) {
    setSelectedLabel(option.label);
    onChange(option.value);
    closeList();
  }

  function moveHighlight(delta: number) {
    setHighlighted((prev) => {
      const next = Math.max(0, Math.min(itemCount - 1, prev + delta));
      document.getElementById(optionId(next))?.scrollIntoView({ block: "nearest" });
      return next;
    });
  }

  function optionId(index: number) {
    return `${listId}-opt-${index}`;
  }

  function handleKeyDown(e: React.KeyboardEvent) {
    if (!open) {
      // Enter on a closed empty picker opens it instead of submitting an enclosing form; once a
      // value is held, Enter falls through and submits like any other field — ArrowDown reopens.
      if (e.key === "ArrowDown" || (e.key === "Enter" && value === "")) {
        e.preventDefault();
        openList();
      }
      return;
    }
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        moveHighlight(1);
        break;
      case "ArrowUp":
        e.preventDefault();
        moveHighlight(-1);
        break;
      case "Enter":
        e.preventDefault();
        if (items[highlighted]) {
          select(items[highlighted]);
        } else if (createTerm !== null && highlighted === items.length) {
          select({ value: createTerm, label: createTerm });
        }
        break;
      case "Escape":
        // preventDefault keeps a surrounding native <dialog> from treating this Escape as its
        // own cancel; the picker just closes.
        e.preventDefault();
        closeList();
        break;
      case "Tab":
        dismiss();
        break;
    }
  }

  return (
    <div className={[styles.root, className ?? ""].filter(Boolean).join(" ")} ref={rootRef}>
      <div className={styles.control}>
        <input
          id={id}
          type="text"
          role="combobox"
          aria-expanded={open}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={open && highlighted < itemCount ? optionId(highlighted) : undefined}
          aria-invalid={error ? true : undefined}
          className={[styles.input, error ? styles.inputError : ""].filter(Boolean).join(" ")}
          value={open ? term : displayLabel}
          placeholder={open && displayLabel !== "" ? displayLabel : (placeholder ?? "Pick…")}
          autoComplete="off"
          onClick={() => {
            if (!open) {
              openList();
            }
          }}
          onChange={(e) => {
            if (!open) {
              setOpen(true);
            }
            setTerm(e.target.value);
            setHighlighted(0);
            onSearchChange?.(e.target.value);
          }}
          onKeyDown={handleKeyDown}
        />
        <ChevronDown size={16} className={styles.chevron} aria-hidden="true" />
      </div>

      {open && (
        <div className={styles.list} ref={listRef} popover="manual" role="listbox" id={listId}>
          {items.map((option, i) => (
            <button
              key={option.value === "" ? " clear" : option.value}
              id={optionId(i)}
              type="button"
              role="option"
              aria-selected={option.value === value}
              className={[
                styles.option,
                i === highlighted ? styles.highlighted : "",
                option.value === value ? styles.selected : "",
              ]
                .filter(Boolean)
                .join(" ")}
              // Keeps focus on the input so selection happens before any blur handling.
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => select(option)}
              onMouseEnter={() => setHighlighted(i)}
            >
              {option.label}
            </button>
          ))}
          {createTerm !== null && (
            <button
              id={optionId(items.length)}
              type="button"
              role="option"
              aria-selected={false}
              className={[styles.option, highlighted === items.length ? styles.highlighted : ""]
                .filter(Boolean)
                .join(" ")}
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => select({ value: createTerm, label: createTerm })}
              onMouseEnter={() => setHighlighted(items.length)}
            >
              Add “{createTerm}”
            </button>
          )}
          {itemCount === 0 && (
            <div className={styles.note}>{loading ? "Loading…" : "No matches."}</div>
          )}
          {truncated && (
            <div className={styles.note}>Showing the first matches — keep typing to narrow.</div>
          )}
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
