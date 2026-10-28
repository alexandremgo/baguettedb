/** Standalone Formulas list — the primary screen: each top-level formula as a tappable row
 *  (name + description). Tapping a row opens that formula's own screen. Hidden (assistant-only)
 *  formulas are omitted by default; writers get a "Show hidden" toggle. */

import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { bdbCanWrite } from "../api/bdbs";
import { EmptyState } from "../components/EmptyState";
import { ChevronRight, SquareKanban } from "../components/icons";
import { formulaTitle, topLevelFormulas } from "../components/Formula/selectors";
import { useBdb } from "../hooks/useBdb";
import { useFormulas } from "../hooks/useFormulas";
import { SkeletonRows } from "../components/Skeleton";
import styles from "./Standalone.module.css";

export function FormulasScreen() {
  const { bdbId } = useParams<{ bdbId: string }>();
  const { data: formulas = [], isLoading, error } = useFormulas(bdbId);
  const { data: bdb } = useBdb(bdbId);
  const [showHidden, setShowHidden] = useState(false);

  if (!bdbId) return null;
  if (isLoading) return <SkeletonRows />;
  if (error) return <p className={styles.error}>Couldn't load formulas.</p>;

  const canWrite = bdb ? bdbCanWrite(bdb) : false;
  const topLevel = topLevelFormulas(formulas);
  const hiddenCount = topLevel.filter((f) => f.hidden).length;
  const visible = showHidden ? topLevel : topLevel.filter((f) => !f.hidden);

  if (topLevel.length === 0) {
    return (
      <EmptyState
        icon={SquareKanban}
        title="No formulas yet."
        hint="Ask your assistant to create one for this bdb."
      />
    );
  }

  return (
    <>
      <ul className={styles.list}>
        {visible.map((formula) => (
          <li key={formula.id}>
            <Link to={`/app/bdbs/${bdbId}/formulas/${formula.id}`} className={styles.item}>
              <span className={styles.itemBody}>
                <span className={styles.itemTitle}>{formulaTitle(formula)}</span>
                {formula.description && (
                  <span className={styles.itemDesc}>{formula.description}</span>
                )}
              </span>
              {formula.hidden && <span className={styles.badge}>hidden</span>}
              <ChevronRight size={16} className={styles.itemChevron} aria-hidden="true" />
            </Link>
          </li>
        ))}
      </ul>
      {canWrite && hiddenCount > 0 && (
        <label className={styles.showHidden}>
          <input
            type="checkbox"
            checked={showHidden}
            onChange={(e) => setShowHidden(e.target.checked)}
          />
          Show hidden ({hiddenCount})
        </label>
      )}
    </>
  );
}
