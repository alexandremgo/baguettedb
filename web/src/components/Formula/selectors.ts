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

/** Returns each formula's title by its ID, followed by its slug name when another formula of the list has the same
 *  title, so a list naming them all can tell them apart. */
export function distinctFormulaTitles(formulas: Formula[]): Map<string, string> {
  const counts = new Map<string, number>();
  for (const f of formulas) {
    counts.set(formulaTitle(f), (counts.get(formulaTitle(f)) ?? 0) + 1);
  }
  return new Map(
    formulas.map((f) => {
      const title = formulaTitle(f);
      return [f.id, (counts.get(title) ?? 0) > 1 ? `${title} (${f.name})` : title];
    }),
  );
}
