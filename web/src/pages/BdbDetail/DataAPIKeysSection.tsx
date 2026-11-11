/**
 * Access tab section where a bdb's owner manages its Data API keys, which let apps of theirs use the bdb as their
 * database: listing the keys with the formulas each runs, creating one with its formulas, its value shown once with
 * the instructions for a coding agent, adding and removing a key's formulas, and revoking a key.
 */

import { type SubmitEvent, useMemo, useState } from "react";
import {
  type CreatedDataAPIKey,
  DATA_API_KEY_PREFIX,
  type DataAPIKey,
} from "../../api/dataAPIKeys";
import type { Formula } from "../../api/formulas";
import { Button } from "../../components/Button";
import { CopyableCode } from "../../components/CopyableCode";
import { Dialog } from "../../components/Dialog";
import { distinctFormulaTitles, formulaTitle } from "../../components/Formula/selectors";
import { Input } from "../../components/Input";
import { Menu } from "../../components/Menu";
import { MultiSelect } from "../../components/MultiSelect";
import { SkeletonRows } from "../../components/Skeleton";
import { useToast } from "../../components/Toast";
import { X } from "../../components/icons";
import {
  useAddDataAPIKeyFormula,
  useCreateDataAPIKey,
  useDataAPIKeys,
  useRemoveDataAPIKeyFormula,
  useRevokeDataAPIKey,
} from "../../hooks/useDataAPIKeys";
import { useFormulas } from "../../hooks/useFormulas";
import { dataAPIBaseURL, dataAPIInstructions } from "../../utils/dataApi";
import { formatDate } from "../../utils/date";
import styles from "./DataAPIKeysSection.module.css";

interface DataAPIKeysSectionProps {
  bdbId: string;
  bdbName: string;
}

/** Returns the line describing key: when it was created and last used. */
function keyDetails(key: DataAPIKey): string {
  const lastUsed = key.last_used_at ? `last used ${formatDate(key.last_used_at)}` : "never used";
  return `Created ${formatDate(key.created_at)} · ${lastUsed}`;
}

/** Returns the message of err, or fallback when it has none. */
function errorMessage(err: unknown, fallback: string): string {
  return err instanceof Error ? err.message : fallback;
}

export function DataAPIKeysSection({ bdbId, bdbName }: DataAPIKeysSectionProps) {
  const { toast } = useToast();
  const { data: keys } = useDataAPIKeys(bdbId);
  const { data: formulas = [] } = useFormulas(bdbId);
  const createKey = useCreateDataAPIKey();
  const revokeKey = useRevokeDataAPIKey();
  const addFormula = useAddDataAPIKeyFormula();
  const removeFormula = useRemoveDataAPIKeyFormula();

  const [createOpen, setCreateOpen] = useState(false);
  const [newName, setNewName] = useState("");
  const [newFormulaIds, setNewFormulaIds] = useState<string[]>([]);
  const [createError, setCreateError] = useState<string | null>(null);
  const [created, setCreated] = useState<CreatedDataAPIKey | null>(null);
  const [addingTo, setAddingTo] = useState<DataAPIKey | null>(null);
  const [addedFormulaIds, setAddedFormulaIds] = useState<string[]>([]);
  const [adding, setAdding] = useState(false);
  const [revoking, setRevoking] = useState<DataAPIKey | null>(null);

  const formulasById = useMemo(() => new Map(formulas.map((f) => [f.id, f])), [formulas]);
  const titles = useMemo(() => distinctFormulaTitles(formulas), [formulas]);
  const formulaOptions = useMemo(
    () =>
      formulas
        .map((f) => ({ value: f.id, label: titles.get(f.id) ?? formulaTitle(f) }))
        .sort((a, b) => a.label.localeCompare(b.label)),
    [formulas, titles],
  );

  /** Returns what the section calls formula, its slug added when another formula has the same title. */
  function titleOf(formula: Formula): string {
    return titles.get(formula.id) ?? formulaTitle(formula);
  }

  /** Returns the formulas key runs that the bdb still has, in the key's order. */
  function keyFormulas(key: { formula_ids: string[] }): Formula[] {
    return key.formula_ids.flatMap((id) => formulasById.get(id) ?? []);
  }

  function copyInstructions(key: { name: string; formula_ids: string[] }) {
    navigator.clipboard
      .writeText(dataAPIInstructions(bdbId, bdbName, key.name, keyFormulas(key)))
      .then(
        () => toast("Instructions copied: paste them to your coding agent.", "success"),
        () => toast("Couldn't copy the instructions", "error"),
      );
  }

  function openCreate() {
    setNewName("");
    setNewFormulaIds([]);
    setCreateError(null);
    setCreateOpen(true);
  }

  function handleCreate(e: SubmitEvent<HTMLFormElement>) {
    e.preventDefault();
    const name = newName.trim();
    if (!name) return;
    setCreateError(null);
    createKey.mutate(
      { bdbId, name, formulaIds: newFormulaIds },
      {
        onSuccess: (key) => {
          setCreateOpen(false);
          setCreated(key);
        },
        onError: (err) => setCreateError(errorMessage(err, "Failed to create the key")),
      },
    );
  }

  function openAdd(key: DataAPIKey) {
    setAddedFormulaIds([]);
    setAddingTo(key);
  }

  async function handleAdd() {
    if (!addingTo || addedFormulaIds.length === 0) return;
    setAdding(true);
    try {
      for (const formulaId of addedFormulaIds) {
        await addFormula.mutateAsync({ bdbId, keyId: addingTo.id, formulaId });
      }
      setAddingTo(null);
    } catch (err) {
      toast(errorMessage(err, "Failed to add the formulas"), "error");
    } finally {
      setAdding(false);
    }
  }

  function handleRemove(key: DataAPIKey, formula: Formula) {
    removeFormula.mutate(
      { bdbId, keyId: key.id, formulaId: formula.id },
      {
        onSuccess: () => toast(`${key.name} no longer runs ${titleOf(formula)}`, "success"),
        onError: (err) => toast(errorMessage(err, "Failed to remove the formula"), "error"),
      },
    );
  }

  function handleRevoke() {
    if (!revoking) return;
    revokeKey.mutate(
      { bdbId, keyId: revoking.id },
      {
        onSuccess: () => {
          toast(`Revoked ${revoking.name}`, "success");
          setRevoking(null);
        },
        onError: (err) => toast(errorMessage(err, "Failed to revoke the key"), "error"),
      },
    );
  }

  const addableOptions = addingTo
    ? formulaOptions.filter((o) => !addingTo.formula_ids.includes(o.value))
    : [];

  return (
    <section>
      <div className={styles.sectionHeader}>
        <h2 className={styles.sectionTitle}>Data API keys{keys ? ` (${keys.length})` : ""}</h2>
        <Button size="sm" onClick={openCreate}>
          Create key
        </Button>
      </div>
      <p className={styles.hint}>
        Apps of yours, such as a web page or a script, use this bdb as their database through the Data API at{" "}
        <CopyableCode text={dataAPIBaseURL()} />.
      </p>
      <p className={styles.hint}>A key acts as you, and runs only the formulas you pick for it.</p>

      {keys === undefined && <SkeletonRows />}
      {keys && keys.length === 0 && <p className={styles.hint}>No keys yet.</p>}
      {keys && keys.length > 0 && (
        <ul className={styles.list}>
          {keys.map((key) => {
            const runs = keyFormulas(key);
            return (
              <li key={key.id} className={styles.key}>
                <div className={styles.keyInfo}>
                  <div className={styles.keyTitle}>
                    <span className={styles.keyName}>{key.name}</span>
                    <code className={styles.prefix}>
                      {DATA_API_KEY_PREFIX}
                      {key.token_prefix}…
                    </code>
                  </div>
                  <span className={styles.details}>{keyDetails(key)}</span>
                  {runs.length === 0 ? (
                    <span className={styles.details}>Runs no formula yet.</span>
                  ) : (
                    <ul className={styles.formulas} aria-label={`Formulas ${key.name} runs`}>
                      {runs.map((formula) => (
                        <li key={formula.id} className={styles.formula}>
                          {titleOf(formula)}
                          <button
                            type="button"
                            className={styles.removeFormula}
                            aria-label={`Remove ${titleOf(formula)} from ${key.name}`}
                            title="Remove from this key"
                            onClick={() => handleRemove(key, formula)}
                          >
                            <X size={14} aria-hidden="true" />
                          </button>
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
                <Menu
                  aria-label={`${key.name} actions`}
                  items={[
                    ...(runs.length < formulas.length
                      ? [{ label: "Add formulas…", onSelect: () => openAdd(key) }]
                      : []),
                    { label: "Copy instructions for your coding agent", onSelect: () => copyInstructions(key) },
                    {
                      label: "Revoke…",
                      onSelect: () => setRevoking(key),
                      destructive: true,
                      separatorBefore: true,
                    },
                  ]}
                />
              </li>
            );
          })}
        </ul>
      )}

      <Dialog open={createOpen} onClose={() => setCreateOpen(false)} title="Create a Data API key">
        <form onSubmit={handleCreate} className={styles.dialogBody}>
          <Input
            label="Key name"
            name="key_name"
            required
            placeholder="The app it is for, e.g. budget tracker"
            value={newName}
            onChange={(e) => setNewName(e.target.value)}
          />
          <div className={styles.field}>
            <span className={styles.fieldLabel}>Formulas it runs</span>
            <MultiSelect
              values={newFormulaIds}
              options={formulaOptions}
              noun="formulas"
              onChange={setNewFormulaIds}
            />
            <span className={styles.hint}>You can add or remove formulas later.</span>
          </div>
          {createError && <p className={styles.error}>{createError}</p>}
          <div className={styles.actions}>
            <Button type="button" variant="ghost" onClick={() => setCreateOpen(false)}>
              Cancel
            </Button>
            <Button type="submit" loading={createKey.isPending}>
              Create key
            </Button>
          </div>
        </form>
      </Dialog>

      <Dialog open={created !== null} onClose={() => setCreated(null)} title={`Key ${created?.name ?? ""} created`}>
        {created && (
          <div className={styles.dialogBody}>
            <p className={styles.hint}>Copy it now: it won&apos;t be shown again.</p>
            <CopyableCode text={created.token} block />
            <p className={styles.hint}>
              Give it to your app through its configuration, never in its code&apos;s repository. Your coding agent
              writes the calls from the instructions, which describe the Data API and this key&apos;s formulas without
              the key itself.
            </p>
            <div className={styles.actions}>
              <Button variant="secondary" onClick={() => copyInstructions(created)}>
                Copy instructions for your coding agent
              </Button>
              <Button onClick={() => setCreated(null)}>Done</Button>
            </div>
          </div>
        )}
      </Dialog>

      <Dialog
        open={addingTo !== null}
        onClose={() => setAddingTo(null)}
        title={`Add formulas to ${addingTo?.name ?? ""}`}
      >
        <div className={styles.dialogBody}>
          <MultiSelect
            values={addedFormulaIds}
            options={addableOptions}
            noun="formulas"
            onChange={setAddedFormulaIds}
          />
          <p className={styles.hint}>The app runs them with the key it already has.</p>
          <div className={styles.actions}>
            <Button variant="ghost" onClick={() => setAddingTo(null)}>
              Cancel
            </Button>
            <Button onClick={handleAdd} loading={adding} disabled={addedFormulaIds.length === 0}>
              Add
            </Button>
          </div>
        </div>
      </Dialog>

      <Dialog open={revoking !== null} onClose={() => setRevoking(null)} title={`Revoke ${revoking?.name ?? ""}?`}>
        <div className={styles.dialogBody}>
          <p className={styles.hint}>Apps using it stop reaching this bdb at once.</p>
          <div className={styles.actions}>
            <Button variant="ghost" onClick={() => setRevoking(null)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={handleRevoke} loading={revokeKey.isPending}>
              Revoke
            </Button>
          </div>
        </div>
      </Dialog>
    </section>
  );
}
