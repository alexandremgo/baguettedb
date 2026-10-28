/** Formulas viewer for a bdb: a master-detail list of top-level formulas; selecting one runs it.
 *  Hidden (assistant-only) formulas are omitted by default; members who can write formulas get a
 *  "Show hidden" toggle that lists them with a badge. */

import { useMemo, useState } from "react";
import { bdbCanWrite } from "../../api/bdbs";
import { Badge } from "../../components/Badge";
import { EmptyState } from "../../components/EmptyState";
import { SquareKanban } from "../../components/icons";
import { FormulaView } from "../../components/Formula";
import { formulaTitle, topLevelFormulas } from "../../components/Formula/selectors";
import { SkeletonRows } from "../../components/Skeleton";
import { useBdb } from "../../hooks/useBdb";
import { useFormulas } from "../../hooks/useFormulas";
import styles from "./FormulasTab.module.css";

interface FormulasTabProps {
  bdbId: string;
}

export function FormulasTab({ bdbId }: FormulasTabProps) {
  const { data: formulas, isLoading, error } = useFormulas(bdbId);
  const { data: bdb } = useBdb(bdbId);
  const canWrite = bdb ? bdbCanWrite(bdb) : false;
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [showHidden, setShowHidden] = useState(false);

  const topLevel = useMemo(() => topLevelFormulas(formulas ?? []), [formulas]);
  // The server already omits hidden formulas for viewers; the toggle only matters for writers.
  const hiddenCount = topLevel.filter((f) => f.hidden).length;
  const visible = showHidden ? topLevel : topLevel.filter((f) => !f.hidden);
  const selected = visible.find((f) => f.id === selectedId) ?? visible[0] ?? null;

  if (isLoading) {
    return <SkeletonRows />;
  }

  if (error) {
    return (
      <p className={styles.error}>
        {error instanceof Error ? error.message : "Failed to load formulas"}
      </p>
    );
  }

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
    <div className={styles.layout}>
      <div className={styles.list}>
        {visible.map((formula) => (
          <button
            key={formula.id}
            className={[styles.item, formula.id === selected?.id ? styles.itemActive : ""].join(" ")}
            onClick={() => setSelectedId(formula.id)}
          >
            <span className={styles.itemName}>{formulaTitle(formula)}</span>
            {formula.hidden && <Badge variant="viewer">hidden</Badge>}
            <Badge variant={formula.kind === "view" ? "default" : "editor"}>{formula.kind}</Badge>
          </button>
        ))}
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
      </div>

      <div className={styles.detail}>
        {selected && <FormulaView key={selected.id} bdbId={bdbId} formula={selected} />}
      </div>
    </div>
  );
}
