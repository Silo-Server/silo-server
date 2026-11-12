import { Ellipsis, History, Pencil, Trash2, UserRound } from "lucide-react";
import { Link } from "react-router";

import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useT } from "@/i18n";

import { AdminUserRoleBadge, AdminUserStatusBadge } from "./AdminUserRowParts";
import type { AdminUserRow } from "./useAdminUserRows";

// Not translated: a separator glyph.
const SEPARATOR = "·";

// Menu rows tall enough to tap on a phone.
const MENU_ITEM_CLASS = "min-h-11";

/**
 * The admin users list below the `lg` breakpoint: one compact row per account
 * in a single panel, each row a link to the account with its actions in a menu.
 */
export function AdminUserCompactList({ rows }: { rows: AdminUserRow[] }) {
  const t = useT();
  if (rows.length === 0) {
    return (
      <p className="text-muted-foreground px-4 py-8 text-center text-sm break-words">
        {t("web.admin.users.list.noMatches")}
      </p>
    );
  }
  return (
    <ul className="divide-border/60 divide-y">
      {rows.map((row) => (
        <AdminUserCompactRow key={row.user.id} row={row} />
      ))}
    </ul>
  );
}

function AdminUserCompactRow({ row }: { row: AdminUserRow }) {
  const t = useT();
  const { user, group } = row;
  return (
    <li className="flex items-start">
      {/* The menu sits beside the link, not inside it, so the two never nest. */}
      <Link
        to={row.detailPath}
        className="hover:bg-accent/40 focus-visible:ring-ring/50 min-w-0 flex-1 space-y-0.5 py-2.5 ps-4 pe-1 outline-none focus-visible:ring-[3px] focus-visible:ring-inset"
      >
        <span className="flex min-h-6 items-center gap-1.5">
          <span className="min-w-0 flex-1 truncate font-medium">{user.username}</span>
          <AdminUserRoleBadge user={user} />
          <AdminUserStatusBadge user={user} />
        </span>
        {user.email ? (
          <span className="text-muted-foreground block truncate text-sm">{user.email}</span>
        ) : null}
        {/* The group name gives way first, so the last-active time stays whole. */}
        <span className="text-muted-foreground flex gap-1 text-xs">
          {group.kind === "admin" ? null : (
            <>
              <span className="truncate">
                {group.kind === "group" ? group.name : t("web.admin.users.list.noGroup")}
              </span>
              <span aria-hidden="true">{SEPARATOR}</span>
            </>
          )}
          <span className="shrink-0" title={row.lastActiveTitle || undefined}>
            {row.lastActive}
          </span>
        </span>
      </Link>
      <AdminUserRowMenu row={row} />
    </li>
  );
}

function AdminUserRowMenu({ row }: { row: AdminUserRow }) {
  const t = useT();
  const { username } = row.user;
  return (
    // Not modal, like the libraries list menu, so an item can open a dialog.
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="text-muted-foreground data-[state=open]:bg-accent data-[state=open]:text-foreground me-1 mt-0.5 size-11 shrink-0 rounded-[10px]"
          aria-label={t("web.admin.users.list.moreActions", { username })}
        >
          <Ellipsis className="size-4" aria-hidden="true" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-64 max-w-[calc(100vw-2rem)]">
        <DropdownMenuItem className={MENU_ITEM_CLASS} asChild>
          <Link to={row.historyPath}>
            <History aria-hidden="true" />
            {t("web.admin.users.list.historyAria", { username })}
          </Link>
        </DropdownMenuItem>
        {row.viewAs ? (
          <DropdownMenuItem className={MENU_ITEM_CLASS} onSelect={row.viewAs}>
            <UserRound aria-hidden="true" />
            {t("web.admin.users.list.viewAsAria", { username })}
          </DropdownMenuItem>
        ) : null}
        {row.editPath ? (
          <DropdownMenuItem className={MENU_ITEM_CLASS} asChild>
            <Link to={row.editPath}>
              <Pencil aria-hidden="true" />
              {t("web.admin.users.list.editAria", { username })}
            </Link>
          </DropdownMenuItem>
        ) : null}
        {row.remove ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              className={MENU_ITEM_CLASS}
              variant="destructive"
              onSelect={row.remove}
            >
              <Trash2 aria-hidden="true" />
              {t("web.admin.users.list.deleteAria", { username })}
            </DropdownMenuItem>
          </>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
