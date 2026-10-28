/** Toolbar of a view's declared bulk actions (`display.slots.bulk`, in array order). Each button
 *  runs its action against the checked rows via :rowids and needs at least one row. An action
 *  carrying prompt/lookup params opens a dialog to collect them (merged with the selection) before
 *  running; a delete additionally confirms it can't be undone; a plain selection-only action runs
 *  straight from its button. Result is an affected-row-count. */

import { useState } from "react";
import type { Formula } from "../../api/formulas";
import { Button } from "../Button";
import { Dialog } from "../Dialog";
import { useRunFormula } from "../../hooks/useFormulas";
import { actionVerb, isDestructive, promptParamsOf, runSuccessMessage } from "./actions";
import { formulaTitle } from "./selectors";
import { FormulaParamForm } from "./FormulaParamForm";
import { buildRunParams, defaultFields, defaultedSelects, type ParamFieldValue } from "./params";
import styles from "./FormulaSelectionActions.module.css";

interface FormulaSelectionActionsProps {
  bdbId: string;
  actions: Formula[];
  selectedIds: number[];
  /** Called after an action succeeds so the parent can clear its selection (the view refetches). */
  onRan: () => void;
}

export function FormulaSelectionActions({
  bdbId,
  actions,
  selectedIds,
  onRan,
}: FormulaSelectionActionsProps) {
  // The action whose dialog is open — because it needs params and/or a delete confirm. null when idle.
  const [active, setActive] = useState<Formula | null>(null);
  const [fields, setFields] = useState<Record<string, ParamFieldValue>>({});
  const [lastResult, setLastResult] = useState<{ name: string; text: string; detail?: string } | null>(null);
  const run = useRunFormula(bdbId);

  function runAction(action: Formula) {
    setLastResult(null);
    const params = { rowids: selectedIds, ...buildRunParams(promptParamsOf(action), fields) };
    run.mutate(
      { ref: action.id, data: { params } },
      {
        onSuccess: (res) => {
          setLastResult({ name: formulaTitle(action), ...runSuccessMessage(res) });
          setActive(null);
          onRan();
        },
      },
    );
  }

  function handleClick(action: Formula) {
    // Clears a previous run's error so the dialog / next run starts clean.
    run.reset();
    if (promptParamsOf(action).length > 0 || isDestructive(action)) {
      // An untouched dialog holds the params' declared defaults — what an omitting run binds.
      setFields(defaultFields(promptParamsOf(action)));
      setActive(action);
    } else {
      runAction(action);
    }
  }

  const errorMessage = run.error instanceof Error ? run.error.message : null;
  const activeParams = active ? promptParamsOf(active) : [];
  const rowCount = selectedIds.length;

  return (
    <div className={styles.bar}>
      <div className={styles.buttons}>
        {actions.map((action) => {
          const disabled = selectedIds.length === 0;
          const label =
            selectedIds.length > 0
              ? `${formulaTitle(action)} (${selectedIds.length})`
              : formulaTitle(action);
          return (
            <Button
              key={action.id}
              size="sm"
              variant={isDestructive(action) ? "destructive" : "secondary"}
              disabled={disabled}
              loading={run.isPending && run.variables?.ref === action.id && active === null}
              onClick={() => handleClick(action)}
            >
              {label}
            </Button>
          );
        })}
      </div>

      {/* Errors from a direct (dialogless) run surface here; a dialog run shows its error inside. */}
      {errorMessage && active === null && <div className={styles.error}>{errorMessage}</div>}
      {lastResult && (
        <div className={styles.success} title={lastResult.detail}>
          ✓ {lastResult.name}: {lastResult.text}
        </div>
      )}

      <Dialog
        open={active !== null}
        onClose={() => setActive(null)}
        title={active ? active.name : ""}
      >
        {active && (
          <>
            <p className={styles.confirmText}>
              Runs “{active.name}” on {rowCount} selected row{rowCount === 1 ? "" : "s"}
              {isDestructive(active) ? " and can’t be undone." : "."}
            </p>

            {activeParams.length > 0 && (
              <div className={styles.formWrap}>
                <FormulaParamForm
                  bdbId={bdbId}
                  params={activeParams}
                  values={fields}
                  seededSelects={defaultedSelects(activeParams)}
                  onChange={(name, value) => setFields((prev) => ({ ...prev, [name]: value }))}
                />
              </div>
            )}

            {errorMessage && <div className={styles.error}>{errorMessage}</div>}

            <div className={styles.confirmActions}>
              <Button size="sm" variant="secondary" onClick={() => setActive(null)}>
                Cancel
              </Button>
              <Button
                size="sm"
                variant={isDestructive(active) ? "destructive" : "primary"}
                loading={run.isPending}
                onClick={() => runAction(active)}
              >
                {actionVerb(active)}
              </Button>
            </div>
          </>
        )}
      </Dialog>
    </div>
  );
}
