/** Starter gallery — pick a curated starter to create a ready-to-use bdb, or start blank. */

import { type SubmitEvent, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import type { Bdb } from "../../api/bdbs";
import type { Starter } from "../../api/starters";
import { Button } from "../Button";
import { Card } from "../Card";
import { Dialog } from "../Dialog";
import { Input } from "../Input";
import { Skeleton } from "../Skeleton";
import { useToast } from "../Toast";
import { useCreateBdbFromStarter } from "../../hooks/useBdbMutations";
import { useStarters } from "../../hooks/useStarters";
import styles from "./StarterGallery.module.css";

interface StarterGalleryProps {
  /** Heading above the gallery; the empty state and the new-bdb page set their own. */
  title?: string;
  /** Continuation of the intro line after the leading "Start blank, or " link. */
  subtitle?: string;
  /** Route of the blank-creation form; the standalone shell points it inside /app. */
  blankTo?: string;
  /** Builds the post-create destination; the standalone shell keeps it inside /app. */
  createdTo?: (bdb: Bdb) => string;
}

export function StarterGallery({
  title = "Create a bdb",
  subtitle = "pick a curated starter — tables, formulas, and a skill included.",
  blankTo = "/bdbs/new/blank",
  createdTo = (bdb) => `/bdbs/${bdb.id}`,
}: StarterGalleryProps) {
  const navigate = useNavigate();
  const { toast } = useToast();
  const { data: starters = [], isLoading } = useStarters();
  const createFromStarter = useCreateBdbFromStarter();
  const [selected, setSelected] = useState<Starter | null>(null);
  const [error, setError] = useState<string | null>(null);

  function openStarter(starter: Starter) {
    setError(null);
    setSelected(starter);
  }

  async function handleCreate(e: SubmitEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!selected) return;
    setError(null);

    const form = new FormData(e.currentTarget);
    try {
      const bdb = await createFromStarter.mutateAsync({
        starter_key: selected.key,
        name: form.get("name") as string,
        description: form.get("description") as string,
      });
      toast("bdb created!", "success");
      navigate(createdTo(bdb), { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create the bdb");
    }
  }

  if (isLoading) {
    return (
      <div className={styles.container}>
        <h1 className={styles.title}>{title}</h1>
        <div className={styles.grid}>
          {Array.from({ length: 3 }, (_, i) => (
            <div key={i} className={styles.skeletonCard}>
              <Skeleton className={styles.skeletonTitle} />
              <Skeleton className={styles.skeletonLine} />
              <Skeleton className={styles.skeletonLineShort} />
            </div>
          ))}
        </div>
      </div>
    );
  }

  return (
    <div className={styles.container}>
      <h1 className={styles.title}>{title}</h1>
      <p className={styles.subtitle}>
        <Link to={blankTo} className={styles.blankLink}>
          Start blank
        </Link>
        , or {subtitle}
      </p>

      <div className={styles.grid}>
        {starters.map((starter) => (
          <button
            key={starter.key}
            type="button"
            className={styles.cardButton}
            onClick={() => openStarter(starter)}
          >
            <Card className={styles.card}>
              <div className={styles.cardBody}>
                <div className={styles.cardHeader}>
                  {starter.icon && <span className={styles.cardIcon}>{starter.icon}</span>}
                  <span className={styles.cardName}>{starter.name}</span>
                </div>
                <p className={styles.cardDesc}>{starter.description}</p>
                <p className={styles.cardMeta}>
                  {starter.formulas.length} formula{starter.formulas.length !== 1 ? "s" : ""} ·{" "}
                  {starter.skills.length} skill{starter.skills.length !== 1 ? "s" : ""}
                </p>
                {(() => {
                  // The chips preview the starter's visible views — the tabs a fresh bdb opens on —
                  // not the attached actions and hidden plumbing riding along in the manifest.
                  const views = starter.formulas.filter((f) => !f.attached && !f.hidden);
                  if (views.length === 0) return null;
                  return (
                    <div className={styles.cardPreview}>
                      {views.slice(0, 3).map((f) => (
                        <span key={f.name} className={styles.previewChip} title={f.description}>
                          {f.label || f.name}
                        </span>
                      ))}
                      {views.length > 3 && (
                        <span className={styles.previewMore}>+{views.length - 3} more</span>
                      )}
                    </div>
                  );
                })()}
              </div>
            </Card>
          </button>
        ))}
      </div>

      <Dialog
        open={selected !== null}
        onClose={() => setSelected(null)}
        title={selected ? `Create from ${selected.name}` : undefined}
      >
        {selected && (
          <form onSubmit={handleCreate} className={styles.dialogForm}>
            <Input label="Name" name="name" required defaultValue={selected.name} />
            <Input
              label="Description"
              name="description"
              placeholder={selected.description}
            />
            {error && <p className={styles.error}>{error}</p>}
            <div className={styles.dialogActions}>
              <Button variant="secondary" type="button" onClick={() => setSelected(null)}>
                Cancel
              </Button>
              <Button type="submit" loading={createFromStarter.isPending}>
                Create
              </Button>
            </div>
          </form>
        )}
      </Dialog>
    </div>
  );
}
