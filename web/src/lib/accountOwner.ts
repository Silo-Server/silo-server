import type { AdminUser } from "@/api/types";

// The server Owner is the account that set up the server. Only the Owner may
// change, reset, or delete the Owner's account, the Owner itself cannot be
// deleted, and only the Owner may view as another admin. The server enforces
// all of this; these helpers only keep the admin UI from offering refused actions.

/** Whether the viewer may edit or reset the target account. */
export function canManageAccount(target: AdminUser, viewerId: number | undefined): boolean {
  return !target.is_owner || target.id === viewerId;
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
