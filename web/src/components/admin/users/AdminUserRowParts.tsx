import type { AdminUser } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { useT } from "@/i18n";
import { accountRoleLabel } from "@/lib/accountOwner";

/** The account's role (Owner, Admin, User), as the users table and compact list show it. */
export function AdminUserRoleBadge({ user }: { user: AdminUser }) {
  return (
    <Badge variant={user.role === "admin" ? "default" : "secondary"}>
      {accountRoleLabel(user)}
    </Badge>
  );
}

/** Whether the account can sign in. */
export function AdminUserStatusBadge({ user }: { user: AdminUser }) {
  const t = useT();
  return (
    <Badge variant={user.enabled ? "outline" : "destructive"}>
      {user.enabled
        ? t("web.admin.users.list.status.active")
        : t("web.admin.users.list.status.disabled")}
    </Badge>
  );
}
