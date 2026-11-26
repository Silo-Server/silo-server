import { Fragment } from "react";

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

/**
 * An email that may break after its "@" and before its dots, so a narrow column
 * wraps "alice@" and "example.com" onto separate lines instead of cutting a word.
 * A part that still can't fit breaks wherever its cell allows.
 */
export function BreakableEmail({ email }: { email: string }) {
  const parts = email
    .replace(/@/g, "@\u0000")
    .replace(/\./g, "\u0000.")
    .split("\u0000")
    .filter(Boolean);
  return (
    <>
      {parts.map((part, position) => (
        <Fragment key={position}>
          {position > 0 ? <wbr /> : null}
          {part}
        </Fragment>
      ))}
    </>
  );
}
