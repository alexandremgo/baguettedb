/** Data API keys API: a bdb's keys, each letting an app of its owner's run the formulas picked for it through the Data
 *  API. Only the bdb's owner manages them. */

import { del, get, post, put } from "./client";

const BASE = "/api/bdbs/v1/bdbs";

/** Kind prefix every Data API key's value starts with. */
export const DATA_API_KEY_PREFIX = "bdbdk_";

/** A Data API key as the API lists it, never with its value. */
export interface DataAPIKey {
  id: string;
  name: string;
  /** Start of the value after DATA_API_KEY_PREFIX, enough to tell the key apart. */
  token_prefix: string;
  created_by: string;
  created_at: string;
  last_used_at: string | null;
  /** IDs of the formulas the key may run, in the order they were added. */
  formula_ids: string[];
}

/** A key just created, with its value: the only time the API returns it. */
export interface CreatedDataAPIKey extends DataAPIKey {
  token: string;
}

function keysPath(bdbId: string): string {
  return `${BASE}/${encodeURIComponent(bdbId)}/data-api-keys`;
}

/** Lists the bdb's keys that are not revoked, newest first. */
export async function listDataAPIKeys(bdbId: string): Promise<DataAPIKey[]> {
  const res = await get<{ keys: DataAPIKey[] }>(keysPath(bdbId));
  return res.keys;
}

/** Creates a key named name for the bdb, allowed to run the formulas formulaIds, which may be none. */
export function createDataAPIKey(bdbId: string, name: string, formulaIds: string[]): Promise<CreatedDataAPIKey> {
  return post<CreatedDataAPIKey>(keysPath(bdbId), { name, formula_ids: formulaIds });
}

export function revokeDataAPIKey(bdbId: string, keyId: string): Promise<void> {
  return del(`${keysPath(bdbId)}/${encodeURIComponent(keyId)}`);
}

/** Lets the key run the formula formulaId; a formula the key already has is left as is. */
export function addDataAPIKeyFormula(bdbId: string, keyId: string, formulaId: string): Promise<void> {
  return put<void>(`${keysPath(bdbId)}/${encodeURIComponent(keyId)}/formulas/${encodeURIComponent(formulaId)}`);
}

export function removeDataAPIKeyFormula(bdbId: string, keyId: string, formulaId: string): Promise<void> {
  return del(`${keysPath(bdbId)}/${encodeURIComponent(keyId)}/formulas/${encodeURIComponent(formulaId)}`);
}
