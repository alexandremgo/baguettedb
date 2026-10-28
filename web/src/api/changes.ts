/** Changes API — reverting a change batch (the undo behind one-tap checklist deletes). */

import { post } from "./client";
import type { QueryResult } from "./databases";

/** Reverts every change of the given batch (e.g. one formula run), as a new inverse batch. */
export function revertChangeBatch(bdbId: string, batchId: string): Promise<QueryResult> {
  return post<QueryResult>(
    `/api/databases/v1/bdbs/${bdbId}/database/changes/batch/${encodeURIComponent(batchId)}/revert`,
  );
}
