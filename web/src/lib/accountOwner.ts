import type { AdminUser } from "@/api/types";

// The server Owner is the account that set up the server, or the one ownership
// was last transferred to. Only the Owner may grant the admin role, change an
// admin's access policy, or change, reset, or delete another admin; nobody
// else may change the Owner's account,
// the Owner itself cannot be deleted, and only the Owner may view as another
// admin. The server enforces all of this; these helpers only keep the admin UI
// from offering refused actions.

/** Whether the viewer may edit, reset, or manage API keys for the target account. */
export function canManageAccount(
  target: AdminUser,
  viewerId: number | undefined,
  viewerIsOwner: boolean,
): boolean {
  if (target.id === viewerId) return true;
  if (target.is_owner) return false;
  return target.role !== "admin" || viewerIsOwner;
}

/**
 * Whether the viewer may change the target's access policy: libraries,
 * playback limits, downloads and requests. Only the Owner changes an admin's,
 * including an admin's own.
 */
export function canChangeAccessPolicy(
  target: AdminUser,
  viewerId: number | undefined,
  viewerIsOwner: boolean,
): boolean {
  return (
    canManageAccount(target, viewerId, viewerIsOwner) && (target.role !== "admin" || viewerIsOwner)
  );
}

/** Whether the viewer may make the target account the server Owner. */
export function canTransferOwnership(
  target: AdminUser,
  viewerId: number | undefined,
  viewerIsOwner: boolean,
): boolean {
  return (
    viewerIsOwner &&
    target.id !== viewerId &&
    !target.is_owner &&
    target.role === "admin" &&
    target.enabled
  );
}

/** Whether the viewer may view the server as the target account. */
export function canViewAsAccount(
  target: AdminUser,
  viewerId: number | undefined,
  viewerIsOwner: boolean,
): boolean {
  return (
    target.enabled &&
    !target.is_owner &&
    target.id !== viewerId &&
    (target.role !== "admin" || viewerIsOwner)
  );
}

/** The role shown for an account: the Owner is labeled "owner" rather than "admin". */
export function accountRoleLabel(account: AdminUser): string {
  return account.is_owner ? "owner" : account.role;
}
