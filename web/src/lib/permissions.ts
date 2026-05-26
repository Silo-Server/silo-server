import type { User } from "@/api/types";

export const PERMISSION_METADATA_CURATION = "metadata_curation";

export function hasPermission(
  user: Pick<User, "role" | "permissions"> | null | undefined,
  permission: string,
) {
  if (!user) return false;
  if (user.role === "admin") return true;
  return Array.isArray(user.permissions) && user.permissions.includes(permission);
}

export function canCurateMetadata(user: Pick<User, "role" | "permissions"> | null | undefined) {
  return hasPermission(user, PERMISSION_METADATA_CURATION);
}
