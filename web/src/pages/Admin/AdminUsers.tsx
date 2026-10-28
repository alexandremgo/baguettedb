/** Admin users tab — user list and create user form. */

import { type SubmitEvent, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "../../components/Button";
import { Input } from "../../components/Input";
import { Badge } from "../../components/Badge";
import { SkeletonRows } from "../../components/Skeleton";
import { useToast } from "../../components/Toast";
import { listUsers, createUser, type CreateUserRequest } from "../../api/admin";
import styles from "./Admin.module.css";

export function AdminUsers() {
  const { toast } = useToast();
  const queryClient = useQueryClient();
  const [search, setSearch] = useState("");
  const [creating, setCreating] = useState(false);
  const [showForm, setShowForm] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const formRef = useRef<HTMLFormElement>(null);

  const { data, isLoading, isError } = useQuery({
    queryKey: ["admin-users", search],
    queryFn: () => listUsers(search, 50, 0),
  });
  const users = data?.users ?? [];
  const total = data?.total ?? 0;

  async function handleCreate(e: SubmitEvent<HTMLFormElement>) {
    e.preventDefault();
    setError(null);
    setCreating(true);

    const form = new FormData(e.currentTarget);
    const payload: CreateUserRequest = {
      email: form.get("email") as string,
      password: form.get("password") as string,
      first_name: form.get("first_name") as string,
      last_name: form.get("last_name") as string,
    };

    try {
      await createUser(payload);
      toast("User created", "success");
      formRef.current?.reset();
      setShowForm(false);
      void queryClient.invalidateQueries({ queryKey: ["admin-users"] });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create user");
    } finally {
      setCreating(false);
    }
  }

  return (
    <>
      <div className={styles.header}>
        <h2 className={styles.title}>Users</h2>
        <Button onClick={() => setShowForm((v) => !v)}>
          {showForm ? "Cancel" : "+ New User"}
        </Button>
      </div>

      {showForm && (
        <form ref={formRef} onSubmit={handleCreate} className={styles.form}>
          <div className={styles.formRow}>
            <Input label="Email" name="email" type="email" required />
            <Input label="Password" name="password" type="password" required placeholder="At least 8 characters" />
          </div>
          <div className={styles.formRow}>
            <Input label="First Name" name="first_name" placeholder="Optional" />
            <Input label="Last Name" name="last_name" placeholder="Optional" />
          </div>
          {error && <p className={styles.error}>{error}</p>}
          <div className={styles.formActions}>
            <Button type="submit" loading={creating}>Create User</Button>
          </div>
        </form>
      )}

      <Input
        label=""
        name="search"
        placeholder="Search by email or name..."
        value={search}
        onChange={(e) => setSearch(e.target.value)}
      />

      {isLoading ? (
        <SkeletonRows />
      ) : isError ? (
        <p className={styles.error}>Failed to load users</p>
      ) : (
        <>
          <p className={styles.count}>{total} user{total !== 1 ? "s" : ""}</p>
          <table className={styles.table}>
            <thead>
              <tr>
                <th>Email</th>
                <th>Name</th>
                <th>Roles</th>
                <th>Created</th>
              </tr>
            </thead>
            <tbody>
              {users.map((u) => (
                <tr key={u.id}>
                  <td>{u.email}</td>
                  <td>{[u.first_name, u.last_name].filter(Boolean).join(" ") || "—"}</td>
                  <td>
                    {u.roles?.map((r) => (
                      <Badge key={r} variant={r === "admin" ? "owner" : "default"}>{r}</Badge>
                    )) ?? "—"}
                  </td>
                  <td>{new Date(u.created_at).toLocaleDateString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </>
  );
}
