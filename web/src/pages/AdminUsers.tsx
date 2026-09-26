import { useState, useId, useMemo, useRef } from "react";
import type { FormEvent, ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
import type { AdminUser, CreateUserRequest, UpdateUserRequest } from "@/api/types";
import {
  useAdminUsers,
  useCreateUser,
  useUpdateUser,
  useAdminUserCapabilities,
  useViewerIsOwner,
} from "@/hooks/queries/admin/users";
import { canManageAccount, canViewAsAccount } from "@/lib/accountOwner";
import { useAdminServerSettings } from "@/hooks/queries/admin/settings";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { useAccessGroups } from "@/hooks/queries/admin/accessGroups";
import {
  PolicyAccessFields,
  PolicyLimitFields,
  effectiveAccessGroupID,
  policyCreateFields,
  policyDefaultSource,
  policyInheritHints,
  policyStateFromUser,
  policyUpdateFields,
} from "@/components/UserPolicyFields";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { AdminUserImpersonationDialog } from "@/components/AdminUserImpersonationDialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
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
import { V2ProblemError } from "@/api/v2/request";
import { Skeleton } from "@/components/ui/skeleton";
import InvitationsTab from "./admin-settings/InvitationsTab";
import InviteCodesTab from "./admin-settings/InviteCodesTab";
import {
  PERMISSION_MARKER_EDIT,
  PERMISSION_METADATA_CURATION,
  hasAssignedPermission,
  setAssignedPermission,
} from "@/lib/permissions";
import { formatDateTime as formatDateTimePreferred } from "@/lib/datetime";
import { INVALID_EMAIL_MESSAGE, isValidEmail } from "@/lib/email";

const PAGE_SIZE_OPTIONS = ["25", "50", "100"] as const;
type UserSortField = "username" | "email" | "role" | "enabled" | "created_at" | "last_active_at";
type SortDirection = "asc" | "desc";

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
  const [editingUser, setEditingUser] = useState<AdminUserEditor | null>(null);
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
    setSortDir(field === "created_at" || field === "last_active_at" ? "desc" : "asc");
  }

  async function loadEditor(u: AdminUser, deleting = false) {
    if (busy.current || !available) return;
    busy.current = true;
    setActionError("");
    try {
      const editor = await getAdminUser(u.id, authority);
      if (deleting) setConfirmDeleteUser(editor);
      else {
        setEditingUser(editor);
        setDialogOpen(true);
      }
    } catch (err) {
      setActionError(err instanceof Error ? err.message : "Could not load user.");
    } finally {
      busy.current = false;
    }
  }
  function handleDelete(u: AdminUser) {
    void loadEditor(u, true);
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
      {actionError && <p role="alert">{actionError}</p>}
      {usersQuery.isError && (
        <div role="alert">
          Could not load users.{" "}
          <Button onClick={() => void usersQuery.refetch()}>Reload users</Button>
        </div>
      )}
      {!available && <p role="status">User administration is unavailable.</p>}
      <div className="page-header">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Users</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Manage access, defaults, and invite flow for the people using Silo.
          </p>
          {serverSettings !== undefined && (
            <Link
              to="/admin/settings/general"
              className="inline-flex w-fit items-center gap-1 hover:opacity-80"
            >
              <Badge
                variant={signupsEnabled ? "outline" : "secondary"}
                className={
                  signupsEnabled
                    ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-500"
                    : undefined
                }
              >
                {signupsEnabled ? "Public signups on" : "Public signups off"}
                <ArrowRight className="h-3 w-3" aria-hidden="true" />
              </Badge>
            </Link>
          )}
        </div>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" asChild>
            <Link to="/admin/access-groups">
              <Settings2 className="mr-1 h-4 w-4" /> Access Groups
            </Link>
          </Button>
          <Dialog
            open={dialogOpen}
            onOpenChange={(open) => {
              if (formBusy.current || (open && !available)) return;
              setDialogOpen(open);
              if (!open) setEditingUser(null);
            }}
          >
            <DialogTrigger asChild>
              <Button size="sm">
                <Plus className="mr-1 h-4 w-4" /> Add User
              </Button>
            </DialogTrigger>
            <DialogContent className="sm:max-w-2xl">
              <DialogHeader>
                <DialogTitle>{editingUser ? "Edit User" : "Create User"}</DialogTitle>
              </DialogHeader>
              <UserForm
                initialEditor={editingUser}
                onBusy={(value) => {
                  formBusy.current = value;
                }}
                onClose={() => {
                  setDialogOpen(false);
                  setEditingUser(null);
                }}
              />
            </DialogContent>
          </Dialog>
        </div>
      </div>

      <Tabs value={activeTab} onValueChange={setActiveTab}>
        <TabsList variant="line" className="border-border w-full justify-start border-b">
          <TabsTrigger value="users">Users</TabsTrigger>
          <TabsTrigger value="invitations">Invitations</TabsTrigger>
          <TabsTrigger value="invite-codes">Invite Codes</TabsTrigger>
        </TabsList>
        <TabsContent value="users" className="pt-4">
          <div className="mb-4 flex flex-col gap-2 sm:flex-row">
            <div className="relative flex-1">
              <Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
              <Input
                placeholder="Search by username or email..."
                value={search}
                onChange={(e) => {
                  setSearch(e.target.value);
                  setPage(0);
                }}
                className="pr-9 pl-9"
              />
              {search && (
                <button
                  type="button"
                  aria-label="Clear search"
                  onClick={() => {
                    setSearch("");
                    setPage(0);
                  }}
                  className="text-muted-foreground hover:text-foreground absolute top-1/2 right-3 -translate-y-1/2"
                >
                  <X className="h-4 w-4" />
                </button>
              )}
            </div>
            <Select
              value={groupFilter}
              onValueChange={(value) => {
                setGroupFilter(value);
                setPage(0);
              }}
            >
              <SelectTrigger className="sm:w-56" aria-label="Filter by access group">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All groups</SelectItem>
                {accessGroups.map((group) => (
                  <SelectItem key={group.id} value={String(group.id)}>
                    {group.name}
                  </SelectItem>
                ))}
                <SelectItem value="none">No group</SelectItem>
              </SelectContent>
            </Select>
          </div>
          {accessGroupsQuery.isError && (
            <div role="alert" className="mb-4 flex flex-wrap items-center gap-2 text-sm">
              Could not load access groups, so group names and group filters are unavailable.
              <Button variant="outline" size="sm" onClick={() => void accessGroupsQuery.refetch()}>
                Retry
              </Button>
            </div>
          )}
          <div className="surface-panel overflow-x-auto rounded-2xl border-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <SortableUserHead
                    field="username"
                    activeField={sortField}
                    activeDir={sortDir}
                    onSort={handleSort}
                  >
                    Username
                  </SortableUserHead>
                  <SortableUserHead
                    field="email"
                    activeField={sortField}
                    activeDir={sortDir}
                    onSort={handleSort}
                  >
                    Email
                  </SortableUserHead>
                  <SortableUserHead
                    field="role"
                    activeField={sortField}
                    activeDir={sortDir}
                    onSort={handleSort}
                  >
                    Role
                  </SortableUserHead>
                  <TableHead>Group</TableHead>
                  <SortableUserHead
                    field="enabled"
                    activeField={sortField}
                    activeDir={sortDir}
                    onSort={handleSort}
                  >
                    Status
                  </SortableUserHead>
                  <SortableUserHead
                    field="created_at"
                    activeField={sortField}
                    activeDir={sortDir}
                    onSort={handleSort}
                  >
                    Created
                  </SortableUserHead>
                  <SortableUserHead
                    field="last_active_at"
                    activeField={sortField}
                    activeDir={sortDir}
                    onSort={handleSort}
                  >
                    Last Active
                  </SortableUserHead>
                  <TableHead className="w-32">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {paginatedUsers.map((u) => (
                  <TableRow key={u.id}>
                    <TableCell>
                      <Link to={`/admin/users/${u.id}`} className="font-medium hover:underline">
                        {u.username}
                      </Link>
                    </TableCell>
                    <TableCell>{u.email}</TableCell>
                    <TableCell>
                      <div className="flex flex-wrap gap-1">
                        <Badge variant={u.role === "admin" ? "default" : "secondary"}>
                          {u.role}
                        </Badge>
                        {u.is_owner && <Badge variant="outline">Owner</Badge>}
                      </div>
                    </TableCell>
                    <TableCell>
                      {u.role === "admin" ? (
                        <span className="text-muted-foreground">—</span>
                      ) : u.access_group_id == null ? (
                        <span className="text-muted-foreground">No group</span>
                      ) : (
                        <Link
                          to={`/admin/access-groups/${u.access_group_id}`}
                          className="hover:underline"
                        >
                          {groupNames.get(String(u.access_group_id)) ?? "Unknown group"}
                        </Link>
                      )}
                    </TableCell>
                    <TableCell>
                      <Badge variant={u.enabled ? "outline" : "destructive"}>
                        {u.enabled ? "Active" : "Disabled"}
                      </Badge>
                    </TableCell>
                    <TableCell title={formatFullDateTime(u.created_at)}>
                      {formatDateTime(u.created_at)}
                    </TableCell>
                    <TableCell title={u.last_active_at ? formatFullDateTime(u.last_active_at) : ""}>
                      {formatRelativeTime(u.last_active_at, "Never")}
                    </TableCell>
                    <TableCell>
                      <TooltipProvider delayDuration={0}>
                        <div className="flex gap-1">
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Button asChild variant="ghost" size="icon" className="h-7 w-7">
                                <Link
                                  to={`/admin/history?user_id=${u.id}`}
                                  aria-label={`View ${u.username} playback history`}
                                >
                                  <History className="h-3 w-3" aria-hidden="true" />
                                </Link>
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent>View playback history</TooltipContent>
                          </Tooltip>
                          {available && canViewAsAccount(u, viewerId, viewerIsOwner) && (
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <Button
                                  variant="ghost"
                                  size="icon"
                                  className="h-7 w-7"
                                  aria-label={`View as user: ${u.username}`}
                                  onClick={() => setImpersonatingUser(u)}
                                >
                                  <UserRound className="h-3 w-3" aria-hidden="true" />
                                </Button>
                              </TooltipTrigger>
                              <TooltipContent>View as user</TooltipContent>
                            </Tooltip>
                          )}
                          {canManageAccount(u, viewerId) && (
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <Button
                                  variant="ghost"
                                  size="icon"
                                  className="h-7 w-7"
                                  aria-label={`Edit ${u.username}`}
                                  onClick={() => {
                                    void loadEditor(u);
                                  }}
                                >
                                  <Pencil className="h-3 w-3" aria-hidden="true" />
                                </Button>
                              </TooltipTrigger>
                              <TooltipContent>Edit user</TooltipContent>
                            </Tooltip>
                          )}
                          {!u.is_owner && (
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <Button
                                  variant="ghost"
                                  size="icon"
                                  className="h-7 w-7"
                                  aria-label={`Delete ${u.username}`}
                                  onClick={() => handleDelete(u)}
                                >
                                  <Trash2 className="h-3 w-3" aria-hidden="true" />
                                </Button>
                              </TooltipTrigger>
                              <TooltipContent>Delete user</TooltipContent>
                            </Tooltip>
                          )}
                        </div>
                      </TooltipProvider>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
            {total > pageSize && (
              <div className="flex items-center justify-between px-4 py-4">
                <div className="flex items-center gap-4">
                  <span className="text-muted-foreground text-sm">
                    Showing {page * pageSize + 1}-{Math.min((page + 1) * pageSize, total)} of{" "}
                    {total}
                  </span>
                  <Select
                    value={String(pageSize)}
                    onValueChange={(v) => {
                      setPageSize(Number(v));
                      setPage(0);
                    }}
                  >
                    <SelectTrigger className="h-8 w-[100px]">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {PAGE_SIZE_OPTIONS.map((size) => (
                        <SelectItem key={size} value={size}>
                          {size} rows
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className="flex gap-2">
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => setPage((p) => p - 1)}
                    disabled={page === 0}
                  >
                    Previous
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => setPage((p) => p + 1)}
                    disabled={(page + 1) * pageSize >= total}
                  >
                    Next
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

function sortAdminUsers(users: AdminUser[], field: UserSortField, dir: SortDirection) {
  const direction = dir === "asc" ? 1 : -1;

  return [...users].sort((a, b) => {
    let result = 0;
    switch (field) {
      case "username":
        result = compareText(a.username, b.username);
        break;
      case "email":
        result = compareText(a.email, b.email);
        break;
      case "role":
        result = compareText(a.role, b.role);
        break;
      case "enabled":
        result = compareText(a.enabled ? "active" : "disabled", b.enabled ? "active" : "disabled");
        break;
      case "created_at":
        result = compareTime(a.created_at, b.created_at, dir);
        break;
      case "last_active_at":
        result = compareTime(a.last_active_at, b.last_active_at, dir);
        break;
    }

    if (result !== 0) {
      return field === "created_at" || field === "last_active_at" ? result : result * direction;
    }

    return compareText(a.username, b.username) || a.id - b.id;
  });
}

function compareText(a?: string | null, b?: string | null) {
  return (a ?? "").localeCompare(b ?? "", undefined, { numeric: true, sensitivity: "base" });
}

function compareTime(a?: string | null, b?: string | null, dir: SortDirection = "asc") {
  const aTime = parseTime(a);
  const bTime = parseTime(b);
  if (aTime === null && bTime === null) return 0;
  if (aTime === null) return 1;
  if (bTime === null) return -1;
  return (aTime - bTime) * (dir === "asc" ? 1 : -1);
}

function parseTime(value?: string | null) {
  const timestamp = Date.parse(value ?? "");
  return Number.isNaN(timestamp) ? null : timestamp;
}

function formatDateTime(value?: string | null, fallback = "-") {
  const timestamp = parseTime(value);
  if (timestamp === null) return fallback;
  return formatDateTimePreferred(timestamp, { dateStyle: "medium", seconds: false });
}

function formatFullDateTime(value?: string | null) {
  const timestamp = parseTime(value);
  if (timestamp === null) return "";
  return formatDateTimePreferred(timestamp);
}

function formatRelativeTime(value?: string | null, fallback = "-") {
  const timestamp = parseTime(value);
  if (timestamp === null) return fallback;

  const seconds = Math.round((timestamp - Date.now()) / 1000);
  const ranges: Array<[Intl.RelativeTimeFormatUnit, number]> = [
    ["year", 60 * 60 * 24 * 365],
    ["month", 60 * 60 * 24 * 30],
    ["week", 60 * 60 * 24 * 7],
    ["day", 60 * 60 * 24],
    ["hour", 60 * 60],
    ["minute", 60],
    ["second", 1],
  ];
  const formatter = new Intl.RelativeTimeFormat(undefined, { numeric: "always" });

  for (const [unit, secondsPerUnit] of ranges) {
    if (Math.abs(seconds) >= secondsPerUnit || unit === "second") {
      return formatter.format(Math.round(seconds / secondsPerUnit), unit);
    }
  }

  return fallback;
}

function UserForm({
  initialEditor,
  onClose,
  onBusy,
}: {
  initialEditor: AdminUserEditor | null;
  onClose: () => void;
  onBusy: (busy: boolean) => void;
}) {
  const [editor, setEditor] = useState(initialEditor);
  const user = editor?.user;
  const [authority] = useState(captureAdminUserAuthority);
  const busy = useRef(false);
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const [reloading, setReloading] = useState(false);
  const [saved, setSaved] = useState(false);
  const capabilities = useAdminUserCapabilities();
  const [createDefaultProfile, setCreateDefaultProfile] = useState(true);
  async function reload() {
    if (!editor || busy.current) return;
    busy.current = true;
    setReloading(true);
    onBusy(true);
    try {
      setEditor(await getAdminUser(editor.user.id, editor.profileContext));
      setConflict(false);
      setError("");
      if (saved) onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not reload user.");
    } finally {
      busy.current = false;
      setReloading(false);
      onBusy(false);
    }
  }

  const { data: libraries = [] } = useAdminLibraries();
  const { data: accessGroups = [], isSuccess: accessGroupsLoaded } = useAccessGroups();
  const [username, setUsername] = useState(user?.username ?? "");
  const [email, setEmail] = useState(user?.email ?? "");
  const [password, setPassword] = useState("");
  const [requirePasswordChange, setRequirePasswordChange] = useState(false);
  const [role, setRole] = useState(user?.role ?? "user");
  const [enabled, setEnabled] = useState(user?.enabled ?? true);
  const [permissions, setPermissions] = useState<string[]>(
    user?.permissions ?? [PERMISSION_MARKER_EDIT],
  );
  // Policy fields inherit from the access group unless explicitly overridden.
  const [policy, setPolicy] = useState(() => policyStateFromUser(user ?? null));
  const [maxProfiles, setMaxProfiles] = useState<number>(user?.max_profiles ?? 5);
  const usernameId = useId();
  const emailId = useId();
  const passwordId = useId();
  const requireChangeId = useId();
  const roleId = useId();
  const enabledId = useId();
  const markerEditId = useId();
  const metadataCurationId = useId();
  const maxProfilesId = useId();
  const accessGroupSelectId = useId();
  const createMutation = useCreateUser();
  const updateMutation = useUpdateUser();
  const isPending = createMutation.isPending || updateMutation.isPending;
  // The group picker starts on the account's group. A new account, or an admin
  // being demoted here, starts on the default group (undefined until picked),
  // which is where the server would place it anyway. An admin stays ungrouped
  // (auth.Repository create and update), so the picker is disabled for admins.
  const [pickedGroupID, setPickedGroupID] = useState<number | null | undefined>(
    user && user.role !== "admin" ? user.access_group_id : undefined,
  );
  const defaultGroupID = accessGroups.find((group) => group.is_default)?.id ?? null;
  const selectedGroupID = pickedGroupID === undefined ? defaultGroupID : pickedGroupID;
  const inheritGroupID = effectiveAccessGroupID(role, selectedGroupID);
  // Until the group list loads, the default group such an account joins is
  // unknown, and so is what it inherits; it is not the no-group defaults.
  const awaitingDefaultGroup =
    pickedGroupID === undefined && role !== "admin" && !accessGroupsLoaded;
  const hintSource = awaitingDefaultGroup ? "group" : policyDefaultSource(role, inheritGroupID);
  const inheritHints = awaitingDefaultGroup
    ? undefined
    : (policyInheritHints(inheritGroupID, accessGroups) ??
      (role !== "admin" && user && selectedGroupID === user.access_group_id
        ? user.effective_policy
        : undefined));
  // The group to send: none while the default group is still unknown, so the
  // server applies its own default instead of an accidental "no group".
  const groupToSend = awaitingDefaultGroup
    ? undefined
    : effectiveAccessGroupID(role, selectedGroupID);
  const accessGroupValue =
    awaitingDefaultGroup || role === "admin" || selectedGroupID === null
      ? "none"
      : String(selectedGroupID);
  // Creating an account can't choose "no group": the server treats a missing or
  // null group alike and places the account in the default group. Offer it only
  // when editing, or when there is no default group to show instead.
  const offerNoGroup = Boolean(user) || awaitingDefaultGroup || defaultGroupID === null;
  const selectedGroupMissing =
    selectedGroupID !== null &&
    accessGroupsLoaded &&
    !accessGroups.some((group) => group.id === selectedGroupID);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (
      busy.current ||
      conflict ||
      saved ||
      !capabilities.data?.available ||
      (!user && createDefaultProfile && !capabilities.data.default_profile)
    )
      return;
    if (!isValidEmail(email)) {
      setError(INVALID_EMAIL_MESSAGE);
      return;
    }
    busy.current = true;
    onBusy(true);
    setError("");
    try {
      if (user && editor) {
        const body: UpdateUserRequest = {
          username,
          email,
          role,
          permissions,
          enabled,
          max_profiles: maxProfiles,
          ...policyUpdateFields(policy),
        };
        if (groupToSend !== undefined) {
          body.access_group_id = groupToSend;
        }
        if (password) {
          body.password = password;
          if (requirePasswordChange) body.require_password_change = true;
        }
        await updateMutation.mutateAsync({ editor, body });
        setSaved(true);
        await getAdminUser(user.id, editor.profileContext);
        onClose();
      } else {
        const body: CreateUserRequest = {
          username,
          email,
          password,
          ...(requirePasswordChange ? { require_password_change: true } : {}),
          role,
          permissions,
          create_default_profile: createDefaultProfile,
          max_profiles: maxProfiles,
          ...policyCreateFields(policy),
          ...(typeof groupToSend === "number" ? { access_group_id: groupToSend } : {}),
        };
        await createMutation.mutateAsync({ body, profileContext: authority });
        createMutation.reset();
        onClose();
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not save user.");
      if (err instanceof V2ProblemError && err.status === 412) setConflict(true);
    } finally {
      busy.current = false;
      onBusy(false);
    }
  }

  return (
    <form onSubmit={handleSubmit} className="flex max-h-[70vh] flex-col">
      {error && <p role="alert">{error}</p>}
      {(conflict || saved) && (
        <div>
          {saved
            ? "Saved. Reload the user to confirm the current state."
            : "Your draft is preserved. Reload before submitting again."}
          <Button type="button" disabled={reloading} onClick={() => void reload()}>
            Reload current user
          </Button>
        </div>
      )}
      {!user && (
        <label className="mb-3 flex items-center gap-2">
          <input
            type="checkbox"
            checked={createDefaultProfile}
            onChange={(event) => setCreateDefaultProfile(event.target.checked)}
          />
          Create a default profile
          {!capabilities.data?.default_profile &&
            " (unavailable; uncheck to create only the account)"}
        </label>
      )}

      <Tabs defaultValue="account" className="min-h-0 flex-1">
        <TabsList variant="line" className="border-border mb-4 w-full justify-start border-b pb-1">
          <TabsTrigger value="account" className="flex-none px-1">
            Account
          </TabsTrigger>
          <TabsTrigger value="access" className="flex-none px-1">
            Access
          </TabsTrigger>
          <TabsTrigger value="limits" className="flex-none px-1">
            Limits
          </TabsTrigger>
        </TabsList>

        <div className="min-h-0 flex-1 overflow-y-auto pr-1">
          <TabsContent value="account" className="mt-0 space-y-4">
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor={usernameId}>Username</Label>
                <Input
                  id={usernameId}
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor={emailId}>Email</Label>
                <Input
                  id={emailId}
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  required
                />
              </div>
              {user && !user.password_login ? (
                <div className="space-y-2">
                  <Label>Password</Label>
                  <p className="text-muted-foreground text-xs">
                    An external sign-in provider manages this account's password.
                  </p>
                </div>
              ) : (
                <div className="space-y-2">
                  <Label htmlFor={passwordId}>
                    Password {user && "(leave blank to keep current)"}
                  </Label>
                  <Input
                    id={passwordId}
                    type="password"
                    autoComplete="new-password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    required={!user}
                  />
                  <div className="flex items-center gap-2">
                    <Switch
                      id={requireChangeId}
                      checked={requirePasswordChange && password !== ""}
                      disabled={password === ""}
                      onCheckedChange={setRequirePasswordChange}
                    />
                    <Label htmlFor={requireChangeId} className="text-xs font-normal">
                      Require change at {user ? "next" : "first"} sign-in
                    </Label>
                  </div>
                </div>
              )}
              <div className="space-y-2">
                <Label htmlFor={roleId}>Role</Label>
                <Select value={role} onValueChange={setRole} disabled={user?.is_owner}>
                  <SelectTrigger id={roleId}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="user">User</SelectItem>
                    <SelectItem value="admin">Admin</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
            {user && (
              <div className="border-border flex items-center justify-between rounded-md border px-3 py-2">
                <div>
                  <div className="text-sm font-medium">Account status</div>
                  <div className="text-muted-foreground text-xs">
                    {user.is_owner
                      ? "The server owner stays an enabled admin."
                      : "Disable access without deleting the user."}
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  <Label htmlFor={enabledId} className="text-xs">
                    Enabled
                  </Label>
                  <Switch
                    id={enabledId}
                    checked={enabled}
                    onCheckedChange={setEnabled}
                    disabled={user.is_owner}
                  />
                </div>
              </div>
            )}
          </TabsContent>

          <TabsContent value="access" className="mt-0 space-y-4">
            <div className="space-y-2">
              <Label htmlFor={accessGroupSelectId}>Group</Label>
              <Select
                value={accessGroupValue}
                onValueChange={(value) => {
                  setPickedGroupID(value === "none" ? null : Number(value));
                }}
                disabled={role === "admin" || awaitingDefaultGroup}
              >
                <SelectTrigger id={accessGroupSelectId} className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {offerNoGroup && (
                    <SelectItem value="none">
                      {awaitingDefaultGroup ? "Default group" : "No group"}
                    </SelectItem>
                  )}
                  {selectedGroupMissing && (
                    <SelectItem value={String(selectedGroupID)}>#{selectedGroupID}</SelectItem>
                  )}
                  {accessGroups.map((group) => (
                    <SelectItem key={group.id} value={String(group.id)}>
                      {group.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {role === "admin" && (
                <p className="text-muted-foreground text-xs">
                  Admin accounts can&apos;t join groups.
                </p>
              )}
            </div>
            <div className="border-border flex items-center justify-between rounded-md border px-3 py-2">
              <div>
                <Label htmlFor={markerEditId}>Marker Editing</Label>
                <p className="text-muted-foreground text-xs">
                  Edit intro, recap, credits, and preview markers within assigned libraries.
                </p>
              </div>
              <Switch
                id={markerEditId}
                checked={hasAssignedPermission(permissions, PERMISSION_MARKER_EDIT)}
                onCheckedChange={(checked) =>
                  setPermissions((current) =>
                    setAssignedPermission(current, PERMISSION_MARKER_EDIT, checked),
                  )
                }
              />
            </div>
            <div className="border-border flex items-center justify-between rounded-md border px-3 py-2">
              <div>
                <Label htmlFor={metadataCurationId}>Metadata Curation</Label>
                <p className="text-muted-foreground text-xs">
                  Edit, refresh, and rematch metadata within assigned libraries.
                </p>
              </div>
              <Switch
                id={metadataCurationId}
                checked={hasAssignedPermission(permissions, PERMISSION_METADATA_CURATION)}
                onCheckedChange={(checked) =>
                  setPermissions((current) =>
                    setAssignedPermission(current, PERMISSION_METADATA_CURATION, checked),
                  )
                }
              />
            </div>
            <PolicyAccessFields
              state={policy}
              onChange={setPolicy}
              source={hintSource}
              effective={inheritHints}
              libraries={libraries}
            />
          </TabsContent>

          <TabsContent value="limits" className="mt-0 space-y-4">
            <PolicyLimitFields
              state={policy}
              onChange={setPolicy}
              source={hintSource}
              effective={inheritHints}
            />
            <div className="space-y-1">
              <Label htmlFor={maxProfilesId}>Max Profiles</Label>
              <Input
                id={maxProfilesId}
                type="number"
                min={1}
                value={maxProfiles}
                onChange={(e) => setMaxProfiles(Number(e.target.value))}
              />
            </div>
          </TabsContent>
        </div>
      </Tabs>

      <div className="border-border mt-4 border-t pt-4">
        <Button
          type="submit"
          className="w-full"
          disabled={
            isPending ||
            conflict ||
            saved ||
            reloading ||
            !capabilities.data?.available ||
            (!user && createDefaultProfile && !capabilities.data.default_profile)
          }
        >
          {isPending ? "Saving..." : "Save"}
        </Button>
      </div>
    </form>
  );
}
