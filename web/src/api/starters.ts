/** Starters API — the built-in starter catalog and bdb-from-starter creation. */

import { get, post } from "./client";
import type { Bdb } from "./bdbs";

/** One formula on a starter's catalog card — name and description only. */
export interface StarterFormulaPreview {
  name: string;
  /** Human name shown instead of the slug; absent means the name is shown. */
  label?: string;
  description: string;
  /** True for a formula attached to a view — an action, not a view of its own. */
  attached?: boolean;
  /** True for a curation-hidden formula. */
  hidden?: boolean;
}

/** One skill on a starter's catalog card. */
export interface StarterSkillPreview {
  name: string;
  description: string;
}

/** One catalog entry: a curated starter a new bdb can be seeded from. */
export interface Starter {
  key: string;
  version: number;
  name: string;
  description: string;
  icon?: string;
  tags?: string[];
  formulas: StarterFormulaPreview[];
  skills: StarterSkillPreview[];
}

/** Name and description override the starter's defaults when non-empty. */
export interface CreateFromStarterRequest {
  starter_key: string;
  name?: string;
  description?: string;
}

export function listStarters(): Promise<Starter[]> {
  return get<Starter[]>("/api/bdbs/v1/starters");
}

export function createBdbFromStarter(data: CreateFromStarterRequest): Promise<Bdb> {
  return post<Bdb>("/api/bdbs/v1/bdbs/from-starter", data);
}
