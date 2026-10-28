/** Skills viewer for a bdb: list + filter + body modal + delete. */

import { useMemo, useState } from "react";
import { Button } from "../../components/Button";
import { EmptyState } from "../../components/EmptyState";
import { BookOpen } from "../../components/icons";
import { Card } from "../../components/Card";
import { Dialog } from "../../components/Dialog";
import { Input } from "../../components/Input";
import { SkeletonRows } from "../../components/Skeleton";
import { Table } from "../../components/Table";
import { useToast } from "../../components/Toast";
import { useDeleteSkill, useSkills } from "../../hooks/useSkills";
import type { Skill } from "../../api/skills";
import styles from "./SkillsTab.module.css";

interface SkillsTabProps {
  bdbId: string;
  /** When true, the modal shows a Delete button. Pass true for editor+ roles. */
  canEdit: boolean;
}

function formatDateTime(iso: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

/** Sorts skills ascending by target-table count, then alphabetically by name. */
function sortSkills(skills: Skill[]): Skill[] {
  return [...skills].sort((a, b) => {
    const byCount = a.target_tables.length - b.target_tables.length;
    if (byCount !== 0) return byCount;
    return a.name.localeCompare(b.name);
  });
}

export function SkillsTab({ bdbId, canEdit }: SkillsTabProps) {
  const { data: skills, isLoading, error } = useSkills(bdbId);
  const deleteSkillMutation = useDeleteSkill(bdbId);
  const { toast } = useToast();
  const [filter, setFilter] = useState("");
  const [openSkill, setOpenSkill] = useState<Skill | null>(null);
  const [confirmingDelete, setConfirmingDelete] = useState(false);

  function closeModal() {
    setOpenSkill(null);
    setConfirmingDelete(false);
  }

  function handleDelete() {
    if (!openSkill) return;
    deleteSkillMutation.mutate(openSkill.id, {
      onSuccess: () => {
        toast(`Deleted skill "${openSkill.name}"`, "success");
        closeModal();
      },
      onError: (err) => {
        toast(err instanceof Error ? err.message : "Failed to delete skill", "error");
      },
    });
  }

  const visible = useMemo(() => {
    const all = sortSkills(skills ?? []);
    const q = filter.trim().toLowerCase();
    if (!q) return all;
    return all.filter((s) => s.name.toLowerCase().includes(q));
  }, [skills, filter]);

  if (isLoading) {
    return <SkeletonRows />;
  }

  if (error) {
    return (
      <p className={styles.error}>
        {error instanceof Error ? error.message : "Failed to load skills"}
      </p>
    );
  }

  if (!skills || skills.length === 0) {
    return (
      <EmptyState
        icon={BookOpen}
        title="No skills yet."
        hint="Skills teach your assistant how to work with this bdb — ask it to add one."
      />
    );
  }

  return (
    <div className={styles.container}>
      <Input
        type="search"
        placeholder="Filter by name..."
        value={filter}
        onChange={(e) => setFilter(e.target.value)}
        className={styles.filter}
      />

      {visible.length === 0 ? (
        <p className={styles.empty}>No skills match the filter.</p>
      ) : (
        <Card>
          <Table
            columns={[
              {
                key: "name",
                header: "Name",
                render: (s) => <span className={styles.name}>{s.name}</span>,
              },
              {
                key: "description",
                header: "Description",
                render: (s) => <span className={styles.description}>{s.description}</span>,
              },
              {
                key: "target_tables",
                header: "Target tables",
                render: (s) => {
                  if (s.target_tables.length === 0) {
                    return <span className={styles.bdbLevel}>bdb-level</span>;
                  }
                  return (
                    <span className={styles.targets}>
                      {s.target_tables.join(", ")}{" "}
                      <span className={styles.targetCount}>
                        ({s.target_tables.length})
                      </span>
                    </span>
                  );
                },
              },
            ]}
            data={visible}
            rowKey={(s) => s.id}
            onRowClick={(s) => setOpenSkill(s)}
          />
        </Card>
      )}

      <Dialog
        open={!!openSkill}
        onClose={closeModal}
        title={openSkill?.name}
        className={styles.dialog}
      >
        {openSkill && !confirmingDelete && (
          <div className={styles.modalContent}>
            <p className={styles.modalDescription}>{openSkill.description}</p>

            <div className={styles.metaRow}>
              <span className={styles.metaLabel}>Targets:</span>
              <span className={styles.metaValue}>
                {openSkill.target_tables.length === 0
                  ? "bdb-level (no target tables)"
                  : openSkill.target_tables.join(", ")}
              </span>
            </div>

            <div className={styles.metaRow}>
              <span className={styles.metaLabel}>Created:</span>
              <span className={styles.metaValue}>
                {formatDateTime(openSkill.created_at)}
              </span>
            </div>

            <div className={styles.metaRow}>
              <span className={styles.metaLabel}>Updated:</span>
              <span className={styles.metaValue}>
                {formatDateTime(openSkill.updated_at)}
              </span>
            </div>

            <div className={styles.bodyLabel}>Body</div>
            <pre className={styles.body}>{openSkill.body}</pre>

            {canEdit && (
              <div className={styles.modalActions}>
                <Button
                  variant="destructive"
                  size="sm"
                  onClick={() => setConfirmingDelete(true)}
                >
                  Delete
                </Button>
              </div>
            )}
          </div>
        )}

        {openSkill && confirmingDelete && (
          <div className={styles.modalContent}>
            <p className={styles.confirmText}>
              Delete skill <strong>{openSkill.name}</strong>? This cannot be undone.
            </p>
            <div className={styles.modalActions}>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setConfirmingDelete(false)}
                disabled={deleteSkillMutation.isPending}
              >
                Cancel
              </Button>
              <Button
                variant="destructive"
                size="sm"
                onClick={handleDelete}
                loading={deleteSkillMutation.isPending}
              >
                Delete
              </Button>
            </div>
          </div>
        )}
      </Dialog>
    </div>
  );
}
