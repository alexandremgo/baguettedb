/** Formulas viewer for a bdb: a master-detail list of top-level formulas; selecting one runs it.
 *  Hidden (assistant-only) formulas are omitted by default; members who can write formulas get a
 *  "Show hidden" toggle that lists them with a badge. The owner also sees the Data API keys a
 *  formula is on, and can add it to one. */

import { useMemo, useState } from "react";
import { bdbCanWrite } from "../../api/bdbs";
import { Badge } from "../../components/Badge";
import { Button } from "../../components/Button";
import { Dialog } from "../../components/Dialog";
import { EmptyState } from "../../components/EmptyState";
import { KeyRound, SquareKanban } from "../../components/icons";
import { FormulaView } from "../../components/Formula";
import { formulaTitle, topLevelFormulas } from "../../components/Formula/selectors";
import { Select } from "../../components/Select";
import { SkeletonRows } from "../../components/Skeleton";
import { useToast } from "../../components/Toast";
import { useBdb } from "../../hooks/useBdb";
import { useAddDataAPIKeyFormula, useDataAPIKeys } from "../../hooks/useDataAPIKeys";
import { useFormulas } from "../../hooks/useFormulas";
import styles from "./FormulasTab.module.css";

interface FormulasTabProps {
  bdbId: string;
}

export function FormulasTab({ bdbId }: FormulasTabProps) {
  const { data: formulas, isLoading, error } = useFormulas(bdbId);
  const { data: bdb } = useBdb(bdbId);
  const canWrite = bdb ? bdbCanWrite(bdb) : false;
  const isOwner = bdb?.my_role === "owner" && !bdb.deleted_at;
  const { data: keys = [] } = useDataAPIKeys(isOwner ? bdbId : undefined);
  const addToKey = useAddDataAPIKeyFormula();
  const { toast } = useToast();
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [showHidden, setShowHidden] = useState(false);
  const [addToKeyOpen, setAddToKeyOpen] = useState(false);
  const [keyToAddTo, setKeyToAddTo] = useState("");

  const topLevel = useMemo(() => topLevelFormulas(formulas ?? []), [formulas]);
  // The server already omits hidden formulas for viewers; the toggle only matters for writers.
  const hiddenCount = topLevel.filter((f) => f.hidden).length;
  const visible = showHidden ? topLevel : topLevel.filter((f) => !f.hidden);
  const selected = visible.find((f) => f.id === selectedId) ?? visible[0] ?? null;
  const keysOn = selected ? keys.filter((k) => k.formula_ids.includes(selected.id)) : [];
  const keysOff = selected ? keys.filter((k) => !k.formula_ids.includes(selected.id)) : [];

  function openAddToKey() {
    setKeyToAddTo(keysOff[0]?.id ?? "");
    setAddToKeyOpen(true);
  }

  function handleAddToKey() {
    const key = keysOff.find((k) => k.id === keyToAddTo);
    if (!selected || !key) return;
    addToKey.mutate(
      { bdbId, keyId: key.id, formulaId: selected.id },
      {
        onSuccess: () => {
          toast(`${key.name} now runs ${formulaTitle(selected)}`, "success");
          setAddToKeyOpen(false);
        },
        onError: (err) => toast(err instanceof Error ? err.message : "Failed to add it to the key", "error"),
      },
    );
  }

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
        {selected && (
          <FormulaView
            key={selected.id}
            bdbId={bdbId}
            formula={selected}
            menuItems={isOwner ? [{ label: "Add to key…", onSelect: openAddToKey }] : []}
            annotation={
              keysOn.length > 0 && (
                <p className={styles.keys}>
                  <KeyRound size={14} aria-hidden="true" />
                  <span>
                    Apps run it with the Data API {keysOn.length === 1 ? "key" : "keys"}{" "}
                    {keysOn.map((k) => k.name).join(", ")}
                  </span>
                </p>
              )
            }
          />
        )}
      </div>

      <Dialog
        open={addToKeyOpen}
        onClose={() => setAddToKeyOpen(false)}
        title={`Add ${selected ? formulaTitle(selected) : ""} to a Data API key`}
      >
        <div className={styles.dialogBody}>
          {keys.length === 0 ? (
            <p className={styles.hint}>No Data API keys yet: create one in the Access tab.</p>
          ) : keysOff.length === 0 ? (
            <p className={styles.hint}>Every Data API key of this bdb already runs it.</p>
          ) : (
            <>
              <Select
                label="Key"
                options={keysOff.map((k) => ({ value: k.id, label: k.name }))}
                value={keyToAddTo}
                onChange={(e) => setKeyToAddTo(e.target.value)}
              />
              <p className={styles.hint}>The app using the key can run it from its next call.</p>
            </>
          )}
          <div className={styles.actions}>
            <Button variant="ghost" onClick={() => setAddToKeyOpen(false)}>
              {keysOff.length === 0 ? "Close" : "Cancel"}
            </Button>
            {keysOff.length > 0 && (
              <Button onClick={handleAddToKey} loading={addToKey.isPending}>
                Add
              </Button>
            )}
          </div>
        </div>
      </Dialog>
    </div>
  );
}
