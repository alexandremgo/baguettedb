/** Always-visible quick-add form for a view's add slot: the add action's inline-marked params
 *  render as compact labelled controls in a wrapping row, with an Add button. Submitting runs the
 *  action and clears the form in place; the action's unmarked optional params are omitted, so they
 *  bind SQL NULL and the SQL's defaults apply. */

import { useState } from "react";
import type { Formula, FormulaParam } from "../../api/formulas";
import { useRunFormula } from "../../hooks/useFormulas";
import { Button } from "../Button";
import { Ellipsis, Plus } from "../icons";
import { promptParamsOf } from "./actions";
import { formulaTitle } from "./selectors";
import { FieldControl } from "./FormulaParamForm";
import {
  buildRunParams,
  defaultFields,
  emptyParamField,
  missingVisibleRequired,
  scopeRawValues,
  visibleParams,
  type ParamFieldValue,
} from "./params";
import styles from "./QuickAddForm.module.css";

interface QuickAddFormProps {
  bdbId: string;
  /** The add-slot action the form runs. */
  action: Formula;
  /** The action's inline-marked params, in declaration order (from quickAddParams). */
  params: FormulaParam[];
  /** App-bound selection params the run must carry (:parent_rowid inside a detail modal). */
  selection: Record<string, unknown>;
  /** The view's shared run mutation, so errors surface in the view's usual spot. */
  run: ReturnType<typeof useRunFormula>;
  /** Opens the full param dialog, seeded with the values already typed here so nothing is lost on
   *  the way. The button only renders when the action has params beyond the inline-marked ones. */
  onOpenDialog: (typed: Record<string, unknown>) => void;
}

export function QuickAddForm({ bdbId, action, params, selection, run, onOpenDialog }: QuickAddFormProps) {
  // The action's full param list backs condition evaluation: an inline field may gate on a
  // controller the compact bar doesn't render, whose default still decides the condition.
  const allParams = promptParamsOf(action);
  // An untouched form holds the params' declared defaults — exactly what an omitting run binds.
  const [fields, setFields] = useState<Record<string, ParamFieldValue>>(() => defaultFields(allParams));

  // Conditional params render — and count toward the form's shape — only while their condition
  // holds against the values typed so far.
  const shown = visibleParams(params, fields, allParams);

  // A textarea can't share a row: the form stacks, the entry takes the full width, and the buttons
  // drop to their own line beneath it.
  const hasMultiline = shown.some((p) => p.type === "text" && p.multiline);

  const missingRequired = missingVisibleRequired(allParams, fields);

  // Clears the form whenever its action succeeds — from the form itself or from the seeded dialog
  // the "⋯" button hands off to — so a stale draft never invites a duplicate add. The shared run
  // mutation carries the ref, so other actions' successes (toggles, edits) leave the draft alone.
  // Compared during render (not in an effect): remembering the success's submittedAt makes the
  // clear fire exactly once per run.
  const [clearedAt, setClearedAt] = useState(0);
  if (run.isSuccess && run.variables?.ref === action.id && run.submittedAt !== clearedAt) {
    setClearedAt(run.submittedAt);
    setFields(defaultFields(allParams));
  }

  function submit() {
    if (missingRequired) return;
    run.mutate({
      ref: action.id,
      data: { params: { ...selection, ...buildRunParams(params, fields, allParams) } },
    });
  }

  // Hands the dialog whatever the bar holds, keyed by param name — the inline fields as typed, plus
  // any dialog-only field a lookup pick filled (the state carries every param, the bar renders the
  // inline ones); blank and condition-hidden fields are left out so the dialog's own empty state
  // applies.
  function openDialogSeeded() {
    const typed: Record<string, unknown> = {};
    for (const p of visibleParams(allParams, fields)) {
      const raw = fields[p.name]?.raw ?? "";
      if (raw.trim() !== "") typed[p.name] = raw;
    }
    onOpenDialog(typed);
  }

  const hasDialogButton = promptParamsOf(action).length > params.length;

  // The canonical single-text quick-add renders as one pill: the action's own title is the
  // placeholder ("Add task…"), so no label repeats it, and submit is the + inside the pill.
  const pillParam =
    !hasMultiline &&
    shown.length === 1 &&
    shown[0].type === "text" &&
    !shown[0].options &&
    shown[0].source !== "lookup"
      ? shown[0]
      : null;

  if (pillParam) {
    return (
      <form
        className={styles.pill}
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <input
          type="text"
          className={styles.pillInput}
          placeholder={`${formulaTitle(action)}…`}
          aria-label={pillParam.name}
          value={fields[pillParam.name]?.raw ?? ""}
          onChange={(e) =>
            setFields((f) => ({
              ...f,
              [pillParam.name]: { ...(f[pillParam.name] ?? emptyParamField), raw: e.target.value },
            }))
          }
        />
        {hasDialogButton && (
          <button
            type="button"
            className={styles.pillGhost}
            aria-label="Add with all fields"
            title="Add with all fields"
            onClick={openDialogSeeded}
          >
            <Ellipsis size={16} aria-hidden="true" />
          </button>
        )}
        <button
          type="submit"
          className={styles.pillSubmit}
          disabled={missingRequired || run.isPending}
          aria-label={formulaTitle(action)}
          title={formulaTitle(action)}
        >
          <Plus size={18} aria-hidden="true" />
        </button>
      </form>
    );
  }

  return (
    <form
      className={[styles.form, hasMultiline ? styles.formStacked : ""].join(" ").trim()}
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      {shown.map((param) => (
        <div
          key={param.name}
          className={[
            styles.field,
            (param.type === "text" && !param.options) || param.source === "lookup" ? styles.fieldGrow : "",
          ]
            .join(" ")
            .trim()}
          // The compact bar shows no hint text; for a checkbox — whose meaning is otherwise only its
          // slug — hovering reveals the name and the authored hint.
          title={param.type === "boolean" && param.hint ? `${param.name}: ${param.hint}` : undefined}
        >
          <span className={styles.label}>
            {param.name}
            {param.optional && <span className={styles.optional}> optional</span>}
          </span>
          <FieldControl
            bdbId={bdbId}
            param={param}
            value={fields[param.name]?.raw ?? ""}
            values={fields[param.name]?.values}
            hideBlankOption={param.default != null && param.options != null}
            scopeValues={scopeRawValues(param, allParams, fields)}
            rows={3}
            fullWidthLookup
            onChange={(raw) =>
              setFields((f) => ({ ...f, [param.name]: { ...(f[param.name] ?? emptyParamField), raw } }))
            }
            onValuesChange={(values) =>
              setFields((f) => ({ ...f, [param.name]: { ...(f[param.name] ?? emptyParamField), values } }))
            }
            // A pick's fills may land on dialog-only siblings: the bar's submit still sends inline
            // fields only, and the seeded dialog shows what was filled.
            onFill={(fills) =>
              setFields((f) => {
                const next = { ...f };
                for (const [sibling, raw] of Object.entries(fills)) {
                  if (allParams.some((p) => p.name === sibling)) {
                    next[sibling] = { ...(f[sibling] ?? emptyParamField), raw };
                  }
                }
                return next;
              })
            }
          />
        </div>
      ))}
      <div className={styles.actions}>
        <Button type="submit" disabled={missingRequired} loading={run.isPending}>
          Add
        </Button>
        {hasDialogButton && (
          <Button
            variant="secondary"
            type="button"
            aria-label="Add with all fields"
            title="Add with all fields"
            onClick={openDialogSeeded}
          >
            <Ellipsis size={16} aria-hidden="true" />
          </Button>
        )}
      </div>
    </form>
  );
}
