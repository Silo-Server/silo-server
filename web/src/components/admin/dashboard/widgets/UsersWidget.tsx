import { Link, useNavigate } from "react-router";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type { AdminUser } from "@/api/types";
import { accountRoleLabel } from "@/lib/accountOwner";
import { formatRelativeTime } from "@/lib/date";
import { useAdminUsers } from "@/hooks/queries/admin/users";
import { SectionError, UserSkeletonRows } from "../feedback";
import { useT } from "@/i18n";

// Most recently active first; users with no recorded activity sink to the
// bottom. The list endpoint itself returns account-creation order.
function byLastActive(a: AdminUser, b: AdminUser): number {
  const at = a.last_active_at ? Date.parse(a.last_active_at) : 0;
  const bt = b.last_active_at ? Date.parse(b.last_active_at) : 0;
  if (at !== bt) {
    return bt - at;
  }
  return a.username.localeCompare(b.username);
}

const EM_DASH = "—";

export function UsersWidget() {
  const t = useT();
  const navigate = useNavigate();
  const usersQuery = useAdminUsers();
  const users = [...(usersQuery.data ?? [])].sort(byLastActive);

  return (
    <Card className="h-full">
      <CardHeader className="flex shrink-0 flex-row items-center justify-between space-y-0 pb-3">
        <CardTitle className="text-sm font-bold">
          {t("web.admin.dashboard.widgets.users.title")}
        </CardTitle>
        <Link
          to="/admin/users"
          className="text-muted-foreground hover:text-primary text-[11px] transition-colors"
        >
          {t("web.admin.dashboard.widgets.users.manage")}
        </Link>
      </CardHeader>
      <CardContent className="min-h-0 flex-1 overflow-y-auto">
        {usersQuery.isLoading ? (
          <UserSkeletonRows />
        ) : usersQuery.error ? (
          <SectionError message={t("web.admin.dashboard.widgets.users.loadFailed")} />
        ) : users.length === 0 ? (
          <div className="text-muted-foreground py-4 text-center text-sm">
            {t("web.admin.dashboard.widgets.users.empty")}
          </div>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                {/* w-full + max-w-0 makes this column take the leftover width and
                    clip long emails instead of widening the table. */}
                <TableHead className="w-full max-w-0">
                  {t("web.admin.dashboard.widgets.users.user")}
                </TableHead>
                <TableHead className="hidden sm:table-cell">
                  {t("web.admin.dashboard.widgets.users.lastActive")}
                </TableHead>
                <TableHead>{t("web.admin.dashboard.widgets.users.status")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {users.slice(0, 8).map((u) => (
                <TableRow
                  key={u.id}
                  className="hover:bg-accent/50 focus-visible:ring-ring cursor-pointer focus-visible:ring-2 focus-visible:outline-none"
                  role="link"
                  tabIndex={0}
                  aria-label={t("web.admin.dashboard.widgets.users.openUser", { name: u.username })}
                  onClick={() => navigate(`/admin/users/${u.id}`)}
                  onKeyDown={(event) => {
                    // Space scrolls the page by default; a row that behaves like
                    // a link has to swallow it before navigating.
                    if (event.key === "Enter" || event.key === " ") {
                      event.preventDefault();
                      navigate(`/admin/users/${u.id}`);
                    }
                  }}
                >
                  <TableCell className="w-full max-w-0">
                    <div className="flex items-center gap-2.5">
                      <div
                        className="text-primary-foreground flex h-7 w-7 flex-shrink-0 items-center justify-center rounded-full text-[10px] font-bold"
                        style={{ background: `var(--primary)` }}
                      >
                        {u.username.charAt(0).toUpperCase()}
                      </div>
                      <div className="min-w-0">
                        <div className="flex items-center gap-1.5">
                          <span className="truncate text-[13px] font-semibold">{u.username}</span>
                          {u.role === "admin" && (
                            <Badge variant="default" className="px-1.5 py-0 text-[9px]">
                              {accountRoleLabel(u)}
                            </Badge>
                          )}
                        </div>
                        <div className="text-muted-foreground hidden truncate text-[10px] sm:block">
                          {u.email}
                        </div>
                      </div>
                    </div>
                  </TableCell>
                  <TableCell className="text-muted-foreground hidden text-xs whitespace-nowrap sm:table-cell">
                    {formatRelativeTime(u.last_active_at ?? null, {
                      rounding: "floor",
                      justNowLabel: t("web.admin.dashboard.widgets.users.justNow"),
                    }) ?? EM_DASH}
                  </TableCell>
                  <TableCell>
                    <Badge variant={u.enabled ? "outline" : "destructive"}>
                      {u.enabled
                        ? t("web.admin.dashboard.widgets.users.active")
                        : t("web.admin.dashboard.widgets.users.disabled")}
                    </Badge>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}
