import type { AdminUser } from "@/api/types";
import { useT } from "@/i18n";
import { canManageAccount, canViewAsAccount } from "@/lib/accountOwner";
import { userDetailTabSearch } from "@/pages/admin-users/detail/userDetailTabs";

import { formatFullDateTime, formatRelativeTime } from "./adminUserList";

/** Where a row's access group stands: admins can't join one, others may have none. */
export type AdminUserRowGroup =
  | { kind: "admin" }
  | { kind: "none" }
  | { kind: "group"; id: number; name: string };

/**
 * Everything the users table and the compact list show for one account, and
 * the row actions the viewer may take on it. Both layouts read this, so the
 * permission rules live in one place.
 */
export interface AdminUserRow {
  user: AdminUser;
  detailPath: string;
  group: AdminUserRowGroup;
  lastActive: string;
  /** The full timestamp behind `lastActive`, for a hover title; empty when never active. */
  lastActiveTitle: string;
  historyPath: string;
  /** Set when the viewer may manage the account. */
  editPath: string | null;
  /** Shown only when the viewer may view as this account. */
  viewAs: (() => void) | null;
  /** Shown only when the viewer may delete this account. */
  remove: (() => void) | null;
}

export function useAdminUserRows({
  viewerId,
  viewerIsOwner,
  available,
  groupNames,
  onViewAs,
  onDelete,
}: {
  viewerId: number | undefined;
  viewerIsOwner: boolean;
  /** Whether user administration is available on this server. */
  available: boolean;
  groupNames: Map<string, string>;
  onViewAs: (user: AdminUser) => void;
  onDelete: (user: AdminUser) => void;
}): (user: AdminUser) => AdminUserRow {
  const t = useT();
  return (u: AdminUser): AdminUserRow => {
    const canManage = canManageAccount(u, viewerId, viewerIsOwner);
    return {
      user: u,
      detailPath: `/admin/users/${u.id}`,
      group:
        u.role === "admin"
          ? { kind: "admin" }
          : u.access_group_id == null
            ? { kind: "none" }
            : {
                kind: "group",
                id: u.access_group_id,
                name:
                  groupNames.get(String(u.access_group_id)) ??
                  t("web.admin.users.list.unknownGroup"),
              },
      lastActive: formatRelativeTime(u.last_active_at, t("web.admin.users.list.lastActiveNever")),
      lastActiveTitle: u.last_active_at ? formatFullDateTime(u.last_active_at) : "",
      historyPath: `/admin/history?user_id=${u.id}`,
      editPath: canManage ? `/admin/users/${u.id}${userDetailTabSearch("access")}` : null,
      viewAs: available && canViewAsAccount(u, viewerId, viewerIsOwner) ? () => onViewAs(u) : null,
      remove: !u.is_owner && u.id !== viewerId && canManage ? () => onDelete(u) : null,
    };
  };
}
