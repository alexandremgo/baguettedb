/** Mini-form for a formula's user-supplied parameters: a type-aware input per param — text/number
 *  field, checkbox for booleans, searchable combobox for lookups. Optional params may be left
 *  empty. */

import { useState } from "react";
import { Combobox } from "../Combobox";
import { Input } from "../Input";
import { MultiSelect } from "../MultiSelect";
import { Select } from "../Select";
import { Switch } from "../Switch";
import type { FormulaParam } from "../../api/formulas";
import { useDebouncedValue } from "../../hooks/useDebouncedValue";
import { useLookupOptions } from "../../hooks/useFormulas";
import {
  emptyParamField,
  multiDefaultValues,
  scopeRawValues,
  visibleParams,
  type ParamFieldValue,
} from "./params";
import styles from "./FormulaParamForm.module.css";

interface FormulaParamFormProps {
  bdbId: string;
  /** The formula's user-supplied parameters (selection / pagination params are handled elsewhere). */
  params: FormulaParam[];
  values: Record<string, ParamFieldValue>;
  /** Option-select params that were seeded with a valid option; their select drops its blank
   *  entry, since a blank there could only mean "unchanged" — which the seed already expresses. */
  seededSelects?: Set<string>;
  /** The row the dialog acts on, when opened from one; lookups flagged exclude_selection drop it
   *  from their options. */
  selectionRowId?: number | null;
  /** Renders boolean params as a toggle switch instead of a checkbox — for filter bars, where a
   *  boolean is a mode; forms that submit a value keep the checkbox default. */
  booleanControl?: "checkbox" | "switch";
  onChange: (name: string, value: ParamFieldValue) => void;
}

export function FormulaParamForm({
  bdbId,
  params,
  values,
  seededSelects,
  selectionRowId,
  booleanControl,
  onChange,
}: FormulaParamFormProps) {
  return (
    <div className={styles.form}>
      {visibleParams(params, values).map((param) => (
        <ParamField
          key={param.name}
          bdbId={bdbId}
          param={param}
          field={values[param.name] ?? emptyParamField}
          hideBlankOption={seededSelects?.has(param.name) ?? false}
          selectionRowId={selectionRowId}
          booleanControl={booleanControl}
          scopeValues={scopeRawValues(param, params, values)}
          onChange={(value) => onChange(param.name, value)}
          // A pick's fills land on the siblings as their new raw values, one change per sibling;
          // parents merge changes functionally, so several in one tick compose.
          onFill={(fills) => {
            for (const [sibling, raw] of Object.entries(fills)) {
              if (params.some((p) => p.name === sibling)) {
                onChange(sibling, { ...(values[sibling] ?? emptyParamField), raw });
              }
            }
          }}
        />
      ))}
    </div>
  );
}

interface ParamFieldProps {
  bdbId: string;
  param: FormulaParam;
  field: ParamFieldValue;
  hideBlankOption: boolean;
  selectionRowId?: number | null;
  booleanControl?: "checkbox" | "switch";
  scopeValues?: Record<string, string>;
  onChange: (value: ParamFieldValue) => void;
  /** Receives a lookup pick's fill values, keyed by sibling param name. */
  onFill?: (fills: Record<string, string>) => void;
}

function ParamField({
  bdbId,
  param,
  field,
  hideBlankOption,
  selectionRowId,
  booleanControl,
  scopeValues,
  onChange,
  onFill,
}: ParamFieldProps) {
  const inputId = `param-${param.name}`;

  // A checkbox is complete with its text beside it: one clickable full-width row, with the hint as
  // the label — it says what checking means. The slug-shaped name stays visible but subdued: it is
  // the param's reference (how an assistant addresses it), not its explanation.
  if (param.type === "boolean" && param.source !== "lookup") {
    return (
      <div className={styles.field}>
        <label className={styles.checkboxRow}>
          <FieldControl
            bdbId={bdbId}
            param={param}
            id={inputId}
            value={field.raw}
            booleanControl={booleanControl}
            onChange={(raw) => onChange({ ...field, raw })}
          />
          <span>{param.hint || param.name}</span>
          {param.hint && <span className={styles.type}>{param.name}</span>}
        </label>
      </div>
    );
  }

  return (
    <div className={styles.field}>
      <div className={styles.labelRow}>
        <label className={styles.label} htmlFor={param.type === "group" ? undefined : inputId}>
          {param.name}
          <span className={styles.type}>{param.type}</span>
          {param.optional && <span className={styles.optional}>optional</span>}
        </label>
        {param.hint && <span className={styles.hint}>{param.hint}</span>}
      </div>

      {param.type === "group" ? (
        <GroupField
          bdbId={bdbId}
          param={param}
          field={field}
          selectionRowId={selectionRowId}
          onChange={onChange}
        />
      ) : (
        <FieldControl
          bdbId={bdbId}
          param={param}
          id={inputId}
          value={field.raw}
          values={field.values}
          hideBlankOption={hideBlankOption}
          selectionRowId={selectionRowId}
          scopeValues={scopeValues}
          onChange={(raw) => onChange({ ...field, raw })}
          onValuesChange={(values) => onChange({ ...field, values })}
          onFill={onFill}
        />
      )}
    </div>
  );
}

interface FieldControlProps {
  bdbId: string;
  /** A scalar prompt or lookup parameter — a top-level param or one group field. */
  param: FormulaParam;
  id?: string;
  value: string;
  /** A multi-select lookup's checked values; undefined = untouched (the control shows its default
   *  state and the run omits the param). Ignored for every other param. */
  values?: string[];
  /** Omits an option-select's blank entry — for fields seeded with a current value, where the
   *  blank would be a redundant "unchanged". */
  hideBlankOption?: boolean;
  /** The row the dialog acts on; a lookup flagged exclude_selection drops it from its options. */
  selectionRowId?: number | null;
  /** Height of a multiline text param's textarea; dialogs keep the roomy default, compact
   *  always-visible forms pass a smaller one. */
  rows?: number;
  /** Switch rendering for boolean params (filter-bar modes); checkbox is the default. */
  booleanControl?: "checkbox" | "switch";
  /** Lets a lookup combobox fill its container instead of stopping at the 360px comfort cap — for
   *  bars whose field wrapper already sizes the row. */
  fullWidthLookup?: boolean;
  /** The current values of the lookup's scope siblings (sibling param name → normalized raw), from
   *  scopeRawValues; each entry narrows the options query, and changing one re-fetches the page. */
  scopeValues?: Record<string, string>;
  onChange: (raw: string) => void;
  /** Receives a multi-select's checked values; required whenever the param is multiple. */
  onValuesChange?: (values: string[]) => void;
  /** Receives the picked option's fill values (sibling param name → raw) when the lookup declares
   *  a fill and the new value matches an option — a typed-past value or a clear reports nothing. */
  onFill?: (fills: Record<string, string>) => void;
}

// lookupNoun makes a lookup's table identifier readable as the multi-select's summary noun
// ("splitting_members" -> "All splitting members"), which snake_case would otherwise reach verbatim.
// Table names are already plural by convention, so undoing the underscores is the whole job.
function lookupNoun(table: string | undefined): string | undefined {
  return table?.replace(/_/g, " ");
}

// FieldControl renders one scalar value input: a searchable combobox for lookups (a checkbox panel
// when the param is multiple), a checkbox for booleans, a native date picker for dates, a
// text/number field otherwise.
export function FieldControl({
  bdbId,
  param,
  id,
  value,
  values,
  hideBlankOption,
  selectionRowId,
  scopeValues,
  rows = 8,
  booleanControl,
  fullWidthLookup,
  onChange,
  onValuesChange,
  onFill,
}: FieldControlProps) {
  const [term, setTerm] = useState("");
  const debouncedTerm = useDebouncedValue(term, 250).trim();
  const excludeValue =
    param.lookup?.exclude_selection && selectionRowId != null ? selectionRowId : undefined;
  // The unfiltered page decides the mode: under the cap the combobox filters it client-side and
  // the narrowed query stays disabled; at the cap, each debounced keystroke narrows server-side.
  const basePage = useLookupOptions(bdbId, param.lookup, "", excludeValue, scopeValues);
  const needsNarrow = (basePage.data?.truncated ?? false) && debouncedTerm !== "";
  const narrowed = useLookupOptions(
    bdbId,
    needsNarrow ? param.lookup : undefined,
    debouncedTerm,
    excludeValue,
    scopeValues,
  );
  const page = needsNarrow ? narrowed.data : basePage.data;

  const lookupError = basePage.error
    ? `Couldn't load options from “${param.lookup?.table}” — has its table or a column ` +
      `been renamed? Ask your AI assistant to update this formula.`
    : undefined;

  if (param.multiple) {
    // A lookup enumerates its table's page; an options param enumerates its declared vocabulary,
    // which never loads or truncates.
    const fromOptions = param.source !== "lookup";
    const multiOptions = fromOptions
      ? (param.options ?? []).map((o) => ({ value: o.value, label: o.label }))
      : (page?.options ?? []).map((o) => ({ value: String(o.value), label: o.label }));
    return (
      <MultiSelect
        id={id}
        values={values}
        defaultValues={multiDefaultValues(
          param,
          multiOptions.map((o) => o.value),
        )}
        options={multiOptions}
        noun={fromOptions ? param.name : lookupNoun(param.lookup?.table)}
        truncated={fromOptions ? false : page?.truncated}
        loading={fromOptions ? false : basePage.isLoading || narrowed.isFetching}
        error={fromOptions ? undefined : lookupError}
        onChange={(next) => onValuesChange?.(next)}
        onSearchChange={setTerm}
      />
    );
  }
  if (param.source === "lookup") {
    // ~42 chars ≈ the 360px cap at this font size; a longer label earns the full row so it never clips.
    const longestLabel = Math.max(0, ...(page?.options ?? []).map((o) => o.label.length));
    return (
      <Combobox
        id={id}
        className={fullWidthLookup || longestLabel > 42 ? undefined : styles.lookupCap}
        value={value}
        options={(page?.options ?? []).map((o) => ({
          value: String(o.value),
          label: o.label,
        }))}
        truncated={page?.truncated}
        loading={basePage.isLoading || narrowed.isFetching}
        clearable={param.optional}
        allowNew={param.lookup?.allow_new}
        placeholder={basePage.isLoading ? "Loading…" : param.lookup?.allow_new ? "Pick or type…" : "Pick…"}
        error={lookupError}
        onChange={(raw) => {
          onChange(raw);
          // The combobox reports picks and typed text alike; an option carrying the value is a
          // pick (typing an existing value exactly counts too, which is the same intent).
          const picked = page?.options.find((o) => String(o.value) === raw);
          if (raw !== "" && picked?.fill && onFill) onFill(picked.fill);
        }}
        onSearchChange={setTerm}
      />
    );
  }
  if (param.options && param.options.length > 0) {
    const options = param.options.map((option) => ({ value: option.value, label: option.label }));
    return (
      <Select
        id={id}
        className={styles.sizeSelect}
        value={value}
        aria-label={param.name}
        // A required param still opens on an unchoosable blank so no value is submitted unchosen; an
        // optional one keeps "any" as a real entry, so the choice stays clearable after picking.
        placeholder={!hideBlankOption && !param.optional ? "Pick…" : undefined}
        options={!hideBlankOption && param.optional ? [{ value: "", label: "any" }, ...options] : options}
        onChange={(e) => onChange(e.target.value)}
      />
    );
  }
  if (param.type === "boolean") {
    if (booleanControl === "switch") {
      return (
        <Switch
          id={id}
          checked={value === "true"}
          aria-label={param.name}
          onChange={(e) => onChange(e.target.checked ? "true" : "")}
        />
      );
    }
    return (
      <input
        id={id}
        type="checkbox"
        className={styles.checkbox}
        checked={value === "true"}
        aria-label={param.name}
        onChange={(e) => onChange(e.target.checked ? "true" : "")}
      />
    );
  }
  if (param.type === "text" && param.multiline) {
    return (
      <textarea
        id={id}
        className={styles.textarea}
        rows={rows}
        value={value}
        aria-label={param.name}
        placeholder={`${param.name}${param.optional ? " (optional)" : ""}`}
        onChange={(e) => onChange(e.target.value)}
      />
    );
  }
  return (
    <Input
      id={id}
      className={controlSizeClass(param.type)}
      // Dates and times get their native pickers; everything but text is a number field.
      type={inputTypeFor(param.type)}
      step={param.type === "float" ? "any" : undefined}
      value={value}
      aria-label={param.name}
      placeholder={`${param.name}${param.optional ? " (optional)" : ""}`}
      onChange={(e) => onChange(e.target.value)}
    />
  );
}

// inputTypeFor maps a param type to the HTML input type its control uses.
function inputTypeFor(type: string): "text" | "date" | "datetime-local" | "time" | "number" {
  if (type === "text") return "text";
  if (type === "date") return "date";
  if (type === "datetime") return "datetime-local";
  if (type === "time") return "time";
  return "number";
}

// controlSizeClass caps a control whose content has a knowable size; free text returns undefined
// and keeps the form's full width.
function controlSizeClass(type: string): string | undefined {
  if (type === "integer" || type === "float") return styles.sizeNumber;
  if (type === "date") return styles.sizeDate;
  if (type === "datetime") return styles.sizeDatetime;
  if (type === "time") return styles.sizeTime;
  return undefined;
}

// GroupField renders a repeated group's pending rows: one line of field controls per row with a
// remove button, plus an add-row button. Values live on the shared field state as `rows`.
function GroupField({ bdbId, param, field, selectionRowId, onChange }: Omit<ParamFieldProps, "hideBlankOption">) {
  const rows = field.rows ?? [];
  const groupFields = param.fields ?? [];

  const setRows = (next: Record<string, string>[]) => onChange({ ...field, rows: next });

  return (
    <div className={styles.groupRows}>
      {rows.map((row, i) => (
        <div key={i} className={styles.groupRow}>
          {groupFields.map((groupField) => (
            <FieldControl
              key={groupField.name}
              bdbId={bdbId}
              param={groupField}
              value={row[groupField.name] ?? ""}
              selectionRowId={selectionRowId}
              onChange={(raw) =>
                setRows(rows.map((r, j) => (j === i ? { ...r, [groupField.name]: raw } : r)))
              }
            />
          ))}
          <button
            type="button"
            className={styles.groupRemove}
            aria-label={`Remove ${param.name} row ${i + 1}`}
            onClick={() => setRows(rows.filter((_, j) => j !== i))}
          >
            &times;
          </button>
        </div>
      ))}
      <button
        type="button"
        className={styles.groupAdd}
        onClick={() => setRows([...rows, {}])}
      >
        + Add row
      </button>
    </div>
  );
}
