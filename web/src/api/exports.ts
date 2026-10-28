/**
 * Exports API — full-database download (encrypted, one-time passphrase) and view-formula CSV download.
 * Downloads bypass the JSON client: the body is a file and the database export's passphrase arrives in a
 * response header, so both go through a shared fetch-and-save helper here.
 */

import { getConfig } from "../config";
import { ApiError } from "./client";
import { get } from "./client";

export interface DatabaseExportStatus {
  /** Whether the caller may run a full-database export (owner-only permission). */
  allowed: boolean;
  /** Successful exports allowed per rolling window; 0 or less means unlimited. */
  max_per_window: number;
  window_seconds: number;
  used_in_window: number;
  /** Set only while the window is exhausted. */
  next_available_at: string | null;
}

export interface FormulaExportStatus {
  /** Row cap for one CSV export; 0 or less means uncapped. */
  max_rows: number;
}

export interface ExportStatus {
  database_export: DatabaseExportStatus;
  formula_export: FormulaExportStatus;
}

export function getExportStatus(bdbId: string): Promise<ExportStatus> {
  return get<ExportStatus>(`/api/databases/v1/bdbs/${bdbId}/database/export/status`);
}

/** Header carrying the database export's one-time passphrase; never sent again after this response. */
const exportKeyHeader = "X-Baguettedb-Export-Key";

interface DownloadResult {
  filename: string;
  /** Set for database exports only. */
  passphrase?: string;
}

/**
 * Fetches path as a file download and hands the body to the browser's save flow. Errors surface as
 * ApiError with the server's JSON message, mirroring the JSON client.
 */
async function download(path: string): Promise<DownloadResult> {
  const { apiUrl } = getConfig();
  const res = await fetch(`${apiUrl}${path}`, { credentials: "include" });

  if (!res.ok) {
    let message = `Request failed with status ${res.status}`;
    try {
      const err: { error: string } = await res.json();
      message = err.error;
    } catch {
      // Response body wasn't JSON
    }
    throw new ApiError(res.status, message);
  }

  const filename = filenameFromDisposition(res.headers.get("Content-Disposition")) ?? "export";
  const passphrase = res.headers.get(exportKeyHeader) ?? undefined;
  saveBlob(await res.blob(), filename);
  return { filename, passphrase };
}

/** Downloads the bdb's full database as an encrypted file, returning the one-time passphrase. */
export function downloadDatabaseExport(bdbId: string): Promise<DownloadResult> {
  return download(`/api/databases/v1/bdbs/${bdbId}/database/export`);
}

/** Downloads a view formula's full result as CSV, with params as query-string values. */
export function downloadFormulaCSV(
  bdbId: string,
  ref: string,
  params: Record<string, string>,
): Promise<DownloadResult> {
  const query = new URLSearchParams(params).toString();
  const suffix = query ? `?${query}` : "";
  return download(`/api/databases/v1/bdbs/${bdbId}/formulas/${ref}/export.csv${suffix}`);
}

function filenameFromDisposition(disposition: string | null): string | null {
  if (!disposition) return null;
  const match = /filename="([^"]+)"/.exec(disposition);
  return match ? match[1] : null;
}

function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}
