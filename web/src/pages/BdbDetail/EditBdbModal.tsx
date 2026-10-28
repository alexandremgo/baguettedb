/** Modal to edit a bdb's name and description, prefilled with the current values. */

import { type SubmitEvent } from "react";
import type { Bdb } from "../../api/bdbs";
import { Button } from "../../components/Button";
import { Dialog } from "../../components/Dialog";
import { Input } from "../../components/Input";
import { useToast } from "../../components/Toast";
import { useUpdateBdb } from "../../hooks/useBdbMutations";
import styles from "./EditBdbModal.module.css";

interface EditBdbModalProps {
  bdb: Bdb;
  open: boolean;
  onClose: () => void;
}

export function EditBdbModal({ bdb, open, onClose }: EditBdbModalProps) {
  const { toast } = useToast();
  const updateMutation = useUpdateBdb();

  function handleSubmit(e: SubmitEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = new FormData(e.currentTarget);
    const name = form.get("name") as string;
    const description = form.get("description") as string;
    if (name === bdb.name && description === bdb.description) {
      onClose();
      return;
    }

    updateMutation.mutate(
      { id: bdb.id, data: { name, description } },
      {
        onSuccess: () => {
          toast("bdb updated.", "success");
          onClose();
        },
        onError: (err) => {
          toast(err instanceof Error ? err.message : "Failed to update bdb", "error");
        },
      },
    );
  }

  return (
    <Dialog open={open} onClose={onClose} title={`Edit "${bdb.name}"`}>
      <form onSubmit={handleSubmit} className={styles.form}>
        <Input label="Name" name="name" required defaultValue={bdb.name} autoFocus />
        <Input
          label="Description"
          name="description"
          defaultValue={bdb.description}
          placeholder="What is this bdb for?"
          hint="Helps you — and your AI assistants — pick the right bdb later."
        />
        <div className={styles.actions}>
          <Button variant="secondary" type="button" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={updateMutation.isPending}>
            Save
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
