/**
 * View-formula CSV export dialog: states the row cap, then downloads the full result with the caller's
 * current parameter values. No rolling-window quota applies — the row cap is the only guard.
 */

import { useState } from "react";
import { downloadFormulaCSV } from "../../api/exports";
import type { Formula } from "../../api/formulas";
import { formulaTitle } from "../Formula/selectors";
import { useExportStatus } from "../../hooks/useExportStatus";
import { Button } from "../Button";
import { Dialog } from "../Dialog";
import styles from "./ExportDialog.module.css";

interface ExportFormulaDialogProps {
  bdbId: string;
  formula: Formula;
  /** Query-string parameter values (already stringified) to export with. */
  params: Record<string, string>;
  open: boolean;
  onClose: () => void;
}

export function ExportFormulaDialog({ bdbId, formula, params, open, onClose }: ExportFormulaDialogProps) {
  const [exporting, setExporting] = useState(false);
  const [downloadedFilename, setDownloadedFilename] = useState<string | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  const { data: status } = useExportStatus(bdbId, open);
  const maxRows = status?.formula_export.max_rows ?? 0;

  function handleClose() {
    setDownloadedFilename(null);
    setErrorMessage(null);
    onClose();
  }

  function handleStart() {
    setExporting(true);
    setErrorMessage(null);
    setDownloadedFilename(null);
    downloadFormulaCSV(bdbId, formula.id, params)
      .then((result) => setDownloadedFilename(result.filename))
      .catch((err) => setErrorMessage(err instanceof Error ? err.message : "Export failed"))
      .finally(() => setExporting(false));
  }

  return (
    <Dialog open={open} onClose={handleClose} title={`Export ${formulaTitle(formula)} as CSV`}>
      <div className={styles.body}>
        <p className={styles.explain}>
          Downloads the full result of this view with the parameter values currently entered —
          {maxRows > 0 ? ` up to ${maxRows.toLocaleString()} rows.` : " no row limit applies."}
        </p>
        {errorMessage && <div className={styles.error}>{errorMessage}</div>}
        {downloadedFilename && <p className={styles.success}>Downloaded {downloadedFilename}.</p>}
        <div className={styles.actions}>
          <Button onClick={handleStart} loading={exporting}>
            {downloadedFilename ? "Export again" : "Start export"}
          </Button>
          {downloadedFilename && (
            <Button variant="secondary" onClick={handleClose}>
              Done
            </Button>
          )}
        </div>
      </div>
    </Dialog>
  );
}
