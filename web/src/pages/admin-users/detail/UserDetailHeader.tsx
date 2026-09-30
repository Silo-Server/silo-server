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
  onTransfer(): void;
  onDisable(): void;
  onEnable(): void;
  onDelete(): void;
}

/** Meta items marked `wide`, and the separator before them, only fit from `sm` up. */
const WIDE_ONLY = "hidden sm:inline";

/** Why "View as user" is off for a reachable account, most basic reason first. */
function viewAsDisabledReason(
  user: AdminUser,
  ownAccount: boolean,
  viewAsDisabled: boolean,
): string | undefined {
  if (!user.enabled) return "Enable the account first";
  if (user.is_owner) return "The server owner can't be viewed as";
  if (ownAccount) return "This is your own account";
  if (viewAsDisabled) return "Only the server owner can view as another admin";
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
  onTransfer,
  onDisable,
  onEnable,
  onDelete,
}: UserDetailHeaderProps) {
  const lastActive = user.last_active_at
    ? `Last active ${formatLastSeen(user.last_active_at)}`
    : "No recorded activity";
  const meta: Array<{ key: string; node: ReactNode; wide?: boolean }> = [
    ...(user.email ? [{ key: "email", node: user.email }] : []),
    ...(user.role !== "admin" && user.access_group_id !== null
      ? [
          {
            key: "group",
            node: (
              <>
                Group:{" "}
                <span className="text-foreground font-medium">
                  {groupName ?? `#${user.access_group_id}`}
                </span>
              </>
            ),
          },
        ]
      : []),
    { key: "joined", node: `Joined ${formatAccountDate(user.created_at)}`, wide: true },
    { key: "active", node: lastActive },
    {
      key: "signin",
      node: user.password_login ? "Password sign-in" : "External sign-in",
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
  const hasMenu = transferable || canDisable || canEnable || canDelete;
  const viewAsReason = available
    ? viewAsDisabledReason(user, ownAccount, viewAsDisabled)
    : undefined;

  return (
    <div className="space-y-5">
      <nav
        aria-label="Breadcrumb"
        className="text-muted-foreground flex items-center gap-1.5 text-sm"
      >
        <Link to="/admin" className="hover:text-foreground transition-colors">
          Admin
        </Link>
        <ChevronRight aria-hidden="true" className="h-3.5 w-3.5" />
        <Link to="/admin/users" className="hover:text-foreground transition-colors">
          Users
        </Link>
        <ChevronRight aria-hidden="true" className="h-3.5 w-3.5" />
        <span className="text-foreground min-w-0 truncate font-medium">{user.username}</span>
      </nav>

      <div className="flex flex-col gap-4 sm:flex-row sm:items-start">
        <div className="flex min-w-0 flex-1 items-start gap-4">
          <InitialAvatar name={user.username} seed={user.id} size="lg" />
          <div className="min-w-0 flex-1 space-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <h1 className="page-title min-w-0 text-[clamp(1.75rem,4vw,2.75rem)] break-words">
                {user.username}
              </h1>
              <Badge variant={user.role === "admin" ? "default" : "secondary"}>
                {roleLabel(user)}
              </Badge>
              {user.enabled ? (
                <Badge variant="outline" className="border-success/30 text-success">
                  <span aria-hidden="true" className="bg-success size-1.5 rounded-full" />
                  Active
                </Badge>
              ) : (
                <Badge variant="destructive">Disabled</Badge>
              )}
              {user.password_change_required ? (
                <Badge
                  variant="outline"
                  className="border-amber-500/30 bg-amber-500/10 text-amber-300"
                >
                  Temporary password
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
                      ·
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
                    ? "This is the server owner. Only the owner can change this account."
                    : "Only the server owner can change another admin account. You can view it."}
                </span>
              </div>
            ) : null}
            {!user.enabled ? (
              <div className="border-destructive/30 bg-destructive/10 flex flex-wrap items-center gap-3 rounded-xl border px-3 py-2.5 text-sm">
                <span className="min-w-0 flex-1 basis-56">
                  This account can't sign in. Its profiles and history are kept.
                </span>
                {manageable && available ? (
                  <Button type="button" variant="outline" size="sm" onClick={onEnable}>
                    Enable account
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
              View as user
            </Button>
          </span>
          {/* An external provider manages this account's sign-in: it has no password to reset. */}
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
                Reset password
              </Button>
            </span>
          ) : null}
          {hasMenu ? (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button type="button" variant="outline" size="icon-sm" aria-label="More actions">
                  <MoreHorizontal />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="min-w-52">
                {transferable ? (
                  <DropdownMenuItem disabled={!available} onSelect={onTransfer}>
                    Make owner
                    <span className="text-muted-foreground ml-auto text-xs">owner only</span>
                  </DropdownMenuItem>
                ) : null}
                {canDisable ? (
                  <DropdownMenuItem disabled={!available} onSelect={onDisable}>
                    Disable account
                    <span className="text-muted-foreground ml-auto text-xs">keeps data</span>
                  </DropdownMenuItem>
                ) : null}
                {canEnable ? (
                  <DropdownMenuItem disabled={!available} onSelect={onEnable}>
                    Enable account
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
                      Delete account…
                      {deleteBlocked ? (
                        <span className="text-muted-foreground ml-auto text-xs">
                          save or cancel edits first
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
