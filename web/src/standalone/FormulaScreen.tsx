/** Standalone single-formula screen — renders the formula's runner/results (reuses the shared
 *  FormulaView). Reached by tapping a row on the Formulas list; the header provides the back control. */

import { useParams } from "react-router-dom";
import { FormulaView } from "../components/Formula";
import { useFormulas } from "../hooks/useFormulas";
import { SkeletonRows } from "../components/Skeleton";
import styles from "./Standalone.module.css";

export function FormulaScreen() {
  const { bdbId, formulaId } = useParams<{ bdbId: string; formulaId: string }>();
  const { data: formulas = [], isLoading, error } = useFormulas(bdbId);

  if (!bdbId) return null;
  if (isLoading) return <SkeletonRows />;
  if (error) return <p className={styles.error}>Couldn't load this formula.</p>;

  const formula = formulas.find((f) => f.id === formulaId);
  if (!formula) {
    return <p className={styles.muted}>This formula no longer exists.</p>;
  }

  return <FormulaView key={formula.id} bdbId={bdbId} formula={formula} chrome="compact" />;
}
