/**
 * Full-database export dialog: shows the caller's rolling-window quota before anything runs, produces the
 * encrypted download on Start, then reveals the one-time passphrase — the only moment it ever exists
 * client-side, so it must be copied before the dialog closes.
 */

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { downloadDatabaseExport } from "../../api/exports";
import { useExportStatus } from "../../hooks/useExportStatus";
import { Button } from "../Button";
import { Dialog } from "../Dialog";
import styles from "./ExportDialog.module.css";

interface ExportBdbDialogProps {
  bdbId: string;
  bdbName: string;
  open: boolean;
  onClose: () => void;
}

export function ExportBdbDialog({ bdbId, bdbName, open, onClose }: ExportBdbDialogProps) {
  const [exporting, setExporting] = useState(false);
  const [passphrase, setPassphrase] = useState<string | null>(null);
  const [filename, setFilename] = useState<string | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const queryClient = useQueryClient();
  const { data: status } = useExportStatus(bdbId, open);
  const dbStatus = status?.database_export;

  const unlimited = dbStatus != null && dbStatus.max_per_window <= 0;
  const nextAvailableAt = dbStatus?.next_available_at ? new Date(dbStatus.next_available_at) : null;
  const slotFree = unlimited || (dbStatus != null && dbStatus.used_in_window < dbStatus.max_per_window);

  function handleClose() {
    setPassphrase(null);
    setFilename(null);
    setErrorMessage(null);
    setCopied(false);
    onClose();
  }

  function handleStart() {
    setExporting(true);
    setErrorMessage(null);
    downloadDatabaseExport(bdbId)
      .then((result) => {
        setPassphrase(result.passphrase ?? null);
        setFilename(result.filename);
      })
      .catch((err) => {
        setErrorMessage(err instanceof Error ? err.message : "Export failed");
      })
      .finally(() => {
        setExporting(false);
        queryClient.invalidateQueries({ queryKey: ["export-status", bdbId] });
      });
  }

  function handleCopy() {
    if (!passphrase) return;
    navigator.clipboard.writeText(passphrase).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  }

  return (
    <Dialog open={open} onClose={handleClose} title={`Export ${bdbName}`}>
      {passphrase == null ? (
        <div className={styles.body}>
          <p className={styles.explain}>
            Downloads a full copy of this bdb's database as an encrypted SQLite file. It opens in any
            SQLCipher-aware tool with a one-time passphrase shown after the export — the passphrase is
            never stored, so it cannot be recovered later.
          </p>
          {dbStatus && (
            <p className={styles.quota}>
              {unlimited
                ? "Exports: unlimited"
                : `Exports: ${dbStatus.used_in_window} of ${dbStatus.max_per_window} used in the last ${formatWindow(dbStatus.window_seconds)}`}
              {nextAvailableAt && (
                <span className={styles.quotaNext}>
                  {" "}
                  — next available {nextAvailableAt.toLocaleString()}
                </span>
              )}
            </p>
          )}
          {errorMessage && <div className={styles.error}>{errorMessage}</div>}
          <div className={styles.actions}>
            <Button onClick={handleStart} loading={exporting} disabled={dbStatus == null || !slotFree}>
              {exporting ? "Preparing encrypted copy…" : "Start export"}
            </Button>
          </div>
        </div>
      ) : (
        <div className={styles.body}>
          <p className={styles.explain}>
            {filename ? `Downloaded ${filename}.` : "Download complete."} This passphrase opens the file
            and is shown only once — copy it now:
          </p>
          <div className={styles.passphraseRow}>
            <input
              type="text"
              readOnly
              value={passphrase}
              className={styles.passphraseInput}
              onFocus={(e) => e.target.select()}
            />
            <Button size="sm" variant="secondary" onClick={handleCopy}>
              {copied ? "Copied!" : "Copy"}
            </Button>
          </div>
          <p className={styles.hint}>
            Open with an SQLCipher-aware tool (e.g. DB Browser for SQLCipher) and enter the passphrase
            when prompted.
          </p>
          <div className={styles.actions}>
            <Button variant="secondary" onClick={handleClose}>
              Done
            </Button>
          </div>
        </div>
      )}
    </Dialog>
  );
}

function formatWindow(seconds: number): string {
  const hours = Math.round(seconds / 3600);
  if (hours >= 24 && hours % 24 === 0) {
    const days = hours / 24;
    return days === 1 ? "24h" : `${days} days`;
  }
  return `${hours}h`;
}
