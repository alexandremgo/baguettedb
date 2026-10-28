/** Bdbs API — CRUD for bdbs and member management. */

import { get, post, patch, del } from "./client";

/** Role a member holds in a bdb. */
export type BdbRole = "owner" | "editor" | "viewer";

export interface Member {
  user_id: string;
  email?: string;
  first_name?: string;
  last_name?: string;
  role: string;
  created_at: string;
  updated_at: string;
}

export interface Bdb {
  /** Owner member with profile data; lets listings attribute a shared bdb without scanning members. */
  owner?: Member;
  id: string;
  name: string;
  description: string;
  my_role?: BdbRole;
  members: Member[];
  created_at: string;
  updated_at: string;
  /** Set when the bdb has been soft-deleted by its owner; otherwise undefined. */
  deleted_at?: string;
  /** Date after which the soft-deleted bdb is permanently purged (deleted_at + grace period). */
  purge_at?: string;
}

/** Reports whether the current user can change the bdb's content (an editor/owner of a live bdb). */
export function bdbCanWrite(bdb: Bdb): boolean {
  return !bdb.deleted_at && (bdb.my_role === "owner" || bdb.my_role === "editor");
}

export interface CreateBdbRequest {
  name: string;
  description: string;
}

export interface UpdateBdbRequest {
  name?: string;
  description?: string;
}

export interface UpdateMemberRoleRequest {
  role: string;
}

const BASE = "/api/bdbs/v1/bdbs";

export function listBdbs(): Promise<Bdb[]> {
  return get<Bdb[]>(BASE);
}

export function getBdb(id: string): Promise<Bdb> {
  return get<Bdb>(`${BASE}/${id}`);
}

export function createBdb(data: CreateBdbRequest): Promise<Bdb> {
  return post<Bdb>(BASE, data);
}

export function updateBdb(id: string, data: UpdateBdbRequest): Promise<Bdb> {
  return patch<Bdb>(`${BASE}/${id}`, data);
}

export function deleteBdb(id: string): Promise<void> {
  return del(`${BASE}/${id}`);
}

/**
 * Rearranges the caller's own bdb list, first id at the top. Every bdb the caller can see must be
 * listed exactly once — a partial list is rejected rather than half-applied.
 */
export function reorderBdbs(bdbIds: string[]): Promise<void> {
  return patch<void>(`${BASE}/order`, { bdb_ids: bdbIds });
}

export function restoreBdb(id: string): Promise<Bdb> {
  return post<Bdb>(`${BASE}/${id}/restore`);
}

export function updateMemberRole(
  bdbId: string,
  userId: string,
  data: UpdateMemberRoleRequest,
): Promise<Bdb> {
  return patch<Bdb>(`${BASE}/${bdbId}/members/${userId}`, data);
}

export function removeMember(bdbId: string, userId: string): Promise<Bdb> {
  return del(`${BASE}/${bdbId}/members/${userId}`) as unknown as Promise<Bdb>;
}

// --- Invites ---

export interface Invite {
  id: string;
  token: string;
  invite_url: string;
  role: string;
  status: string;
  expires_at: string;
  created_at: string;
}

export interface InviteDetails {
  id: string;
  bdb_name: string;
  bdb_description: string;
  role: string;
  status: string;
  expires_at: string;
  created_by_email?: string;
}

export interface CreateInviteRequest {
  role: string;
  expires_in_hours?: number;
}

const INVITES_BASE = "/api/bdbs/v1/invites";

export function createInvite(bdbId: string, data: CreateInviteRequest): Promise<Invite> {
  return post<Invite>(`${BASE}/${bdbId}/invites`, data);
}

export function listInvites(bdbId: string): Promise<Invite[]> {
  return get<Invite[]>(`${BASE}/${bdbId}/invites`);
}

export function revokeInvite(bdbId: string, inviteId: string): Promise<void> {
  return del(`${BASE}/${bdbId}/invites/${inviteId}`);
}

export function getInviteByToken(token: string): Promise<InviteDetails> {
  return get<InviteDetails>(`${INVITES_BASE}/${token}`);
}

export function acceptInvite(token: string): Promise<Bdb> {
  return post<Bdb>(`${INVITES_BASE}/${token}/accept`);
}
