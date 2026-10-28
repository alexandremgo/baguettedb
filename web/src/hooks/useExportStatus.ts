/** Fetches a bdb's export allowances (database-export quota state + formula CSV row cap). */

import { useQuery } from "@tanstack/react-query";
import { getExportStatus } from "../api/exports";

export function useExportStatus(bdbId: string | undefined, enabled = true) {
  return useQuery({
    queryKey: ["export-status", bdbId],
    queryFn: () => getExportStatus(bdbId!),
    enabled: Boolean(bdbId) && enabled,
  });
}
