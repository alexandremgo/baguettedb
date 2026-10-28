/** Helpers for classifying and running action formulas. */

import type { Formula, FormulaRunResult } from "../../api/formulas";

/** A short verb for an action button, from the server-derived statement verb. */
export function actionVerb(formula: Formula): string {
  switch (formula.statement_verb) {
    case "insert":
      return "Add";
    case "update":
      return "Update";
    case "delete":
      return "Delete";
    default:
      return "Run";
  }
}

/** Whether an action warrants a confirm step. Only irreversible row deletion does; an INSERT is
 *  additive and an UPDATE is correctable, so those run directly. The verb is derived server-side
 *  from the prepared statement, so comment- and CTE-led DELETEs are classified correctly. */
export function isDestructive(formula: Formula): boolean {
  return formula.statement_verb === "delete";
}

/** A formula's user-supplied params (prompt/lookup) — everything the caller types or picks. The
 *  selection (:rowid / :rowids) and pagination params are bound separately, not entered in a form. */
export function promptParamsOf(formula: Formula) {
  return formula.params.filter((p) => p.source === "prompt" || p.source === "lookup");
}

/** The success line for an action run. A single-statement run reports its row count — exactly the
 *  confirmation wanted for a delete or a bulk update. A multi-statement run's count sums bookkeeping
 *  rows across its statements ("5 rows" for one applied bill), so the line reads Done and the
 *  detailed total moves to `detail`, rendered as a hover tooltip. */
export function runSuccessMessage(result: FormulaRunResult): { text: string; detail?: string } {
  const affected = result.affected_row_count;
  const rows = `${affected} row${affected === 1 ? "" : "s"}`;
  if ((result.statement_count ?? 1) <= 1) {
    return { text: `${rows} affected` };
  }
  return {
    text: "Done",
    detail: `${rows} across ${result.statement_count} statements — one undo step`,
  };
}
