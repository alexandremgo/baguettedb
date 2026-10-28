/** Skills API — client for the per-bdb skill markdown documents. */

import { del, get } from "./client";

export interface Skill {
  id: string;
  name: string;
  description: string;
  body: string;
  target_tables: string[];
  created_at: string;
  updated_at: string;
}

function basePath(bdbId: string): string {
  return `/api/databases/v1/bdbs/${bdbId}/skills`;
}

export function listSkills(bdbId: string): Promise<Skill[]> {
  return get<Skill[]>(basePath(bdbId));
}

export function getSkill(bdbId: string, ref: string): Promise<Skill> {
  return get<Skill>(`${basePath(bdbId)}/${encodeURIComponent(ref)}`);
}

export function deleteSkill(bdbId: string, ref: string): Promise<void> {
  return del(`${basePath(bdbId)}/${encodeURIComponent(ref)}`);
}
