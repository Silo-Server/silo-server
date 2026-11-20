import { useState, useMemo, useRef } from "react";
import type { ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
import type { AdminUser } from "@/api/types";
import {
  useAdminUsers,
  useAdminUserCapabilities,
  useViewerIsOwner,
} from "@/hooks/queries/admin/users";
import { useAdminServerSettings } from "@/hooks/queries/admin/settings";
import { useAccessGroups } from "@/hooks/queries/admin/accessGroups";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { AdminUserImpersonationDialog } from "@/components/AdminUserImpersonationDialog";
import { PageHeader } from "@/components/page/PageHeader";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Dialog, DialogTrigger } from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  ArrowRight,
  ChevronDown,
  ChevronUp,
  History,
  UserRound,
  Plus,
  Pencil,
  Trash2,
  Settings2,
  Search,
  X,
} from "lucide-react";
import { AdminUserDeleteDialog } from "@/components/AdminUserDeleteDialog";
import { useAuth } from "@/hooks/useAuth";
import {
  adminUserScope,
  captureAdminUserAuthority,
  getAdminUser,
  type AdminUserEditor,
} from "@/api/v2/adminUsers";
import { Skeleton } from "@/components/ui/skeleton";
import InvitationsTab from "./admin-settings/InvitationsTab";
import InviteCodesTab from "./admin-settings/InviteCodesTab";
import {
  SortControl,
  TOUCH_CONTROL_CLASS,
} from "@/components/admin/libraries/DiagnosticsListParts";
import { useLibrariesTableLayout } from "@/components/admin/libraries/diagnosticsListState";
import { AdminUserCompactList } from "@/components/admin/users/AdminUserCompactList";
import {
  AdminUserRoleBadge,
  AdminUserStatusBadge,
} from "@/components/admin/users/AdminUserRowParts";
import {
  USER_SORT_FIELDS,
  defaultSortDirection,
  formatDateTime,
  formatFullDateTime,
  sortAdminUsers,
  type SortDirection,
  type UserSortField,
} from "@/components/admin/users/adminUserList";
import { CreateUserDialogContent } from "@/components/admin/users/CreateUserForm";
import { useAdminUserRows } from "@/components/admin/users/useAdminUserRows";
import { useT } from "@/i18n";
import { cn } from "@/lib/utils";

const PAGE_SIZE_OPTIONS = ["25", "50", "100"] as const;

// Tab ids are a URL contract: other pages deep-link here (General settings
// points at ?tab=invite-codes), so reuse the trigger values verbatim.
const ADMIN_USERS_TABS = ["users", "invitations", "invite-codes"] as const;
type AdminUsersTab = (typeof ADMIN_USERS_TABS)[number];

function normalizeAdminUsersTab(value: string | null): AdminUsersTab {
  return ADMIN_USERS_TABS.includes(value as AdminUsersTab) ? (value as AdminUsersTab) : "users";
}

export default function AdminUsers() {
  useAuth();
  return <AdminUsersPage key={adminUserScope()} />;
}
function AdminUsersPage() {
  const t = useT();
  const usersQuery = useAdminUsers();
  const { data: users = [], isLoading } = usersQuery;
  const viewerId = useAuth().user?.id;
  const viewerIsOwner = useViewerIsOwner(viewerId);
  const capabilities = useAdminUserCapabilities();
  const available = capabilities.data?.available === true;
  const [authority] = useState(captureAdminUserAuthority);
  const [actionError, setActionError] = useState("");
  const busy = useRef(false);
  const formBusy = useRef(false);
  const { data: serverSettings } = useAdminServerSettings();
  const signupsEnabled = serverSettings?.["signup.enabled"] === "true";
  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab = normalizeAdminUsersTab(searchParams.get("tab"));
  const [dialogOpen, setDialogOpen] = useState(false);
  const [confirmDeleteUser, setConfirmDeleteUser] = useState<AdminUserEditor | null>(null);
  const [impersonatingUser, setImpersonatingUser] = useState<AdminUser | null>(null);
  const [search, setSearch] = useState("");
  // "all", "none" (regular accounts outside every group), or a group id.
  const [groupFilter, setGroupFilter] = useState("all");
  const accessGroupsQuery = useAccessGroups();
  const accessGroups = useMemo(() => accessGroupsQuery.data ?? [], [accessGroupsQuery.data]);
  const groupNames = useMemo(
    () => new Map(accessGroups.map((group) => [String(group.id), group.name])),
    [accessGroups],
  );
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(25);
  const [sortField, setSortField] = useState<UserSortField>("username");
  const [sortDir, setSortDir] = useState<SortDirection>("asc");
  // Tables from lg up, compact rows below; tests have no matchMedia and get the table.
  const isDesktop = useLibrariesTableLayout();

  const filteredUsers = useMemo(() => {
    const q = search.toLowerCase();
    return users.filter((u) => {
      if (q && !(u.username?.toLowerCase().includes(q) || u.email?.toLowerCase().includes(q))) {
        return false;
      }
      if (groupFilter === "all") return true;
      // Admin accounts can't join groups, so they only appear under "All groups".
      if (u.role === "admin") return false;
      if (groupFilter === "none") return u.access_group_id == null;
      return String(u.access_group_id) === groupFilter;
    });
  }, [users, search, groupFilter]);

  const sortedUsers = useMemo(
    () => sortAdminUsers(filteredUsers, sortField, sortDir),
    [filteredUsers, sortField, sortDir],
  );

  const total = sortedUsers.length;
  const paginatedUsers = sortedUsers.slice(page * pageSize, (page + 1) * pageSize);

  function handleSort(field: UserSortField) {
    setPage(0);
    if (field === sortField) {
      setSortDir((current) => (current === "asc" ? "desc" : "asc"));
      return;
    }
    setSortField(field);
    setSortDir(defaultSortDirection(field));
  }

  // The Sort control below lg: picking a field acts like clicking its header.
  function handleSortFieldChange(field: UserSortField) {
    if (field !== sortField) handleSort(field);
  }

  function handleSortDirChange(dir: SortDirection) {
    setPage(0);
    setSortDir(dir);
  }

  async function handleDelete(u: AdminUser) {
    if (busy.current || !available) return;
    busy.current = true;
    setActionError("");
    try {
      setConfirmDeleteUser(await getAdminUser(u.id, authority));
    } catch (err) {
      setActionError(err instanceof Error ? err.message : t("web.admin.users.list.userLoadFailed"));
    } finally {
      busy.current = false;
    }
  }

  function setActiveTab(value: string) {
    const nextTab = normalizeAdminUsersTab(value);
    const next = new URLSearchParams(searchParams);

    // The default tab stays the bare /admin/users URL.
    if (nextTab === "users") {
      next.delete("tab");
    } else {
      next.set("tab", nextTab);
    }

    setSearchParams(next, { replace: true });
  }

  const rowFor = useAdminUserRows({
    viewerId,
    viewerIsOwner,
    available,
    groupNames,
    onViewAs: setImpersonatingUser,
    onDelete: (u) => void handleDelete(u),
  });
  const paginatedRows = paginatedUsers.map(rowFor);

  const reloadUsersButton = (
    <Button className="max-lg:h-11" onClick={() => void usersQuery.refetch()}>
      {t("web.admin.users.list.reload")}
    </Button>
  );

  if (isLoading)
    return (
      <div className="space-y-3">
        <Skeleton className="h-10 w-full rounded-lg" />
        {Array.from({ length: 5 }).map((_, i) => (
          <Skeleton key={i} className="h-12 w-full rounded-lg" />
        ))}
      </div>
    );

  return (
    <div className="space-y-6">
      {confirmDeleteUser && (
        <AdminUserDeleteDialog
          initialEditor={confirmDeleteUser}
          onClose={() => setConfirmDeleteUser(null)}
          onDeleted={() => setConfirmDeleteUser(null)}
        />
      )}
      {impersonatingUser && (
        <AdminUserImpersonationDialog
          user={impersonatingUser}
          returnPath="/admin/users"
          onClose={() => setImpersonatingUser(null)}
          onError={setActionError}
        />
      )}
      <PageHeader
        title={t("web.admin.users.list.title")}
        subtitle={t("web.admin.users.list.subtitle")}
        actions={
          // Below lg the actions take the full width and Add User its own row.
          <div className="flex flex-wrap items-center gap-2 max-lg:w-full">
            {serverSettings !== undefined && (
              <Link
                to="/admin/settings/sign-in"
                // The badge keeps its size; the link around it is the 44px target.
                className="inline-flex w-fit items-center gap-1 hover:opacity-80 max-lg:min-h-11"
              >
                <Badge
                  variant={signupsEnabled ? "outline" : "secondary"}
                  className={
                    signupsEnabled
                      ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-500"
                      : undefined
                  }
                >
                  {signupsEnabled
                    ? t("web.admin.users.list.signupsOn")
                    : t("web.admin.users.list.signupsOff")}
                  <ArrowRight className="h-3 w-3" aria-hidden="true" />
                </Badge>
              </Link>
            )}
            <Button variant="outline" size="sm" className="max-lg:h-11" asChild>
              <Link to="/admin/access-groups">
                <Settings2 className="me-1 h-4 w-4" /> {t("web.admin.users.list.accessGroupsLink")}
              </Link>
            </Button>
            <Dialog
              open={dialogOpen}
              onOpenChange={(open) => {
                if (formBusy.current || (open && !available)) return;
                setDialogOpen(open);
              }}
            >
              <DialogTrigger asChild>
                <Button size="sm" className="max-lg:h-11 max-lg:basis-full">
                  <Plus className="me-1 h-4 w-4" /> {t("web.admin.users.list.addUser")}
                </Button>
              </DialogTrigger>
              <CreateUserDialogContent
                onBusy={(value) => {
                  formBusy.current = value;
                }}
                onClose={() => setDialogOpen(false)}
              />
            </Dialog>
          </div>
        }
      />
      {actionError && <p role="alert">{actionError}</p>}
      {usersQuery.isError && (
        <div role="alert" className="max-lg:flex max-lg:flex-wrap max-lg:items-center max-lg:gap-2">
          {t("web.admin.users.list.loadFailed")} {reloadUsersButton}
        </div>
      )}
      {!available && <p role="status">{t("web.admin.users.list.unavailable")}</p>}

      <Tabs value={activeTab} onValueChange={setActiveTab}>
        <TabsList
          variant="line"
          className="border-border w-full justify-start border-b max-lg:h-auto!"
        >
          <TabsTrigger value="users" className="max-lg:h-11">
            {t("web.admin.users.list.tab.users")}
          </TabsTrigger>
          <TabsTrigger value="invitations" className="max-lg:h-11">
            {t("web.admin.users.list.tab.invitations")}
          </TabsTrigger>
          <TabsTrigger value="invite-codes" className="max-lg:h-11">
            {t("web.admin.users.list.tab.inviteCodes")}
          </TabsTrigger>
        </TabsList>
        <TabsContent value="users" className="pt-4">
          {/* Below lg: search on its own row, then the group filter and Sort (stacked below sm). */}
          <div className="mb-4 flex flex-col gap-2 lg:flex-row">
            <div className="relative flex-1">
              <Search className="text-muted-foreground absolute start-3 top-1/2 h-4 w-4 -translate-y-1/2" />
              <Input
                placeholder={t("web.admin.users.list.searchPlaceholder")}
                value={search}
                onChange={(e) => {
                  setSearch(e.target.value);
                  setPage(0);
                }}
                // 16px below sm so focusing it does not zoom the page on iOS.
                className="ps-9 pe-9 max-lg:h-11 max-lg:pe-11 max-sm:text-base"
              />
              {search && (
                <button
                  type="button"
                  aria-label={t("web.admin.users.list.clearSearch")}
                  onClick={() => {
                    setSearch("");
                    setPage(0);
                  }}
                  className="text-muted-foreground hover:text-foreground absolute end-3 top-1/2 -translate-y-1/2 max-lg:end-0 max-lg:flex max-lg:size-11 max-lg:items-center max-lg:justify-center"
                >
                  <X className="h-4 w-4" />
                </button>
              )}
            </div>
            <div className="flex flex-col gap-2 sm:flex-row">
              <Select
                value={groupFilter}
                onValueChange={(value) => {
                  setGroupFilter(value);
                  setPage(0);
                }}
              >
                <SelectTrigger
                  className={cn("max-sm:w-full sm:w-56", TOUCH_CONTROL_CLASS)}
                  aria-label={t("web.admin.users.list.groupFilterLabel")}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">{t("web.admin.users.list.groupFilterAll")}</SelectItem>
                  {accessGroups.map((group) => (
                    <SelectItem key={group.id} value={String(group.id)}>
                      {group.name}
                    </SelectItem>
                  ))}
                  <SelectItem value="none">{t("web.admin.users.list.noGroup")}</SelectItem>
                </SelectContent>
              </Select>
              {isDesktop ? null : (
                // SortControl carries its own bottom margin for the cards; the row's gap spaces it here.
                <div className="min-w-0 flex-1 *:mb-0!">
                  <SortControl
                    options={USER_SORT_FIELDS.map(({ value, label }) => ({
                      value,
                      label: t(label),
                    }))}
                    sortField={sortField}
                    sortDir={sortDir}
                    onFieldChange={handleSortFieldChange}
                    onDirChange={handleSortDirChange}
                  />
                </div>
              )}
            </div>
          </div>
          {accessGroupsQuery.isError && (
            <div role="alert" className="mb-4 flex flex-wrap items-center gap-2 text-sm">
              {t("web.admin.users.list.groupsLoadFailed")}
              <Button
                variant="outline"
                size="sm"
                className="max-lg:h-11"
                onClick={() => void accessGroupsQuery.refetch()}
              >
                {t("common.retry")}
              </Button>
            </div>
          )}
          <div className="surface-panel overflow-x-auto rounded-2xl border-0">
            {isDesktop ? (
              <Table>
                <TableHeader>
                  <TableRow>
                    <SortableUserHead
                      field="username"
                      activeField={sortField}
                      activeDir={sortDir}
                      onSort={handleSort}
                    >
                      {t("web.admin.users.list.column.username")}
                    </SortableUserHead>
                    <SortableUserHead
                      field="email"
                      activeField={sortField}
                      activeDir={sortDir}
                      onSort={handleSort}
                    >
                      {t("web.admin.users.list.column.email")}
                    </SortableUserHead>
                    <SortableUserHead
                      field="role"
                      activeField={sortField}
                      activeDir={sortDir}
                      onSort={handleSort}
                    >
                      {t("web.admin.users.list.column.role")}
                    </SortableUserHead>
                    <TableHead>{t("web.admin.users.list.column.group")}</TableHead>
                    <SortableUserHead
                      field="enabled"
                      activeField={sortField}
                      activeDir={sortDir}
                      onSort={handleSort}
                    >
                      {t("web.admin.users.list.column.status")}
                    </SortableUserHead>
                    <SortableUserHead
                      field="created_at"
                      activeField={sortField}
                      activeDir={sortDir}
                      onSort={handleSort}
                    >
                      {t("web.admin.users.list.column.created")}
                    </SortableUserHead>
                    <SortableUserHead
                      field="last_active_at"
                      activeField={sortField}
                      activeDir={sortDir}
                      onSort={handleSort}
                    >
                      {t("web.admin.users.list.column.lastActive")}
                    </SortableUserHead>
                    <TableHead className="w-32">
                      {t("web.admin.users.list.column.actions")}
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {paginatedRows.map((row) => {
                    const u = row.user;
                    return (
                      <TableRow key={u.id}>
                        <TableCell>
                          <Link to={row.detailPath} className="font-medium hover:underline">
                            {u.username}
                          </Link>
                        </TableCell>
                        <TableCell>{u.email}</TableCell>
                        <TableCell>
                          <AdminUserRoleBadge user={u} />
                        </TableCell>
                        <TableCell>
                          {row.group.kind === "admin" ? (
                            <span className="text-muted-foreground">
                              {t("web.admin.users.list.adminGroupNone")}
                            </span>
                          ) : row.group.kind === "none" ? (
                            <span className="text-muted-foreground">
                              {t("web.admin.users.list.noGroup")}
                            </span>
                          ) : (
                            <Link
                              to={`/admin/access-groups/${row.group.id}`}
                              className="hover:underline"
                            >
                              {row.group.name}
                            </Link>
                          )}
                        </TableCell>
                        <TableCell>
                          <AdminUserStatusBadge user={u} />
                        </TableCell>
                        <TableCell title={formatFullDateTime(u.created_at)}>
                          {formatDateTime(u.created_at)}
                        </TableCell>
                        <TableCell title={row.lastActiveTitle}>{row.lastActive}</TableCell>
                        <TableCell>
                          <TooltipProvider delayDuration={0}>
                            <div className="flex gap-1">
                              <Tooltip>
                                <TooltipTrigger asChild>
                                  <Button asChild variant="ghost" size="icon" className="h-7 w-7">
                                    <Link
                                      to={row.historyPath}
                                      aria-label={t("web.admin.users.list.historyAria", {
                                        username: u.username,
                                      })}
                                    >
                                      <History className="h-3 w-3" aria-hidden="true" />
                                    </Link>
                                  </Button>
                                </TooltipTrigger>
                                <TooltipContent>
                                  {t("web.admin.users.list.historyTooltip")}
                                </TooltipContent>
                              </Tooltip>
                              {row.viewAs && (
                                <Tooltip>
                                  <TooltipTrigger asChild>
                                    <Button
                                      variant="ghost"
                                      size="icon"
                                      className="h-7 w-7"
                                      aria-label={t("web.admin.users.list.viewAsAria", {
                                        username: u.username,
                                      })}
                                      onClick={row.viewAs}
                                    >
                                      <UserRound className="h-3 w-3" aria-hidden="true" />
                                    </Button>
                                  </TooltipTrigger>
                                  <TooltipContent>
                                    {t("web.admin.users.list.viewAsTooltip")}
                                  </TooltipContent>
                                </Tooltip>
                              )}
                              {row.editPath && (
                                <Tooltip>
                                  <TooltipTrigger asChild>
                                    <Button asChild variant="ghost" size="icon" className="h-7 w-7">
                                      <Link
                                        to={row.editPath}
                                        aria-label={t("web.admin.users.list.editAria", {
                                          username: u.username,
                                        })}
                                      >
                                        <Pencil className="h-3 w-3" aria-hidden="true" />
                                      </Link>
                                    </Button>
                                  </TooltipTrigger>
                                  <TooltipContent>
                                    {t("web.admin.users.list.editTooltip")}
                                  </TooltipContent>
                                </Tooltip>
                              )}
                              {row.remove && (
                                <Tooltip>
                                  <TooltipTrigger asChild>
                                    <Button
                                      variant="ghost"
                                      size="icon"
                                      className="h-7 w-7"
                                      aria-label={t("web.admin.users.list.deleteAria", {
                                        username: u.username,
                                      })}
                                      onClick={row.remove}
                                    >
                                      <Trash2 className="h-3 w-3" aria-hidden="true" />
                                    </Button>
                                  </TooltipTrigger>
                                  <TooltipContent>
                                    {t("web.admin.users.list.deleteTooltip")}
                                  </TooltipContent>
                                </Tooltip>
                              )}
                            </div>
                          </TooltipProvider>
                        </TableCell>
                      </TableRow>
                    );
                  })}
                </TableBody>
              </Table>
            ) : usersQuery.isError && paginatedRows.length === 0 ? null : (
              <AdminUserCompactList rows={paginatedRows} />
            )}
            {total > pageSize && (
              // Below lg: the range on its own line, then page size, Previous and Next at 44px.
              <div className="max-lg:border-border/60 flex items-center justify-between px-4 py-4 max-lg:flex-wrap max-lg:gap-x-2 max-lg:gap-y-3 max-lg:border-t">
                <div className="flex items-center gap-4 max-lg:contents">
                  <span className="text-muted-foreground text-sm max-lg:basis-full">
                    {t("web.admin.users.list.pageRange", {
                      from: page * pageSize + 1,
                      to: Math.min((page + 1) * pageSize, total),
                      total: total,
                    })}
                  </span>
                  <Select
                    value={String(pageSize)}
                    onValueChange={(v) => {
                      setPageSize(Number(v));
                      setPage(0);
                    }}
                  >
                    <SelectTrigger
                      className={cn("h-8 min-w-[100px] max-lg:me-auto", TOUCH_CONTROL_CLASS)}
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {PAGE_SIZE_OPTIONS.map((size) => (
                        <SelectItem key={size} value={size}>
                          {t("web.admin.users.list.pageSizeOption", { count: Number(size) })}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className="flex gap-2">
                  <Button
                    variant="outline"
                    size="sm"
                    className="max-lg:h-11"
                    onClick={() => setPage((p) => p - 1)}
                    disabled={page === 0}
                  >
                    {t("web.admin.users.list.previousPage")}
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    className="max-lg:h-11"
                    onClick={() => setPage((p) => p + 1)}
                    disabled={(page + 1) * pageSize >= total}
                  >
                    {t("web.admin.users.list.nextPage")}
                  </Button>
                </div>
              </div>
            )}
          </div>
        </TabsContent>
        <TabsContent value="invitations" className="pt-4">
          <InvitationsTab />
        </TabsContent>
        <TabsContent value="invite-codes" className="pt-4">
          <InviteCodesTab />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function SortableUserHead({
  field,
  activeField,
  activeDir,
  onSort,
  children,
}: {
  field: UserSortField;
  activeField: UserSortField;
  activeDir: SortDirection;
  onSort: (field: UserSortField) => void;
  children: ReactNode;
}) {
  const active = field === activeField;

  return (
    <TableHead aria-sort={active ? (activeDir === "asc" ? "ascending" : "descending") : "none"}>
      <button
        type="button"
        className="hover:text-foreground inline-flex items-center gap-1 transition-colors"
        onClick={() => onSort(field)}
      >
        {children}
        {active ? (
          activeDir === "asc" ? (
            <ChevronUp className="h-3 w-3" />
          ) : (
            <ChevronDown className="h-3 w-3" />
          )
        ) : (
          <ChevronDown className="h-3 w-3 opacity-0" />
        )}
      </button>
    </TableHead>
  );
}
