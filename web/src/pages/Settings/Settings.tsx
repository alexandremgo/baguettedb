/**
 * User settings page with account storage usage and change password form. In token mode it also lists the user's
 * personal access tokens, and the form offers to sign out everywhere, this browser included, which a password change
 * does unless unticked.
 */

import { type SubmitEvent, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { Button } from "../../components/Button";
import { Input } from "../../components/Input";
import { useToast } from "../../components/Toast";
import { UsageMeter } from "../../components/UsageMeter";
import { changePassword } from "../../api/auth";
import { isTokenMode } from "../../config";
import { extPlugin } from "../../extPlugins";
import { useAccountStorage } from "../../hooks/useAccountStorage";
import { formatBytes } from "../../utils/bytes";
import styles from "./Settings.module.css";
import { TokensSection } from "./TokensSection";

export function Settings() {
  const { toast } = useToast();
  const showAccountStorage = !extPlugin.hideSettingsAccountStorage;
  const { data: storage } = useAccountStorage(showAccountStorage);
  const formRef = useRef<HTMLFormElement>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleSubmit(e: SubmitEvent<HTMLFormElement>) {
    e.preventDefault();
    setError(null);

    const form = new FormData(e.currentTarget);
    const newPassword = form.get("new_password") as string;
    const confirmPassword = form.get("confirm_password") as string;

    if (newPassword !== confirmPassword) {
      setError("New passwords do not match");
      return;
    }

    const endSessions = isTokenMode() ? form.get("end_sessions") === "on" : undefined;
    setLoading(true);
    try {
      await changePassword(form.get("old_password") as string, newPassword, endSessions);
      if (endSessions) {
        window.location.href = "/sign-in?ended=1";
        return;
      }
      toast("Password changed successfully", "success");
      formRef.current?.reset();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to change password");
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className={styles.container}>
      <h1 className={styles.title}>Settings</h1>

      {showAccountStorage && storage && (
        <section className={styles.section}>
          <h2 className={styles.sectionTitle}>Account Storage</h2>
          {storage.total_storage_bytes_limit > 0 ? (
            <UsageMeter
              label="Used across your bdbs"
              usedBytes={storage.total_storage_bytes_used}
              limitBytes={storage.total_storage_bytes_limit}
            />
          ) : (
            <p className={styles.hint}>
              {formatBytes(storage.total_storage_bytes_used)} used across your bdbs.
            </p>
          )}
        </section>
      )}

      <section className={styles.section}>
        <h2 className={styles.sectionTitle}>MCP Setup</h2>
        <p className={styles.hint}>
          Connect your AI assistant to BaguetteDB using the MCP protocol.
        </p>
        <Link to="/setup" className={styles.setupLink}>
          Open Setup Guide
        </Link>
      </section>

      {isTokenMode() && <TokensSection />}

      <section className={styles.section}>
        <h2 className={styles.sectionTitle}>Change Password</h2>
        <form ref={formRef} onSubmit={handleSubmit} className={styles.form}>
          <Input label="Current Password" name="old_password" type="password" required />
          <Input label="New Password" name="new_password" type="password" required placeholder="At least 8 characters" />
          <Input label="Confirm New Password" name="confirm_password" type="password" required />
          {isTokenMode() && (
            <label className={styles.checkboxRow}>
              <input type="checkbox" name="end_sessions" defaultChecked className={styles.checkbox} />
              Sign out everywhere, this browser included
            </label>
          )}
          {error && <p className={styles.error}>{error}</p>}
          <div className={styles.actions}>
            <Button type="submit" loading={loading}>
              Change Password
            </Button>
          </div>
        </form>
      </section>
    </div>
  );
}
