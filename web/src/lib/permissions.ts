import type { Profile, User } from "@/api/types";

export const PERMISSION_METADATA_CURATION = "metadata_curation";
export const PERMISSION_MARKER_EDIT = "marker_edit";

/**
 * Whether the account is currently acting with admin powers. Admin powers are
 * reserved for the admin account's primary (household parent) profile; any
 * other profile on the account (e.g. a kid's profile) is treated as a regular
 * viewer. With no profile selected (e.g. right after login) the account keeps
 * admin powers so the admin area stays reachable.
 *
 * This is the single client-side definition of the policy — route gates,
 * chrome, and per-item admin actions must all go through it (usually via the
 * useIsActingAdmin hook) so they can never disagree.
 */
export function isActingAdmin(
  user: Pick<User, "role"> | null | undefined,
  profile: Pick<Profile, "is_primary"> | null | undefined,
) {
  if (user?.role !== "admin") return false;
  return !profile || profile.is_primary === true;
}

export function hasPermission(
  user: Pick<User, "role" | "permissions"> | null | undefined,
  permission: string,
  profile: Pick<Profile, "is_primary"> | null,
) {
  if (!user) return false;
  // The role-derived grant only applies while acting as admin; an admin on a
  // non-primary profile keeps only explicitly assigned permissions. Callers
  // must pass the resolved active profile, or explicit null for the "no
  // profile selected" case — the parameter is required so a missed call site
  // can't silently restore the admin bypass.
  if (isActingAdmin(user, profile)) return true;
  return Array.isArray(user.permissions) && user.permissions.includes(permission);
}

export function canCurateMetadata(
  user: Pick<User, "role" | "permissions"> | null | undefined,
  profile: Pick<Profile, "is_primary"> | null,
) {
  return hasPermission(user, PERMISSION_METADATA_CURATION, profile);
}

export function canEditMarkers(
  user: Pick<User, "role" | "permissions"> | null | undefined,
  profile: Pick<Profile, "is_primary"> | null,
) {
  return hasPermission(user, PERMISSION_MARKER_EDIT, profile);
}

export function hasAssignedPermission(permissions: string[] | undefined, permission: string) {
  return Array.isArray(permissions) && permissions.includes(permission);
}

export function setAssignedPermission(permissions: string[], permission: string, enabled: boolean) {
  const next = new Set(permissions);
  if (enabled) {
    next.add(permission);
  } else {
    next.delete(permission);
  }
  return Array.from(next).sort();
}
