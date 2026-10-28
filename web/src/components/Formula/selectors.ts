/** Selectors over a bdb's formula list. */

import type { Formula } from "../../api/formulas";

/** Returns the top-level formulas (not attached to a view), sorted by display order then name. */
export function topLevelFormulas(formulas: Formula[]): Formula[] {
  return formulas
    .filter((f) => !f.attached_to_formula_id)
    .sort((a, b) => a.display_order - b.display_order || a.name.localeCompare(b.name));
}


/** Returns what the app shows for a formula: its human label, falling back to the slug name. */
export function formulaTitle(formula: Formula): string {
  return formula.label || formula.name;
}
