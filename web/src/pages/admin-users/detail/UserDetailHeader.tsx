import { Fragment, type ReactNode } from "react";
import { Link } from "react-router";
import { ChevronRight, Info, MoreHorizontal } from "lucide-react";

import type { AdminUser } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useT, type MessageKey } from "@/i18n";
import { cn } from "@/lib/utils";

import { roleLabel } from "./access/policySources";
import { useCardEditing } from "./cardEditing";
import { formatAccountDate, formatLastSeen } from "./format";
import { InitialAvatar } from "./ui";

export interface UserDetailHeaderProps {
  user: AdminUser;
  /** The account's access group name; shown for a grouped non-admin. */
  groupName?: string;
  manageable: boolean;
  available: boolean;
  viewAsDisabled: boolean;
  transferable: boolean;
  ownAccount: boolean;
  onViewAs(): void;
  onResetPassword(): void;
  /** Opens the Sign-in tab's Set password dialog, for an account without password sign-in. */
  onSetPassword(): void;
  onTransfer(): void;
  onDisable(): void;
  onEnable(): void;
  onDelete(): void;
}

/** Meta items marked `wide`, and the separator before them, only fit from `sm` up. */
const WIDE_ONLY = "hidden sm:inline";
/** The separator between meta items; punctuation, not text to translate. */
const MIDDLE_DOT = "·";

/** Why "View as user" is off for a reachable account, most basic reason first. */
function viewAsDisabledReason(
  user: AdminUser,
  ownAccount: boolean,
  viewAsDisabled: boolean,
): MessageKey | undefined {
  if (!user.enabled) return "web.admin.users.detail.header.viewAsReason.enableFirst";
  if (user.is_owner) return "web.admin.users.detail.header.viewAsReason.owner";
  if (ownAccount) return "web.admin.users.detail.header.viewAsReason.ownAccount";
  if (viewAsDisabled) return "web.admin.users.detail.header.viewAsReason.adminOwnerOnly";
  return undefined;
}

/**
 * The account's identity (name, role, status, email, group, dates, sign-in),
 * why it may be view-only or disabled, and its actions.
 */
export function UserDetailHeader({
  user,
  groupName,
  manageable,
  available,
  viewAsDisabled,
  transferable,
  ownAccount,
  onViewAs,
  onResetPassword,
  onSetPassword,
  onTransfer,
  onDisable,
  onEnable,
  onDelete,
}: UserDetailHeaderProps) {
  const t = useT();
  const lastActive = user.last_active_at
    ? t("web.admin.users.detail.header.lastActive", { when: formatLastSeen(user.last_active_at) })
    : t("web.admin.users.detail.header.noActivity");
  const meta: Array<{ key: string; node: ReactNode; wide?: boolean }> = [
    ...(user.email ? [{ key: "email", node: user.email }] : []),
    ...(user.role !== "admin" && user.access_group_id !== null
      ? [
          {
            key: "group",
            node: t("web.admin.users.detail.header.group", {
              name:
                groupName ??
                t("web.admin.users.detail.header.groupIdFallback", {
                  id: String(user.access_group_id),
                }),
              b: (chunks) => <span className="text-foreground font-medium">{chunks}</span>,
            }),
          },
        ]
      : []),
    {
      key: "joined",
      node: t("web.admin.users.detail.header.joined", { date: formatAccountDate(user.created_at) }),
      wide: true,
    },
    { key: "active", node: lastActive },
    {
      key: "signin",
      node: user.password_login
        ? t("web.admin.users.detail.header.passwordSignIn")
        : t("web.admin.users.detail.header.externalSignIn"),
      wide: true,
    },
  ];

  const canDisable = manageable && user.enabled && !user.is_owner && !ownAccount;
  const canEnable = manageable && !user.enabled;
  const canDelete = manageable && !user.is_owner && !ownAccount;
  // Deleting navigates away, which the unsaved-changes guard would block with a
  // prompt to save a draft for an account that no longer exists.
  const { active: activeEdit } = useCardEditing();
  const deleteBlocked = activeEdit !== null && activeEdit.changeCount > 0;
  const showReset = user.password_login && manageable;
  const showSetPassword = !user.password_login && manageable;
  const hasMenu = transferable || canDisable || canEnable || canDelete;
  const viewAsReasonKey = available
    ? viewAsDisabledReason(user, ownAccount, viewAsDisabled)
    : undefined;
  const viewAsReason = viewAsReasonKey ? t(viewAsReasonKey) : undefined;

  return (
    <div className="space-y-5">
      <nav
        aria-label={t("web.admin.users.detail.header.breadcrumb")}
        className="text-muted-foreground flex items-center gap-1.5 text-sm"
      >
        <Link to="/admin" className="hover:text-foreground transition-colors">
          {t("web.admin.users.detail.header.breadcrumbAdmin")}
        </Link>
        <ChevronRight aria-hidden="true" className="h-3.5 w-3.5" />
        <Link to="/admin/users" className="hover:text-foreground transition-colors">
          {t("web.admin.users.detail.header.breadcrumbUsers")}
        </Link>
        <ChevronRight aria-hidden="true" className="h-3.5 w-3.5" />
        <span className="text-foreground min-w-0 truncate font-medium">{user.username}</span>
      </nav>

      <div className="flex flex-col gap-4 sm:flex-row sm:items-start">
        <div className="flex min-w-0 flex-1 items-start gap-4">
          <InitialAvatar name={user.username} seed={user.id} size="lg" />
          <div className="min-w-0 flex-1 space-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <h1 className="min-w-0 text-[1.75rem] leading-[1.15] font-bold tracking-[-0.035em] text-balance break-words sm:text-[2rem]">
                {user.username}
              </h1>
              <Badge variant={user.role === "admin" ? "default" : "secondary"}>
                {roleLabel(user)}
              </Badge>
              {user.enabled ? (
                <Badge variant="outline" className="border-success/30 text-success">
                  <span aria-hidden="true" className="bg-success size-1.5 rounded-full" />
                  {t("web.admin.users.detail.header.status.active")}
                </Badge>
              ) : (
                <Badge variant="destructive">
                  {t("web.admin.users.detail.header.status.disabled")}
                </Badge>
              )}
              {user.password_change_required ? (
                <Badge
                  variant="outline"
                  className="border-amber-500/30 bg-amber-500/10 text-amber-300"
                >
                  {t("web.admin.users.detail.header.temporaryPassword")}
                </Badge>
              ) : null}
            </div>
            <p className="text-muted-foreground flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
              {meta.map((item, index) => (
                <Fragment key={item.key}>
                  {index > 0 ? (
                    <span
                      aria-hidden="true"
                      className={cn("text-muted-foreground/50", item.wide && WIDE_ONLY)}
                    >
                      {MIDDLE_DOT}
                    </span>
                  ) : null}
                  <span className={cn(item.wide && WIDE_ONLY)}>{item.node}</span>
                </Fragment>
              ))}
            </p>
            {!manageable ? (
              <div className="border-info/25 bg-info/5 text-muted-foreground flex items-start gap-2 rounded-xl border px-3 py-2.5 text-sm">
                <Info aria-hidden="true" className="text-info mt-0.5 size-4 shrink-0" />
                <span>
                  {user.is_owner
                    ? t("web.admin.users.detail.header.ownerNotice")
                    : t("web.admin.users.detail.header.adminNotice")}
                </span>
              </div>
            ) : null}
            {!user.enabled ? (
              <div className="border-destructive/30 bg-destructive/10 flex flex-wrap items-center gap-3 rounded-xl border px-3 py-2.5 text-sm">
                <span className="min-w-0 flex-1 basis-56">
                  {t("web.admin.users.detail.header.disabledNotice")}
                </span>
                {manageable && available ? (
                  <Button type="button" variant="outline" size="sm" onClick={onEnable}>
                    {t("web.admin.users.detail.header.enableAccount")}
                  </Button>
                ) : null}
              </div>
            ) : null}
          </div>
        </div>

        <div className="flex w-full items-center gap-2 sm:w-auto sm:shrink-0">
          {/* A disabled button gets no pointer events, so its wrapper carries the reason. */}
          <span title={viewAsReason} className="flex flex-1 sm:flex-none">
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="flex-1"
              onClick={onViewAs}
              disabled={!available || viewAsDisabled}
              aria-description={viewAsReason}
            >
              {t("web.admin.users.detail.header.viewAs")}
            </Button>
          </span>
          {/* A reset link needs password sign-in on. An account without it (one a
              sign-in provider manages) gets a password set directly on the
              Sign-in tab, and setting one turns its password sign-in back on. */}
          {showReset ? (
            <span className="flex flex-1 sm:flex-none">
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="flex-1"
                onClick={onResetPassword}
                disabled={!available}
              >
                {t("web.admin.users.detail.header.resetPassword")}
              </Button>
            </span>
          ) : null}
          {showSetPassword ? (
            <span className="flex flex-1 sm:flex-none">
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="flex-1"
                onClick={onSetPassword}
                disabled={!available || !user.enabled}
              >
                {t("web.admin.users.detail.header.setPassword")}
              </Button>
            </span>
          ) : null}
          {hasMenu ? (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  type="button"
                  variant="outline"
                  size="icon-sm"
                  aria-label={t("web.admin.users.detail.header.moreActions")}
                >
                  <MoreHorizontal />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="min-w-52">
                {transferable ? (
                  <DropdownMenuItem disabled={!available} onSelect={onTransfer}>
                    {t("web.admin.users.detail.header.makeOwner")}
                    <span className="text-muted-foreground ms-auto text-xs">
                      {t("web.admin.users.detail.header.makeOwnerHint")}
                    </span>
                  </DropdownMenuItem>
                ) : null}
                {canDisable ? (
                  <DropdownMenuItem disabled={!available} onSelect={onDisable}>
                    {t("web.admin.users.detail.header.disableAccount")}
                    <span className="text-muted-foreground ms-auto text-xs">
                      {t("web.admin.users.detail.header.disableHint")}
                    </span>
                  </DropdownMenuItem>
                ) : null}
                {canEnable ? (
                  <DropdownMenuItem disabled={!available} onSelect={onEnable}>
                    {t("web.admin.users.detail.header.enableAccount")}
                  </DropdownMenuItem>
                ) : null}
                {canDelete ? (
                  <>
                    {transferable || canDisable || canEnable ? <DropdownMenuSeparator /> : null}
                    <DropdownMenuItem
                      variant="destructive"
                      disabled={!available || deleteBlocked}
                      onSelect={onDelete}
                    >
                      {t("web.admin.users.detail.header.deleteAccount")}
                      {deleteBlocked ? (
                        <span className="text-muted-foreground ms-auto text-xs">
                          {t("web.admin.users.detail.header.deleteBlockedHint")}
                        </span>
                      ) : null}
                    </DropdownMenuItem>
                  </>
                ) : null}
              </DropdownMenuContent>
            </DropdownMenu>
          ) : null}
        </div>
      </div>
    </div>
  );
}
