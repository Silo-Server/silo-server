import {
  adminImportScope,
  importRunActive,
  type AdminImportRun,
  getAdminImportSource,
  getAdminImportMapping,
  isAdminImportConflict,
} from "@/api/v2/adminHistoryImports";
import { useOptionalAuth } from "@/hooks/useAuth";
import { useState, useMemo, useCallback, useId } from "react";
import { useSearchParams } from "react-router";
import { useEventChannel } from "@/components/realtimeEventsContext";
import {
  AlertTriangle,
  ArrowRight,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  CircleSlash2,
  Clock,
  Eye,
  EyeOff,
  KeyRound,
  Loader2,
  Pencil,
  Play,
  Plus,
  Search,
  Server,
  Trash2,
  XCircle,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
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
  useClearAdminSourceToken,
  useCreateAdminHistoryImportSource,
  useDeleteAdminHistoryImportSource,
  useDiscoverExternalUsers,
  usePlexLogin,
  useSetAdminSourceToken,
  useUpdateAdminHistoryImportSource,
  useAdminHistoryImportSources,
  useAdminHistoryImportCapabilities,
} from "@/hooks/queries/admin/history-import-sources";
import {
  useAdminHistoryImportRuns,
  useAdminBulkRun,
  useCancelAdminRun,
  useCreateAdminMapping,
  useCreateAdminRunForMapping,
  useDeleteAdminMapping,
  useAdminHistoryImportMappings,
} from "@/hooks/queries/admin/history-import-admin";
import { useAdminUsers } from "@/hooks/queries/admin/users";
import { useAdminUserProfiles } from "@/hooks/queries/admin/history";
import type {
  CreateHistoryImportSourceRequest,
  HistoryImportExternalUser,
  HistoryImportSource,
  HistoryImportUserMapping,
  UpdateHistoryImportSourceRequest,
} from "@/api/types";
import { cn } from "@/lib/utils";
import { formatDateTime as formatPreferredDateTime } from "@/lib/datetime";

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

const STATUS_CONFIG = {
  queued: { icon: Clock, color: "text-info", bg: "bg-info/10 border-info/20", label: "Queued" },
  running: {
    icon: Loader2,
    color: "text-warning",
    bg: "bg-warning/10 border-warning/20",
    label: "Running",
    spin: true,
  },
  canceling: {
    icon: Loader2,
    color: "text-warning",
    bg: "bg-warning/10 border-warning/20",
    label: "Canceling",
    spin: true,
  },
  completed: {
    icon: CheckCircle2,
    color: "text-success",
    bg: "bg-success/10 border-success/20",
    label: "Completed",
  },
  failed: {
    icon: XCircle,
    color: "text-destructive",
    bg: "bg-destructive/10 border-destructive/20",
    label: "Failed",
  },
  cancelled: {
    icon: CircleSlash2,
    color: "text-muted-foreground",
    bg: "bg-muted border-border",
    label: "Cancelled",
  },
} as const;

function StatusBadge({ status }: { status: AdminImportRun["status"] }) {
  const c = STATUS_CONFIG[status];
  const Icon = c.icon;
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs font-medium",
        c.bg,
        c.color,
      )}
    >
      <Icon className={cn("h-3 w-3", "spin" in c && c.spin && "animate-spin")} />
      {c.label}
    </span>
  );
}

function formatDate(dateStr: string | undefined) {
  if (!dateStr) return "—";
  return formatPreferredDateTime(dateStr);
}

// ---------------------------------------------------------------------------
// Source dialogs (create/edit + set token) — infrequent operations, keep as dialogs
// ---------------------------------------------------------------------------

type SourceMode = { kind: "create" } | { kind: "edit"; source: HistoryImportSource };

const SOURCE_HINTS = {
  jellyfin: { name: "My Jellyfin Server", url: "https://jellyfin.example.com" },
  emby: { name: "My Emby Server", url: "https://emby.example.com" },
  plex: { name: "My Plex Server", url: "https://plex.example.com:32400" },
} as const;

function SourceDialog({
  mode,
  open,
  onClose,
}: {
  mode: SourceMode;
  open: boolean;
  onClose: () => void;
}) {
  const isEdit = mode.kind === "edit";
  const [existing, setExisting] = useState(isEdit ? mode.source : null);
  const [conflict, setConflict] = useState(false);
  const [clearCredential, setClearCredential] = useState(false);
  const [name, setName] = useState(existing?.name ?? "");
  const [sourceType, setSourceType] = useState<"emby" | "jellyfin" | "plex">(
    (existing?.source_type as "emby" | "jellyfin" | "plex") ?? "jellyfin",
  );
  const [baseURL, setBaseURL] = useState(existing?.base_url ?? "");
  const [enabled, setEnabled] = useState(existing?.enabled ?? true);
  const [adminToken, setAdminToken] = useState("");
  const [showToken, setShowToken] = useState(false);
  const [plexUser, setPlexUser] = useState("");
  const [plexPass, setPlexPass] = useState("");
  const [tokenMode, setTokenMode] = useState<"login" | "token">(
    sourceType === "plex" ? "login" : "token",
  );
  const create = useCreateAdminHistoryImportSource();
  const update = useUpdateAdminHistoryImportSource();
  const plexLogin = usePlexLogin();
  const isPending = create.isPending || update.isPending || plexLogin.isPending;
  const reload = async () => {
    if (!existing) return;
    const fresh = await getAdminImportSource(existing.id);
    setExisting(fresh);
    setName(fresh.name);
    setBaseURL(fresh.base_url ?? "");
    setEnabled(fresh.enabled);
    setAdminToken("");
    setClearCredential(false);
    setConflict(false);
  };

  const hints = SOURCE_HINTS[sourceType] || SOURCE_HINTS.jellyfin;
  const isPlex = sourceType === "plex";

  function handleSave() {
    if (!name.trim() || !baseURL.trim()) return;
    if (isEdit && existing) {
      const body: UpdateHistoryImportSourceRequest = {
        name: name.trim(),
        base_url: baseURL.trim(),
        enabled,
        admin_token: clearCredential ? "" : adminToken.trim() || undefined,
      };
      update.mutate(
        { id: existing.id, body, etag: existing.etag },
        { onSuccess: onClose, onError: (error) => setConflict(isAdminImportConflict(error)) },
      );
    } else if (isPlex && tokenMode === "login" && plexUser.trim() && plexPass) {
      plexLogin.mutate(
        { username: plexUser.trim(), password: plexPass },
        {
          onSuccess: (data) => {
            create.mutate(
              {
                name: name.trim(),
                source_type: sourceType,
                base_url: baseURL.trim(),
                enabled,
                sort_order: 0,
                admin_token: data.token,
              },
              { onSuccess: onClose },
            );
          },
        },
      );
    } else {
      const body: CreateHistoryImportSourceRequest = {
        name: name.trim(),
        source_type: sourceType,
        base_url: baseURL.trim(),
        enabled,
        sort_order: 0,
        admin_token: adminToken.trim() || undefined,
      };
      create.mutate(body, { onSuccess: onClose });
    }
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{isEdit ? "Edit server" : "Add source server"}</DialogTitle>
        </DialogHeader>
        <div className="space-y-4 py-2">
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-1.5">
              <Label htmlFor="src-name">Name</Label>
              <Input
                id="src-name"
                placeholder={hints.name}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            {!isEdit ? (
              <div className="space-y-1.5">
                <Label>Type</Label>
                <Select
                  value={sourceType}
                  onValueChange={(v) => {
                    const t = v as "emby" | "jellyfin" | "plex";
                    setSourceType(t);
                    setTokenMode(t === "plex" ? "login" : "token");
                  }}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="jellyfin">Jellyfin</SelectItem>
                    <SelectItem value="emby">Emby</SelectItem>
                    <SelectItem value="plex">Plex</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            ) : (
              <div className="space-y-1.5">
                <Label>Type</Label>
                <Input value={existing?.source_type ?? ""} disabled className="capitalize" />
              </div>
            )}
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="src-url">Server URL</Label>
            <Input
              id="src-url"
              placeholder={hints.url}
              value={baseURL}
              onChange={(e) => setBaseURL(e.target.value)}
            />
          </div>
          {isPlex ? <PlexAdminImportLimits /> : null}

          {existing?.needs_reconfiguration ? (
            <p role="alert" className="text-warning text-sm">
              This saved address contains unsupported credential or query settings. Review the
              address and replace or clear its credential before using the source.
            </p>
          ) : null}
          {isEdit ? (
            <div className="space-y-2">
              <Label htmlFor="src-replacement-token">Replacement admin credential</Label>
              <Input
                id="src-replacement-token"
                type="password"
                value={adminToken}
                disabled={clearCredential}
                onChange={(e) => setAdminToken(e.target.value)}
                placeholder="Leave blank to keep the saved credential"
              />
              <label className="flex items-center gap-2 text-sm">
                <Switch checked={clearCredential} onCheckedChange={setClearCredential} />
                Clear the saved credential
              </label>
              <p className="text-muted-foreground text-xs">
                Changing the server address requires a replacement credential or clearing the saved
                one.
              </p>
            </div>
          ) : null}
          {conflict ? <ImportEditorConflict onReload={reload} /> : null}
          {/* Token / login section (create mode only) */}
          {!isEdit && (
            <>
              {isPlex && (
                <div className="flex items-center gap-1 rounded-lg border p-0.5">
                  <button
                    type="button"
                    onClick={() => setTokenMode("login")}
                    className={cn(
                      "flex-1 rounded-md px-3 py-1.5 text-xs font-medium transition-colors",
                      tokenMode === "login"
                        ? "bg-accent text-foreground"
                        : "text-muted-foreground hover:text-foreground",
                    )}
                  >
                    Sign in with Plex
                  </button>
                  <button
                    type="button"
                    onClick={() => setTokenMode("token")}
                    className={cn(
                      "flex-1 rounded-md px-3 py-1.5 text-xs font-medium transition-colors",
                      tokenMode === "token"
                        ? "bg-accent text-foreground"
                        : "text-muted-foreground hover:text-foreground",
                    )}
                  >
                    Paste token
                  </button>
                </div>
              )}

              {isPlex && tokenMode === "login" ? (
                <div className="space-y-3">
                  <div className="space-y-1.5">
                    <Label htmlFor="plex-user-create">Plex email or username</Label>
                    <Input
                      id="plex-user-create"
                      placeholder="you@example.com"
                      value={plexUser}
                      onChange={(e) => setPlexUser(e.target.value)}
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="plex-pass-create">Plex password</Label>
                    <Input
                      id="plex-pass-create"
                      type="password"
                      value={plexPass}
                      onChange={(e) => setPlexPass(e.target.value)}
                    />
                    <p className="text-muted-foreground text-xs">
                      If your Plex account uses two-step verification, type the 6-digit code
                      straight after your password.
                    </p>
                  </div>
                </div>
              ) : (
                <div className="space-y-1.5">
                  <Label htmlFor="src-token">
                    {isPlex ? "Plex auth token" : "Admin API key"}{" "}
                    <span className="text-muted-foreground font-normal">(optional)</span>
                  </Label>
                  <div className="relative">
                    <Input
                      id="src-token"
                      type={showToken ? "text" : "password"}
                      placeholder={isPlex ? "Paste Plex token here…" : "Paste API key here…"}
                      value={adminToken}
                      onChange={(e) => setAdminToken(e.target.value)}
                      className="pr-10"
                    />
                    <button
                      type="button"
                      onClick={() => setShowToken((v) => !v)}
                      className="text-muted-foreground hover:text-foreground absolute top-1/2 right-3 -translate-y-1/2"
                    >
                      {showToken ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                    </button>
                  </div>
                </div>
              )}
            </>
          )}

          <div className="flex items-center gap-3">
            <Switch id="src-enabled" checked={enabled} onCheckedChange={setEnabled} />
            <Label htmlFor="src-enabled">Enabled</Label>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            onClick={handleSave}
            disabled={
              !name.trim() ||
              !baseURL.trim() ||
              isPending ||
              conflict ||
              (isEdit && !existing?.etag)
            }
          >
            {isPending ? "Saving…" : isEdit ? "Save" : "Add server"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function TokenDialog({
  source: initialSource,
  open,
  onClose,
}: {
  source: HistoryImportSource;
  open: boolean;
  onClose: () => void;
}) {
  const [source, setSource] = useState(initialSource);
  const [conflict, setConflict] = useState(false);
  const reload = async () => {
    setSource(await getAdminImportSource(source.id));
    setToken("");
    setPlexPass("");
    setConflict(false);
  };
  const onError = (error: unknown) => setConflict(isAdminImportConflict(error));
  const isPlex = source.source_type === "plex";
  const [mode, setMode] = useState<"token" | "login">(isPlex ? "login" : "token");
  const [token, setToken] = useState("");
  const [showToken, setShowToken] = useState(false);
  const [plexUser, setPlexUser] = useState("");
  const [plexPass, setPlexPass] = useState("");
  const setToken_ = useSetAdminSourceToken();
  const clearToken = useClearAdminSourceToken();
  const plexLogin = usePlexLogin();

  function handleSaveToken() {
    if (!token.trim()) return;
    setToken_.mutate(
      { id: source.id, body: { token: token.trim() }, etag: source.etag },
      { onSuccess: onClose, onError },
    );
  }

  function handlePlexLogin() {
    if (!plexUser.trim() || !plexPass) return;
    plexLogin.mutate(
      { username: plexUser.trim(), password: plexPass },
      {
        onSuccess: (data) => {
          if (data?.token) {
            setToken_.mutate(
              { id: source.id, body: { token: data.token }, etag: source.etag },
              { onSuccess: onClose, onError },
            );
          }
        },
      },
    );
  }

  const isSaving = setToken_.isPending || plexLogin.isPending || clearToken.isPending;

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Admin API key — {source.name}</DialogTitle>
        </DialogHeader>
        <div className="space-y-4 py-2">
          {/* Mode toggle for Plex sources */}
          {isPlex && (
            <div className="flex items-center gap-1 rounded-lg border p-0.5">
              <button
                type="button"
                onClick={() => setMode("login")}
                className={cn(
                  "flex-1 rounded-md px-3 py-1.5 text-xs font-medium transition-colors",
                  mode === "login"
                    ? "bg-accent text-foreground"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                Sign in with Plex
              </button>
              <button
                type="button"
                onClick={() => setMode("token")}
                className={cn(
                  "flex-1 rounded-md px-3 py-1.5 text-xs font-medium transition-colors",
                  mode === "token"
                    ? "bg-accent text-foreground"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                Paste token
              </button>
            </div>
          )}

          {mode === "login" && isPlex ? (
            <div className="space-y-3">
              <p className="text-muted-foreground text-sm">
                Sign in with your Plex account to generate an admin token automatically.
              </p>
              <div className="space-y-1.5">
                <Label htmlFor="plex-user">Email or username</Label>
                <Input
                  id="plex-user"
                  placeholder="you@example.com"
                  value={plexUser}
                  onChange={(e) => setPlexUser(e.target.value)}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="plex-pass">Password</Label>
                <Input
                  id="plex-pass"
                  type="password"
                  value={plexPass}
                  onChange={(e) => setPlexPass(e.target.value)}
                />
                <p className="text-muted-foreground text-xs">
                  If your Plex account uses two-step verification, type the 6-digit code straight
                  after your password.
                </p>
              </div>
            </div>
          ) : (
            <div className="space-y-3">
              <p className="text-muted-foreground text-sm">
                {isPlex
                  ? "Paste your Plex auth token directly."
                  : "This key is used to discover users on the server and import their watch history into Silo."}
              </p>
              <div className="space-y-1.5">
                <Label htmlFor="admin-token">API key / Token</Label>
                <div className="relative">
                  <Input
                    id="admin-token"
                    type={showToken ? "text" : "password"}
                    placeholder="Paste token here…"
                    value={token}
                    onChange={(e) => setToken(e.target.value)}
                    className="pr-10"
                  />
                  <button
                    type="button"
                    onClick={() => setShowToken((v) => !v)}
                    className="text-muted-foreground hover:text-foreground absolute top-1/2 right-3 -translate-y-1/2"
                  >
                    {showToken ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                  </button>
                </div>
              </div>
            </div>
          )}

          {source.has_admin_token && (
            <p className="text-muted-foreground text-xs">
              A token is already configured. Saving will replace it.
            </p>
          )}
        </div>
        {conflict ? <ImportEditorConflict onReload={reload} /> : null}
        <DialogFooter className="gap-2">
          {source.has_admin_token && (
            <Button
              variant="destructive"
              onClick={() =>
                clearToken.mutate(
                  { id: source.id, etag: source.etag },
                  { onSuccess: onClose, onError },
                )
              }
              disabled={isSaving || conflict || !source.etag}
            >
              Remove
            </Button>
          )}
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          {mode === "login" && isPlex ? (
            <Button
              onClick={handlePlexLogin}
              disabled={!plexUser.trim() || !plexPass || isSaving || conflict || !source.etag}
            >
              {isSaving ? "Signing in…" : "Sign in & save"}
            </Button>
          ) : (
            <Button
              onClick={handleSaveToken}
              disabled={!token.trim() || isSaving || conflict || !source.etag}
            >
              {isSaving ? "Saving…" : "Save"}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// User discovery + mapping dialog
// ---------------------------------------------------------------------------

function DiscoverDialog({
  source,
  existingMappings,
  open,
  onClose,
}: {
  source: HistoryImportSource;
  existingMappings: HistoryImportUserMapping[];
  open: boolean;
  onClose: () => void;
}) {
  const { data: externalUsers, isFetching, refetch, error } = useDiscoverExternalUsers(source.id);
  const { data: users = [] } = useAdminUsers();
  const createMapping = useCreateAdminMapping();
  const [mappingTarget, setMappingTarget] = useState<HistoryImportExternalUser | null>(null);
  const [search, setSearch] = useState("");
  const [userId, setUserId] = useState("");
  const [profileId, setProfileId] = useState("");
  const { data: profiles = [] } = useAdminUserProfiles(userId ? Number(userId) : undefined);

  const mappedIds = useMemo(
    () => new Set(existingMappings.map((m) => m.external_user_id)),
    [existingMappings],
  );

  const unmappedUsers = useMemo(
    () => (externalUsers ?? []).filter((u) => !mappedIds.has(u.id)),
    [externalUsers, mappedIds],
  );

  const filteredUsers = useMemo(() => {
    if (!search.trim()) return unmappedUsers;
    const q = search.toLowerCase();
    return unmappedUsers.filter(
      (u) => u.name.toLowerCase().includes(q) || u.id.toLowerCase().includes(q),
    );
  }, [unmappedUsers, search]);
  const discoverErrorMessage =
    error instanceof Error && error.message.trim()
      ? error.message
      : "Could not connect. Check the server URL and admin token, then try again.";

  function handleSave() {
    if (!mappingTarget || !userId || !profileId) return;
    createMapping.mutate(
      {
        source_id: source.id,
        external_user_id: mappingTarget.id,
        external_user_name: mappingTarget.name,
        silo_user_id: Number(userId),
        silo_profile_id: profileId,
      },
      {
        onSuccess: () => {
          setMappingTarget(null);
          setUserId("");
          setProfileId("");
        },
      },
    );
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Discover users on {source.name}</DialogTitle>
        </DialogHeader>

        <div className="space-y-4 py-2">
          {/* Trigger */}
          {!externalUsers && !isFetching && !error && (
            <div className="surface-panel-subtle flex flex-col items-center gap-3 rounded-xl p-8 text-center">
              <Search className="text-muted-foreground h-8 w-8" />
              <p className="text-muted-foreground text-sm">
                Query the server to find user accounts available for import.
              </p>
              <Button onClick={() => refetch()} size="sm">
                Discover users
              </Button>
            </div>
          )}

          {isFetching && (
            <div className="flex items-center justify-center gap-2 py-8 text-sm">
              <Loader2 className="text-muted-foreground h-4 w-4 animate-spin" />
              <span className="text-muted-foreground">Connecting to server…</span>
            </div>
          )}

          {error && (
            <div className="space-y-3">
              <div className="bg-destructive/10 border-destructive/20 flex items-start gap-2 rounded-xl border p-3 text-sm">
                <AlertTriangle className="text-destructive mt-0.5 h-4 w-4 shrink-0" />
                <span>{discoverErrorMessage}</span>
              </div>
              <Button variant="outline" size="sm" onClick={() => refetch()}>
                Retry
              </Button>
            </div>
          )}

          {/* Results */}
          {externalUsers && !isFetching && (
            <>
              <div className="flex items-center justify-between">
                <p className="text-muted-foreground text-sm">
                  {unmappedUsers.length === 0
                    ? "All users are already mapped."
                    : `${unmappedUsers.length} unmapped user${unmappedUsers.length !== 1 ? "s" : ""} found`}
                </p>
                <Button variant="ghost" size="sm" onClick={() => refetch()}>
                  Refresh
                </Button>
              </div>

              {unmappedUsers.length > 0 && (
                <>
                  <div className="relative">
                    <Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
                    <Input
                      placeholder="Search users…"
                      value={search}
                      onChange={(e) => setSearch(e.target.value)}
                      className="pl-9"
                    />
                  </div>
                  <div className="surface-panel-subtle max-h-96 divide-y overflow-y-auto rounded-xl border-0">
                    {filteredUsers.length === 0 ? (
                      <p className="text-muted-foreground px-4 py-6 text-center text-sm">
                        No users matching &ldquo;{search}&rdquo;
                      </p>
                    ) : (
                      filteredUsers.map((u) => (
                        <button
                          key={u.id}
                          type="button"
                          onClick={() => {
                            setMappingTarget(u);
                            setUserId("");
                            setProfileId("");
                          }}
                          className={cn(
                            "flex w-full items-center justify-between px-4 py-2.5 text-left transition-colors",
                            mappingTarget?.id === u.id ? "bg-accent" : "hover:bg-accent/50",
                          )}
                        >
                          <div className="min-w-0">
                            <p className="text-sm font-medium">{u.name}</p>
                            <p className="text-muted-foreground truncate text-xs">{u.id}</p>
                          </div>
                          {mappingTarget?.id === u.id ? (
                            <Badge variant="outline" className="shrink-0 text-xs">
                              Selected
                            </Badge>
                          ) : (
                            <ChevronRight className="text-muted-foreground h-4 w-4 shrink-0" />
                          )}
                        </button>
                      ))
                    )}
                  </div>
                </>
              )}

              {/* Mapping form — shown when a user is selected */}
              {mappingTarget && (
                <div className="surface-panel-subtle space-y-4 rounded-xl border-0 p-4">
                  <p className="text-sm font-medium">
                    Map <span className="text-primary">{mappingTarget.name}</span> to a Silo user
                    and profile:
                  </p>
                  <div className="grid grid-cols-2 gap-3">
                    <div className="space-y-1.5">
                      <Label className="text-xs">Silo user</Label>
                      <Select
                        value={userId}
                        onValueChange={(v) => {
                          setUserId(v);
                          setProfileId("");
                        }}
                      >
                        <SelectTrigger>
                          <SelectValue placeholder="Select user…" />
                        </SelectTrigger>
                        <SelectContent>
                          {users.map((u) => (
                            <SelectItem key={u.id} value={String(u.id)}>
                              {u.username}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                    <div className="space-y-1.5">
                      <Label className="text-xs">Profile</Label>
                      <Select value={profileId} onValueChange={setProfileId} disabled={!userId}>
                        <SelectTrigger>
                          <SelectValue placeholder="Select profile…" />
                        </SelectTrigger>
                        <SelectContent>
                          {profiles.map((p) => (
                            <SelectItem key={p.id} value={p.id}>
                              {p.name}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                  </div>
                  <div className="flex items-center gap-2">
                    <Button
                      size="sm"
                      onClick={handleSave}
                      disabled={!userId || !profileId || createMapping.isPending}
                    >
                      {createMapping.isPending ? (
                        <>
                          <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                          Saving…
                        </>
                      ) : (
                        "Save mapping"
                      )}
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => setMappingTarget(null)}>
                      Cancel
                    </Button>
                  </div>
                </div>
              )}
            </>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Done
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Source selector bar
// ---------------------------------------------------------------------------

function SourceBar({
  sources,
  selected,
  onSelect,
  onAdd,
  onEdit,
  onDelete,
  onSetToken,
}: {
  sources: HistoryImportSource[];
  selected: HistoryImportSource | undefined;
  onSelect: (id: number) => void;
  onAdd: () => void;
  onEdit: (s: HistoryImportSource) => void;
  onDelete: (s: HistoryImportSource) => void;
  onSetToken: (s: HistoryImportSource) => void;
}) {
  if (sources.length === 0) {
    return (
      <div className="surface-panel-subtle flex flex-col items-center gap-4 rounded-xl border-0 px-6 py-12 text-center">
        <div className="bg-accent flex h-12 w-12 items-center justify-center rounded-xl">
          <Server className="text-muted-foreground h-6 w-6" />
        </div>
        <div className="space-y-1">
          <p className="text-sm font-medium">No source servers</p>
          <p className="text-muted-foreground max-w-sm text-sm">
            Add the Jellyfin, Emby, or Plex server you want to import watch history from.
          </p>
        </div>
        <Button size="sm" onClick={onAdd}>
          <Plus className="mr-2 h-4 w-4" />
          Add server
        </Button>
      </div>
    );
  }

  return (
    <div className="surface-panel overflow-hidden rounded-2xl border-0">
      <div className="flex items-center justify-between border-b px-4 py-3">
        <div className="flex items-center gap-3">
          <Select
            value={selected ? String(selected.id) : ""}
            onValueChange={(v) => onSelect(Number(v))}
          >
            <SelectTrigger className="w-56">
              <SelectValue placeholder="Select a server…" />
            </SelectTrigger>
            <SelectContent>
              {sources.map((s) => (
                <SelectItem key={s.id} value={String(s.id)}>
                  <span className="flex items-center gap-2">
                    {s.name}
                    <Badge variant="outline" className="text-[10px] capitalize">
                      {s.source_type}
                    </Badge>
                  </span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {selected && (
            <span className="text-muted-foreground hidden text-xs sm:inline">
              {selected.base_url}
            </span>
          )}
        </div>
        <div className="flex items-center gap-1">
          {selected && (
            <>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => onSetToken(selected)}
                title="Set API key"
              >
                <KeyRound className="h-3.5 w-3.5" />
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => onEdit(selected)}
                title="Edit server"
              >
                <Pencil className="h-3.5 w-3.5" />
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => onDelete(selected)}
                title="Delete server"
              >
                <Trash2 className="text-destructive h-3.5 w-3.5" />
              </Button>
            </>
          )}
          <Button size="sm" variant="outline" onClick={onAdd}>
            <Plus className="mr-1.5 h-3.5 w-3.5" />
            Add
          </Button>
        </div>
      </div>

      {/* Token status callout */}
      {selected && !selected.has_admin_token && (
        <div className="bg-warning/5 flex items-center gap-3 border-b px-4 py-3">
          <AlertTriangle className="text-warning h-4 w-4 shrink-0" />
          <p className="text-muted-foreground flex-1 text-sm">
            No admin API key configured. Add one to discover users and run imports.
          </p>
          <Button size="sm" variant="outline" onClick={() => onSetToken(selected)}>
            <KeyRound className="mr-1.5 h-3.5 w-3.5" />
            Set API key
          </Button>
        </div>
      )}

      {selected && selected.has_admin_token && (
        <div className="flex items-center gap-2 px-4 py-2">
          <span className="bg-success/20 inline-flex h-2 w-2 rounded-full" />
          <span className="text-muted-foreground text-xs">API key configured</span>
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// User mappings section
// ---------------------------------------------------------------------------

function MappingsSection({
  source,
  mappings,
}: {
  source: HistoryImportSource;
  mappings: HistoryImportUserMapping[];
}) {
  const [discoverOpen, setDiscoverOpen] = useState(false);
  const deleteMapping = useDeleteAdminMapping();
  const createRun = useCreateAdminRunForMapping();
  const bulkRun = useAdminBulkRun();
  const [deleteTarget, setDeleteTarget] = useState<HistoryImportUserMapping | null>(null);
  const [deleteConflict, setDeleteConflict] = useState(false);
  const { data: users = [] } = useAdminUsers();

  if (!source.has_admin_token || source.needs_reconfiguration) return null;

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold tracking-wide">User mappings</h2>
        <div className="flex items-center gap-2">
          {mappings.length > 0 && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => bulkRun.mutate(source.id)}
              disabled={bulkRun.isPending || mappings.length > 200}
            >
              {bulkRun.isPending ? (
                <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
              ) : (
                <Play className="mr-1.5 h-3.5 w-3.5" />
              )}
              Import all
            </Button>
          )}
          <Button size="sm" onClick={() => setDiscoverOpen(true)}>
            <Search className="mr-1.5 h-3.5 w-3.5" />
            Discover users
          </Button>
        </div>
      </div>

      {bulkRun.data ? (
        <div role="status" className="space-y-1 text-sm">
          <p>
            {bulkRun.data.accepted} queued, {bulkRun.data.active} already active,{" "}
            {bulkRun.data.failed} failed.
          </p>
          {bulkRun.data.outcomes.map((outcome) => (
            <p key={outcome.mapping_id}>
              Mapping {outcome.mapping_id}: {outcome.status}
              {outcome.error ? ` — ${outcome.error}` : ""}
            </p>
          ))}
        </div>
      ) : null}
      {mappings.length > 200 ? (
        <p className="text-sm">
          Bulk imports support up to 200 mappings. Start individual imports for this source.
        </p>
      ) : null}
      {mappings.length === 0 ? (
        <div className="surface-panel-subtle flex flex-col items-center gap-3 rounded-xl border-0 py-10 text-center">
          <p className="text-muted-foreground text-sm">
            No user mappings yet. Discover users on the server to create mappings.
          </p>
          <Button size="sm" variant="outline" onClick={() => setDiscoverOpen(true)}>
            <Search className="mr-1.5 h-3.5 w-3.5" />
            Discover users
          </Button>
        </div>
      ) : (
        <div className="surface-panel overflow-x-auto rounded-2xl border-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Source user</TableHead>
                <TableHead className="hidden sm:table-cell">
                  <ArrowRight className="h-3.5 w-3.5" />
                </TableHead>
                <TableHead>Silo user</TableHead>

                <TableHead className="w-24 text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {mappings.map((m) => (
                <TableRow key={m.id}>
                  <TableCell>
                    <p className="text-sm font-medium">
                      {m.external_user_name || m.external_user_id}
                    </p>
                  </TableCell>
                  <TableCell className="text-muted-foreground hidden sm:table-cell">
                    <ArrowRight className="h-3.5 w-3.5" />
                  </TableCell>
                  <TableCell>
                    <p className="text-sm">
                      {users.find((user) => user.id === m.silo_user_id)?.username ||
                        `User ${m.silo_user_id}`}
                    </p>
                    {m.silo_profile_name && (
                      <p className="text-muted-foreground text-xs">{m.silo_profile_name}</p>
                    )}
                  </TableCell>

                  <TableCell>
                    <div className="flex items-center justify-end gap-1">
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => createRun.mutate(m.id)}
                        disabled={createRun.isPending}
                        title="Run import"
                      >
                        <Play className="h-3.5 w-3.5" />
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => {
                          setDeleteTarget(m);
                          setDeleteConflict(false);
                        }}
                        title="Remove mapping"
                      >
                        <Trash2 className="text-destructive h-3.5 w-3.5" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      {discoverOpen && (
        <DiscoverDialog
          source={source}
          existingMappings={mappings}
          open
          onClose={() => setDiscoverOpen(false)}
        />
      )}

      <ImportConfirmDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteTarget(null);
        }}
        isPending={deleteMapping.isPending || deleteConflict || !deleteTarget?.etag}
        conflict={deleteConflict}
        onReload={async () => {
          if (deleteTarget) {
            setDeleteTarget(await getAdminImportMapping(deleteTarget.id));
            setDeleteConflict(false);
          }
        }}
        title="Remove mapping"
        description={`Remove the mapping for "${deleteTarget?.external_user_name || deleteTarget?.external_user_id}"? This won't delete any imported history.`}
        confirmLabel="Remove"
        variant="destructive"
        onConfirm={() => {
          if (deleteTarget)
            deleteMapping.mutate(
              { id: deleteTarget.id, etag: deleteTarget.etag },
              {
                onSuccess: () => setDeleteTarget(null),
                onError: (error) => setDeleteConflict(isAdminImportConflict(error)),
              },
            );
        }}
      />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Recent import runs section
// ---------------------------------------------------------------------------

type RunFilter = "all" | "admin" | "user";

function RunsSection({ sourceId }: { sourceId: number }) {
  const {
    data: allRuns = [],
    hasNextPage,
    fetchNextPage,
    isFetchingNextPage,
    error,
  } = useAdminHistoryImportRuns(sourceId);
  const { data: users = [] } = useAdminUsers();
  const cancelRun = useCancelAdminRun();
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [filter, setFilter] = useState<RunFilter>("all");

  const userMap = useMemo(() => {
    const m = new Map<number, string>();
    for (const u of users) m.set(u.id, u.username);
    return m;
  }, [users]);

  const runs = useMemo(() => {
    if (filter === "all") return allRuns;
    if (filter === "admin") return allRuns.filter((r) => r.connection_mode === "admin_token");
    return allRuns.filter((r) => r.connection_mode !== "admin_token");
  }, [allRuns, filter]);

  if (allRuns.length === 0 && !error) return null;

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold tracking-wide">Recent imports</h2>
        <div className="flex items-center gap-1 rounded-lg border p-0.5">
          {(["all", "admin", "user"] as const).map((v) => (
            <button
              key={v}
              type="button"
              onClick={() => setFilter(v)}
              className={cn(
                "rounded-md px-2.5 py-1 text-xs font-medium capitalize transition-colors",
                filter === v
                  ? "bg-accent text-foreground"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              {v}
            </button>
          ))}
        </div>
      </div>
      <div className="surface-panel overflow-hidden rounded-2xl border-0">
        {runs.length === 0 ? (
          <p className="text-muted-foreground px-4 py-6 text-center text-sm">
            No {filter === "all" ? "" : filter + " "}imports to show.
          </p>
        ) : (
          <div className="divide-y">
            {runs.map((run) => {
              const expanded = expandedId === run.id;
              return (
                <div key={run.id}>
                  <div className="flex items-center gap-3 px-4 py-3">
                    <button
                      onClick={() => setExpandedId(expanded ? null : run.id)}
                      className="text-muted-foreground hover:text-foreground shrink-0"
                    >
                      {expanded ? (
                        <ChevronDown className="h-4 w-4" />
                      ) : (
                        <ChevronRight className="h-4 w-4" />
                      )}
                    </button>
                    <StatusBadge status={run.status} />
                    <div className="flex-1 space-y-0.5">
                      <div className="flex items-center gap-2">
                        <p className="text-sm font-medium capitalize">{run.source_type} import</p>
                        {run.connection_mode === "admin_token" ? (
                          <Badge variant="outline" className="text-[10px]">
                            Admin
                          </Badge>
                        ) : (
                          <Badge variant="secondary" className="text-[10px]">
                            Self
                          </Badge>
                        )}
                      </div>
                      <p className="text-muted-foreground text-xs">
                        {userMap.get(run.user_id) ?? `User ${run.user_id}`}
                        {" · "}
                        {formatDate(run.created_at)}
                      </p>
                    </div>
                    <div className="text-muted-foreground hidden items-center gap-4 text-xs sm:flex">
                      {run.fetched > 0 && <span>{run.fetched} fetched</span>}
                      {run.matched > 0 && (
                        <span className="text-success">{run.matched} matched</span>
                      )}
                      {run.unmatched > 0 && (
                        <span className="text-warning">{run.unmatched} unmatched</span>
                      )}
                    </div>
                    {run.cancelable && (
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => cancelRun.mutate(run.id)}
                        disabled={cancelRun.isPending}
                        className="text-destructive hover:text-destructive"
                      >
                        Cancel
                      </Button>
                    )}
                  </div>
                  {expanded && (
                    <div className="bg-accent/30 space-y-3 px-11 pb-4">
                      {run.error_message && (
                        <div className="bg-destructive/10 border-destructive/20 flex items-start gap-2 rounded-lg border p-3 text-sm">
                          <AlertTriangle className="text-destructive mt-0.5 h-4 w-4 shrink-0" />
                          {run.error_message}
                        </div>
                      )}
                      <div className="text-muted-foreground grid grid-cols-3 gap-2 text-xs sm:grid-cols-6">
                        <div>
                          <p className="font-medium">Fetched</p>
                          <p>{run.fetched}</p>
                        </div>
                        <div>
                          <p className="font-medium">Matched</p>
                          <p>{run.matched}</p>
                        </div>
                        <div>
                          <p className="font-medium">Unmatched</p>
                          <p>{run.unmatched}</p>
                        </div>
                        <div>
                          <p className="font-medium">Updated</p>
                          <p>{run.progress_updated}</p>
                        </div>
                        <div>
                          <p className="font-medium">History</p>
                          <p>{run.history_created}</p>
                        </div>
                        <div>
                          <p className="font-medium">Favorites</p>
                          <p>{run.favorites_imported}</p>
                        </div>
                        <div>
                          <p className="font-medium">Watchlist</p>
                          <p>{run.watchlist_added}</p>
                        </div>
                        <div>
                          <p className="font-medium">Skipped</p>
                          <p>{run.skipped}</p>
                        </div>
                      </div>
                      {run.warnings.length > 0 && (
                        <div className="space-y-1">
                          <p className="text-muted-foreground text-xs font-medium">
                            Warnings ({run.warnings.length})
                          </p>
                          <ul className="text-muted-foreground list-inside list-disc space-y-0.5 text-xs">
                            {run.warnings.slice(0, 5).map((w, i) => (
                              <li key={i}>{w}</li>
                            ))}
                            {run.warnings.length > 5 && (
                              <li className="italic">and {run.warnings.length - 5} more…</li>
                            )}
                          </ul>
                        </div>
                      )}
                      {run.unmatched_samples.length > 0 && (
                        <div className="space-y-1">
                          <p className="text-muted-foreground text-xs font-medium">
                            Unmatched samples
                          </p>
                          <ul className="text-muted-foreground list-inside list-disc space-y-0.5 text-xs">
                            {run.unmatched_samples.map((s, i) => (
                              <li key={i}>
                                {s.title}
                                {s.year ? ` (${s.year})` : ""} — {s.reason}
                              </li>
                            ))}
                          </ul>
                        </div>
                      )}
                      {!run.error_message &&
                        run.warnings.length === 0 &&
                        run.unmatched_samples.length === 0 && (
                          <p className="text-muted-foreground text-xs">No issues.</p>
                        )}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </div>
      {error ? (
        <p role="alert" className="text-destructive text-sm">
          Import statuses could not be refreshed. Reload to try again.
        </p>
      ) : null}
      {hasNextPage ? (
        <Button
          variant="outline"
          disabled={isFetchingNextPage}
          onClick={() => void fetchNextPage()}
        >
          Load more imports
        </Button>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export default function AdminHistoryImport() {
  useOptionalAuth();
  return <AdminHistoryImportPage key={adminImportScope()} />;
}

function AdminHistoryImportPage() {
  useEventChannel("history_import");
  const capabilities = useAdminHistoryImportCapabilities();
  const {
    data: sources = [],
    isLoading: sourcesLoading,
    error: sourcesError,
  } = useAdminHistoryImportSources();
  const [searchParams, setSearchParams] = useSearchParams();
  const [sourceMode, setSourceMode] = useState<SourceMode | null>(null);
  const [tokenSource, setTokenSource] = useState<HistoryImportSource | null>(null);
  const [deleteSource, setDeleteSource] = useState<HistoryImportSource | null>(null);
  const deleteMutation = useDeleteAdminHistoryImportSource();
  const [deleteConflict, setDeleteConflict] = useState(false);

  // Persist selected source in URL so it survives page refresh.
  const selectedId = searchParams.get("source") ? Number(searchParams.get("source")) : null;
  const setSelectedId = useCallback(
    (id: number) => setSearchParams({ source: String(id) }, { replace: true }),
    [setSearchParams],
  );

  // Auto-select first source if none selected or selected source no longer exists.
  const effectiveId =
    selectedId && sources.some((s) => s.id === selectedId) ? selectedId : (sources[0]?.id ?? null);
  const selected = sources.find((s) => s.id === effectiveId);

  // Query runs at page level so we can pass hasActiveRuns to mappings for auto-refresh.
  const { data: runs = [] } = useAdminHistoryImportRuns(
    effectiveId ?? undefined,
    effectiveId != null,
  );
  const hasActiveRuns = runs.some(importRunActive);

  const mappingsQuery = useAdminHistoryImportMappings(
    selected?.has_admin_token ? (effectiveId ?? undefined) : undefined,
    hasActiveRuns,
  );

  if (capabilities.isLoading || sourcesLoading)
    return <p>Loading history import administration…</p>;
  if (
    !capabilities.data?.available ||
    !capabilities.data.guarded_configuration ||
    !capabilities.data.durable_runs
  )
    return <p role="alert">History import administration is unavailable on this server.</p>;
  if (sourcesError && sources.length === 0)
    return <p role="alert">Saved servers could not be loaded.</p>;
  return (
    <div className="page-shell space-y-8 py-4 sm:py-6">
      <div className="page-header">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">History Import</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Import watch history from external servers into Silo user profiles.
          </p>
        </div>
      </div>

      {/* Source selector */}
      <SourceBar
        sources={sources}
        selected={selected}
        onSelect={setSelectedId}
        onAdd={() => setSourceMode({ kind: "create" })}
        onEdit={(s) => setSourceMode({ kind: "edit", source: s })}
        onDelete={(source) => {
          setDeleteSource(source);
          setDeleteConflict(false);
        }}
        onSetToken={setTokenSource}
      />

      {selected?.source_type === "plex" ? <PlexAdminImportLimits /> : null}
      {selected?.needs_reconfiguration ? (
        <p role="alert" className="text-warning text-sm">
          This source needs reconfiguration. Edit the server address and credential before
          discovering users or starting imports.
        </p>
      ) : null}
      {/* Mappings */}
      {selected &&
        (mappingsQuery.isError ? (
          <div role="alert" className="space-y-2">
            <p>User mappings could not be loaded.</p>
            <Button
              variant="outline"
              disabled={mappingsQuery.isFetching}
              onClick={() => void mappingsQuery.refetch()}
            >
              Retry user mappings
            </Button>
          </div>
        ) : selected.has_admin_token && mappingsQuery.isPending ? (
          <p role="status">Loading user mappings…</p>
        ) : (
          <MappingsSection
            key={`mappings:${selected.id}`}
            source={selected}
            mappings={mappingsQuery.data ?? []}
          />
        ))}

      {/* Recent runs */}
      {selected && effectiveId && (
        <RunsSection key={`runs:${effectiveId}`} sourceId={effectiveId} />
      )}

      {/* Dialogs */}
      {sourceMode && <SourceDialog mode={sourceMode} open onClose={() => setSourceMode(null)} />}
      {tokenSource && (
        <TokenDialog source={tokenSource} open onClose={() => setTokenSource(null)} />
      )}
      <ImportConfirmDialog
        open={deleteSource !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteSource(null);
        }}
        isPending={deleteMutation.isPending || deleteConflict || !deleteSource?.etag}
        conflict={deleteConflict}
        onReload={async () => {
          if (deleteSource) {
            setDeleteSource(await getAdminImportSource(deleteSource.id));
            setDeleteConflict(false);
          }
        }}
        title="Delete server"
        description={`Delete "${deleteSource?.name}"? Remove its user mappings first.`}
        confirmLabel="Delete"
        variant="destructive"
        onConfirm={() => {
          if (deleteSource)
            deleteMutation.mutate(
              { id: deleteSource.id, etag: deleteSource.etag },
              {
                onSuccess: () => setDeleteSource(null),
                onError: (error) => setDeleteConflict(isAdminImportConflict(error)),
              },
            );
        }}
      />
    </div>
  );
}

// PlexAdminImportLimits states what an admin-token Plex import cannot read. Plex
// session history only records finished plays; resume points, titles marked
// watched, and watchlists are per-user state that only the user's own sign-in reaches.
function PlexAdminImportLimits() {
  const headingId = useId();
  return (
    <div
      role="note"
      aria-labelledby={headingId}
      className="border-warning/40 bg-warning/10 flex max-w-3xl gap-3 rounded-xl border p-4 text-sm"
    >
      <AlertTriangle className="text-warning mt-0.5 h-4 w-4 shrink-0" />
      <div className="space-y-2">
        <p id={headingId} className="font-medium">
          Plex admin imports only bring over finished plays
        </p>
        <p>
          Silo reads this server&apos;s play history, which records each title a person played
          through, and when. It can&apos;t see anything else, so these are not imported:
        </p>
        <ul className="list-disc space-y-0.5 pl-5">
          <li>Resume points for titles still in progress</li>
          <li>Titles, seasons, or shows marked as watched without playing them</li>
          <li>Watchlists</li>
        </ul>
        <p>
          To bring those over, each person imports from their own Plex account in Settings &rarr;
          History Import.
        </p>
      </div>
    </div>
  );
}

function ImportEditorConflict({ onReload }: { onReload: () => Promise<void> }) {
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string>();
  return (
    <div role="alert" className="space-y-2 text-sm">
      <p>
        This configuration changed. Your draft or confirmation is kept. Reload the latest version
        before trying again.
      </p>
      {error ? <p>{error}</p> : null}
      <Button
        type="button"
        variant="outline"
        disabled={loading}
        onClick={async () => {
          setLoading(true);
          try {
            await onReload();
          } catch (error) {
            setError(error instanceof Error ? error.message : "Reload failed");
          } finally {
            setLoading(false);
          }
        }}
      >
        Reload latest version
      </Button>
    </div>
  );
}
function ImportConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  onConfirm,
  isPending,
  conflict,
  onReload,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: string;
  confirmLabel: string;
  variant?: string;
  onConfirm: () => void;
  isPending?: boolean;
  conflict: boolean;
  onReload: () => Promise<void>;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <p className="text-sm">{description}</p>
        {conflict ? <ImportEditorConflict onReload={onReload} /> : null}
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button variant="destructive" disabled={isPending} onClick={onConfirm}>
            {confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
