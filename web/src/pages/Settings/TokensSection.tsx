/**
 * Settings section managing the signed-in user's personal access tokens, which scripts and MCP clients send as their
 * bearer token: listing them, creating one, its value shown once with the snippets that register it, and revoking one.
 */

import { type SubmitEvent, useRef, useState } from "react";
import {
  type CreatedPersonalToken,
  PERSONAL_TOKEN_PREFIX,
  type PersonalToken,
} from "../../api/personalTokens";
import { Button } from "../../components/Button";
import { CopyableCode } from "../../components/CopyableCode";
import { Dialog } from "../../components/Dialog";
import { Input } from "../../components/Input";
import { Select } from "../../components/Select";
import { useToast } from "../../components/Toast";
import { getConfig } from "../../config";
import { useCreatePersonalToken, usePersonalTokens, useRevokePersonalToken } from "../../hooks/usePersonalTokens";
import { formatDate } from "../../utils/date";
import { claudeCodeCommand, mcpConfigEntry } from "../../utils/mcpSnippets";
import styles from "./TokensSection.module.css";

/** How long a new token works, in days, by the value of the expiry select; "never" has none. */
const expiryOptions = [
  { value: "never", label: "Never expires" },
  { value: "30", label: "Expires in 30 days" },
  { value: "90", label: "Expires in 90 days" },
  { value: "365", label: "Expires in 1 year" },
];

/** Returns the ISO time a token created now with the expiry option value stops working, undefined for never. */
function expiresAtFor(value: string): string | undefined {
  const days = Number(value);
  if (!Number.isFinite(days)) return undefined;
  return new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString();
}

/** Returns the line describing token: when it was created, last used and expires. */
function tokenDetails(token: PersonalToken): string {
  const lastUsed = token.last_used_at ? `last used ${formatDate(token.last_used_at)}` : "never used";
  let expiry = "never expires";
  if (token.expires_at) {
    expiry = new Date(token.expires_at) <= new Date() ? "expired" : `expires ${formatDate(token.expires_at)}`;
  }
  return `Created ${formatDate(token.created_at)} · ${lastUsed} · ${expiry}`;
}

export function TokensSection() {
  const { mcpUrl } = getConfig();
  const { toast } = useToast();
  const { data: tokens } = usePersonalTokens();
  const createToken = useCreatePersonalToken();
  const revokeToken = useRevokePersonalToken();
  const formRef = useRef<HTMLFormElement>(null);
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState<CreatedPersonalToken | null>(null);
  const [revoking, setRevoking] = useState<PersonalToken | null>(null);

  function handleCreate(e: SubmitEvent<HTMLFormElement>) {
    e.preventDefault();
    setError(null);
    const form = new FormData(e.currentTarget);
    const name = (form.get("token_name") as string).trim();
    if (!name) return;
    createToken.mutate(
      { name, expiresAt: expiresAtFor(form.get("expires_in") as string) },
      {
        onSuccess: (token) => {
          setCreated(token);
          formRef.current?.reset();
        },
        onError: (err) => setError(err instanceof Error ? err.message : "Failed to create the token"),
      },
    );
  }

  function handleRevoke() {
    if (!revoking) return;
    revokeToken.mutate(revoking.id, {
      onSuccess: () => {
        toast(`Revoked ${revoking.name}`, "success");
        setRevoking(null);
      },
      onError: (err) => toast(err instanceof Error ? err.message : "Failed to revoke the token", "error"),
    });
  }

  return (
    <section className={styles.section}>
      <h2 className={styles.sectionTitle}>Personal Access Tokens</h2>
      <p className={styles.hint}>
        Scripts and AI assistants send a token to reach your data, through the MCP endpoint{" "}
        <CopyableCode text={mcpUrl} /> or the API. Each works until you revoke it, or until it expires.
      </p>

      {tokens && tokens.length > 0 && (
        <ul className={styles.list}>
          {tokens.map((token) => (
            <li key={token.id} className={styles.token}>
              <div className={styles.tokenInfo}>
                <div className={styles.tokenTitle}>
                  <span className={styles.tokenName}>{token.name}</span>
                  <code className={styles.prefix}>
                    {PERSONAL_TOKEN_PREFIX}
                    {token.token_prefix}…
                  </code>
                </div>
                <span className={styles.details}>{tokenDetails(token)}</span>
              </div>
              <Button variant="ghost" size="sm" onClick={() => setRevoking(token)}>
                Revoke
              </Button>
            </li>
          ))}
        </ul>
      )}
      {tokens && tokens.length === 0 && <p className={styles.hint}>No tokens yet.</p>}

      <form ref={formRef} onSubmit={handleCreate} className={styles.form}>
        <Input label="Token name" name="token_name" required placeholder="What it is for, e.g. laptop-claude" />
        <Select label="Expiry" name="expires_in" options={expiryOptions} defaultValue="never" />
        {error && <p className={styles.error}>{error}</p>}
        <div className={styles.actions}>
          <Button type="submit" loading={createToken.isPending}>
            Create Token
          </Button>
        </div>
      </form>

      <Dialog open={created !== null} onClose={() => setCreated(null)} title={`Token ${created?.name ?? ""} created`}>
        {created && (
          <div className={styles.dialogBody}>
            <p className={styles.hint}>Copy it now: it won&apos;t be shown again.</p>
            <CopyableCode text={created.token} block />
            <p className={styles.hint}>
              To connect Claude Code with it, run:
              <CopyableCode text={claudeCodeCommand(mcpUrl, created.token)} block />
            </p>
            <p className={styles.hint}>
              Or add it to a client&apos;s configuration file, such as <code>~/.claude.json</code> or a
              project&apos;s <code>.mcp.json</code>:
              <CopyableCode text={mcpConfigEntry(mcpUrl, created.token)} block />
            </p>
            <div className={styles.actions}>
              <Button onClick={() => setCreated(null)}>Done</Button>
            </div>
          </div>
        )}
      </Dialog>

      <Dialog open={revoking !== null} onClose={() => setRevoking(null)} title={`Revoke ${revoking?.name ?? ""}?`}>
        <div className={styles.dialogBody}>
          <p className={styles.hint}>Scripts and assistants using it stop reaching your data at once.</p>
          <div className={styles.actions}>
            <Button variant="ghost" onClick={() => setRevoking(null)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={handleRevoke} loading={revokeToken.isPending}>
              Revoke
            </Button>
          </div>
        </div>
      </Dialog>
    </section>
  );
}
