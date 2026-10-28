/** Form to create a new bdb. */

import { type SubmitEvent, useState } from "react";
import { useNavigate } from "react-router-dom";
import type { Bdb } from "../../api/bdbs";
import { Button } from "../../components/Button";
import { Input } from "../../components/Input";
import { useToast } from "../../components/Toast";
import { useCreateBdb } from "../../hooks/useBdbMutations";
import styles from "./BdbCreate.module.css";

interface BdbCreateProps {
  /** Builds the post-create destination; the standalone shell keeps it inside /app. */
  createdTo?: (bdb: Bdb) => string;
}

export function BdbCreate({ createdTo = (bdb) => `/bdbs/${bdb.id}` }: BdbCreateProps) {
  const navigate = useNavigate();
  const { toast } = useToast();
  const [error, setError] = useState<string | null>(null);
  const createBdb = useCreateBdb();

  async function handleSubmit(e: SubmitEvent<HTMLFormElement>) {
    e.preventDefault();
    setError(null);

    const form = new FormData(e.currentTarget);

    try {
      const bdb = await createBdb.mutateAsync({
        name: form.get("name") as string,
        description: form.get("description") as string,
      });
      toast("bdb created!", "success");
      navigate(createdTo(bdb), { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create bdb");
    }
  }

  return (
    <div className={styles.container}>
      <h1 className={styles.title}>Create bdb</h1>
      <form onSubmit={handleSubmit} className={styles.form}>
        <Input label="Name" name="name" required placeholder="My bdb" />
        <Input
          label="Description"
          name="description"
          placeholder="What is this bdb for?"
          hint="Helps you — and your AI assistants — pick the right bdb later."
        />
        {error && <p className={styles.error}>{error}</p>}
        <div className={styles.actions}>
          <Button variant="secondary" type="button" onClick={() => navigate(-1)}>
            Cancel
          </Button>
          <Button type="submit" loading={createBdb.isPending}>
            Create
          </Button>
        </div>
      </form>
    </div>
  );
}
