/** Lists all bdbs the user belongs to as a card grid. */

import { Link } from "react-router-dom";
import type { Bdb } from "../../api/bdbs";
import { Badge } from "../../components/Badge";
import { Button } from "../../components/Button";
import { Card } from "../../components/Card";
import { Skeleton } from "../../components/Skeleton";
import { StarterGallery } from "../../components/StarterGallery";
import { useToast } from "../../components/Toast";
import { useBdbs } from "../../hooks/useBdbs";
import { useRestoreBdb } from "../../hooks/useBdbMutations";
import { badgedRole, bdbOwnerAttribution, roleBadgeVariant } from "../../utils/bdbDisplay";
import { formatPurgeDate } from "../../utils/date";
import styles from "./BdbList.module.css";

export function BdbList() {
  const { data: bdbs = [], isLoading } = useBdbs();
  const { toast } = useToast();
  const restoreMutation = useRestoreBdb();

  function handleRestore(bdb: Bdb) {
    restoreMutation.mutate(bdb.id, {
      onSuccess: () => toast("bdb restored.", "success"),
      onError: (err) => toast(err instanceof Error ? err.message : "Failed to restore bdb", "error"),
    });
  }

  if (isLoading) {
    return (
      <div className={styles.grid}>
        {Array.from({ length: 6 }, (_, i) => (
          <div key={i} className={styles.skeletonCard}>
            <Skeleton className={styles.skeletonTitle} />
            <Skeleton className={styles.skeletonLine} />
            <Skeleton className={styles.skeletonLineShort} />
          </div>
        ))}
      </div>
    );
  }

  if (bdbs.length === 0) {
    return (
      <StarterGallery
        title="Welcome to BaguetteDB"
        subtitle="create your first bdb from a curated starter — tables, formulas, and a skill included."
      />
    );
  }

  return (
    <div>
      <h1 className={styles.title}>Your bdbs</h1>
      <div className={styles.grid}>
        {bdbs.map((c: Bdb) => {
          const isOwner = c.my_role === "owner";
          const isSoftDeleted = Boolean(c.deleted_at);
          return (
            <div
              key={c.id}
              className={[styles.cardWrapper, isSoftDeleted ? styles.cardSoftDeleted : ""]
                .filter(Boolean)
                .join(" ")}
            >
              <Link to={`/bdbs/${c.id}`} className={styles.cardLink}>
                <Card className={styles.card}>
                  <div className={styles.cardBody}>
                    <div className={styles.cardName}>{c.name}</div>
                    {bdbOwnerAttribution(c) && (
                      <p className={styles.cardOwner}>by {bdbOwnerAttribution(c)}</p>
                    )}
                    {c.description && (
                      <p className={styles.cardDesc}>{c.description}</p>
                    )}
                    <div className={styles.cardMeta}>
                      {badgedRole(c.my_role) && (
                        <Badge variant={roleBadgeVariant(c.my_role)}>{c.my_role}</Badge>
                      )}
                      <span className={styles.memberCount}>
                        {c.members.length} member{c.members.length !== 1 ? "s" : ""}
                      </span>
                    </div>
                    {isSoftDeleted && (
                      <p className={styles.purgeNotice}>
                        Scheduled for deletion on {formatPurgeDate(c.purge_at)}
                      </p>
                    )}
                  </div>
                </Card>
              </Link>
              {isOwner && isSoftDeleted && (
                <div className={styles.cardActions}>
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => handleRestore(c)}
                    loading={restoreMutation.isPending && restoreMutation.variables === c.id}
                  >
                    Restore
                  </Button>
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}
