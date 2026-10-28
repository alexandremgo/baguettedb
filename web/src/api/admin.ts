/** Admin API — user management. */

import { get, post, del } from "./client";
import type { User } from "./auth";

export interface UserListResponse {
  users: User[];
  total: number;
}

export interface CreateUserRequest {
  email: string;
  password: string;
  first_name: string;
  last_name: string;
}

export function listUsers(search: string, limit: number, offset: number): Promise<UserListResponse> {
  const params = new URLSearchParams({ limit: String(limit), offset: String(offset) });
  if (search) params.set("search", search);
  return get<UserListResponse>(`/api/auth/v1/admin/users?${params}`);
}

export function createUser(data: CreateUserRequest): Promise<User> {
  return post<User>("/api/auth/v1/admin/users", data);
}

export function getUserRoles(userID: string): Promise<{ roles: string[] }> {
  return get<{ roles: string[] }>(`/api/auth/v1/admin/users/${userID}/roles`);
}

export function assignRole(userID: string, role: string): Promise<void> {
  return post(`/api/auth/v1/admin/users/${userID}/roles`, { role });
}

export function removeRole(userID: string, role: string): Promise<void> {
  return del(`/api/auth/v1/admin/users/${userID}/roles/${role}`);
}
