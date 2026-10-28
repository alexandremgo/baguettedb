/** Display helpers shared by every bdb listing surface: role badge variants and owner attribution. */

import type { Bdb } from "../api/bdbs";

export function roleBadgeVariant(role?: string) {
  if (role === "owner") return "owner" as const;
  if (role === "editor") return "editor" as const;
  if (role === "viewer") return "viewer" as const;
  return "default" as const;
}

/** Returns the role a listing row should badge — every role except "owner", the default that would repeat on
 *  almost every row. Exception is information; repetition is noise. */
export function badgedRole(role?: string): string | undefined {
  return role && role !== "owner" ? role : undefined;
}

/**
 * Returns the short owner attribution ("Emma", or the email's local part) when the viewer is not the
 * owner, and undefined for the viewer's own bdbs or when the owner is unknown.
 */
export function bdbOwnerAttribution(bdb: Bdb): string | undefined {
  if (bdb.my_role === "owner" || !bdb.owner) return undefined;
  if (bdb.owner.first_name) return bdb.owner.first_name;
  return bdb.owner.email?.split("@")[0] || undefined;
}

/** Returns the bdb name with owner attribution appended ("Workout — owned by Emma"), for tooltips. */
export function bdbTitle(bdb: Bdb): string {
  const owner = bdbOwnerAttribution(bdb);
  return owner ? `${bdb.name} — owned by ${owner}` : bdb.name;
}
