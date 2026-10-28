/** Type-to-confirm modal for soft-deleting a bdb. */

import { useState } from "react";
import type { Bdb } from "../../api/bdbs";
import { Button } from "../../components/Button";
import { Dialog } from "../../components/Dialog";
import { Input } from "../../components/Input";
import { useToast } from "../../components/Toast";
import { useDeleteBdb } from "../../hooks/useBdbMutations";
import styles from "./DeleteBdbModal.module.css";

interface DeleteBdbModalProps {
  bdb: Bdb;
  open: boolean;
  onClose: () => void;
}

export function DeleteBdbModal({ bdb, open, onClose }: DeleteBdbModalProps) {
  const [typed, setTyped] = useState("");
  const { toast } = useToast();
  const deleteMutation = useDeleteBdb();

  const matches = typed === bdb.name;

  function handleClose() {
    setTyped("");
    onClose();
  }

  function handleConfirm() {
    if (!matches) return;
    deleteMutation.mutate(bdb.id, {
      onSuccess: () => {
        toast("bdb deleted. You can restore it within 7 days.", "success");
        handleClose();
      },
      onError: (err) => {
        toast(err instanceof Error ? err.message : "Failed to delete bdb", "error");
      },
    });
  }

  return (
    <Dialog open={open} onClose={handleClose} title={`Delete "${bdb.name}"?`}>
      <div className={styles.body}>
        <p className={styles.warning}>
          This bdb and its data will be scheduled for permanent deletion in 7 days. You can restore it from your
          bdbs list at any time during that window. After 7 days the data is permanently destroyed and cannot be
          recovered.
        </p>
        <Input
          label={`To confirm, type the bdb name below:`}
          placeholder={bdb.name}
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          autoFocus
          autoComplete="off"
          spellCheck={false}
        />
        <div className={styles.actions}>
          <Button variant="ghost" onClick={handleClose}>
            Cancel
          </Button>
          <Button
            variant="destructive"
            onClick={handleConfirm}
            disabled={!matches}
            loading={deleteMutation.isPending}
          >
            Delete bdb
          </Button>
        </div>
      </div>
    </Dialog>
  );
}
