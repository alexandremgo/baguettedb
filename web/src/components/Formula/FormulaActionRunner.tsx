/** Runs a standalone action formula: a param mini-form, a confirm step for destructive statements
 *  (UPDATE/DELETE), and the affected-row-count result. */

import { useState } from "react";
import type { Formula } from "../../api/formulas";
import { Button } from "../Button";
import { Dialog } from "../Dialog";
import { useRunFormula } from "../../hooks/useFormulas";
import { actionVerb, isDestructive, runSuccessMessage } from "./actions";
import { formulaTitle } from "./selectors";
import { FormulaParamForm } from "./FormulaParamForm";
import { buildRunParams, defaultFields, defaultedSelects, type ParamFieldValue } from "./params";
import styles from "./FormulaActionRunner.module.css";

interface FormulaActionRunnerProps {
  bdbId: string;
  formula: Formula;
  /** Whether the current user may run actions in this bdb (editor/owner); null while still unknown. */
  canWrite: boolean | null;
}

export function FormulaActionRunner({ bdbId, formula, canWrite }: FormulaActionRunnerProps) {
  const promptParams = formula.params.filter((p) => p.source === "prompt" || p.source === "lookup");
  const destructive = isDestructive(formula);
  const verb = actionVerb(formula);

  // An untouched form holds the params' declared defaults — exactly what an omitting run binds.
  const [fields, setFields] = useState<Record<string, ParamFieldValue>>(() => defaultFields(promptParams));
  const [confirmOpen, setConfirmOpen] = useState(false);
  const run = useRunFormula(bdbId);

  function submit() {
    run.mutate(
      { ref: formula.id, data: { params: buildRunParams(promptParams, fields) } },
      { onSuccess: () => setConfirmOpen(false) },
    );
  }

  function handleClick() {
    if (destructive) {
      // Clears a previous run's error so the dialog opens clean.
      run.reset();
      setConfirmOpen(true);
    } else {
      submit();
    }
  }

  const errorMessage = run.error instanceof Error ? run.error.message : null;
  const success = run.isSuccess && run.data ? runSuccessMessage(run.data) : null;

  if (canWrite === null) {
    return null;
  }
  if (!canWrite) {
    return <p className={styles.readOnly}>You need editor access to run this action.</p>;
  }

  return (
    <div className={styles.runner}>
      {promptParams.length > 0 && (
        <FormulaParamForm
          bdbId={bdbId}
          params={promptParams}
          values={fields}
          seededSelects={defaultedSelects(promptParams)}
          onChange={(name, value) => setFields((prev) => ({ ...prev, [name]: value }))}
        />
      )}

      <Button
        size="sm"
        className={styles.runButton}
        variant={destructive ? "destructive" : "primary"}
        onClick={handleClick}
        loading={run.isPending && !confirmOpen}
      >
        {verb}
      </Button>

      {errorMessage && <div className={styles.error}>{errorMessage}</div>}
      {success && (
        <div className={styles.success} title={success.detail}>
          ✓ {success.text}
        </div>
      )}

      <Dialog open={confirmOpen} onClose={() => setConfirmOpen(false)} title={`${verb}?`}>
        <p className={styles.confirmText}>
          This runs “{formulaTitle(formula)}” and can’t be undone from here.
        </p>
        {errorMessage && <div className={styles.error}>{errorMessage}</div>}
        <div className={styles.confirmActions}>
          <Button size="sm" variant="secondary" onClick={() => setConfirmOpen(false)}>
            Cancel
          </Button>
          <Button size="sm" variant="destructive" onClick={submit} loading={run.isPending}>
            {verb}
          </Button>
        </div>
      </Dialog>
    </div>
  );
}
