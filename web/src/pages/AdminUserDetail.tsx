import { useId, useMemo, useState } from "react";
import type { FormEvent } from "react";
import { useParams, Link } from "react-router";
import {
  type AdminDeviceSetting,
  useAdminUser,
  useUpdateUser,
  useDeleteUser,
  useImpersonateUser,
  useAdminUserDeviceSettings,
  useAdminUserSettings,
  useDeleteAdminUserDeviceSetting,
  useDeleteAdminUserSetting,
  useDeleteAllAdminUserDeviceSettingsForDevice,
  useUpdateAdminUserDeviceSetting,
  useUpdateAdminUserSetting,
} from "@/hooks/queries/admin/users";
import { useAccessGroups } from "@/hooks/queries/admin/accessGroups";
import { useAdminUserProfiles } from "@/hooks/queries/admin/history";
import { useAdminPlaybackHistory } from "@/hooks/queries/admin/history";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { useUserIPs } from "@/hooks/queries/admin/ips";
import type {
  AdminSettingEntry,
  AdminUser,
  AdminUserProfile,
  UpdateUserRequest,
  UserIPEntry,
} from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { LibraryAccessSelector } from "@/components/LibraryAccessSelector";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
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
import { ArrowUpRight, ChevronRight, Pencil, RotateCcw, Settings2, UserCircle } from "lucide-react";
import { useNavigate } from "react-router";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { useAuth } from "@/hooks/useAuth";
import {
  PLAYBACK_QUALITY_OPTIONS,
  formatPlaybackQualityPreset,
  playbackQualityPresetFromValue,
  playbackQualityValueFromPreset,
  type PlaybackQualityPreset,
} from "@/lib/playback-quality";
import {
  PERMISSION_MARKER_EDIT,
  PERMISSION_METADATA_CURATION,
  hasAssignedPermission,
  setAssignedPermission,
} from "@/lib/permissions";
import { RegistrySettingControl } from "@/components/settings/RegistrySettingControl";
import { formatSettingValue, getSettingDefinition } from "@/lib/settingsManifest";
import {
  DeviceProfileTabs,
  PlatformTile,
  UNKNOWN_PROFILE_ID,
  classifyPlatform,
  formatRelative,
  platformLabel,
  shortenId,
  type DeviceProfileTabEntry,
} from "@/components/admin/deviceOverrides";
import { toast } from "sonner";
import {
  formatDate as formatPreferredDate,
  formatDateTime as formatDateTimePreferred,
} from "@/lib/datetime";

export default function AdminUserDetail() {
  const { id } = useParams<{ id: string }>();
  const userId = Number(id);
  const navigate = useNavigate();
  const { beginImpersonation } = useAuth();
  const { data: user, isLoading, error } = useAdminUser(userId);
  const [editOpen, setEditOpen] = useState(false);
  const [confirmDeleteOpen, setConfirmDeleteOpen] = useState(false);
  const [confirmImpersonateOpen, setConfirmImpersonateOpen] = useState(false);
  const deleteMutation = useDeleteUser();
  const impersonateMutation = useImpersonateUser();

  if (isLoading) return <div className="page-shell py-8">Loading user...</div>;
  if (error || !user)
    return <div className="page-shell text-destructive py-8">User not found.</div>;

  const impersonationDisabled = user.role === "admin" || !user.enabled;

  function handleDelete() {
    setConfirmDeleteOpen(true);
  }

  return (
    <div className="page-shell space-y-6 py-4 sm:py-6">
      <nav
        aria-label="Breadcrumb"
        className="text-muted-foreground flex items-center gap-1.5 text-sm"
      >
        <Link to="/admin" className="hover:text-foreground transition-colors">
          Admin
        </Link>
        <ChevronRight className="h-3.5 w-3.5" />
        <Link to="/admin/users" className="hover:text-foreground transition-colors">
          Users
        </Link>
        <ChevronRight className="h-3.5 w-3.5" />
        <span className="text-foreground font-medium">{user.username}</span>
      </nav>

      <div className="page-header gap-5">
        <div className="min-w-0 flex-1 space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">{user.username}</h1>
            <Badge variant={user.role === "admin" ? "default" : "secondary"}>{user.role}</Badge>
            <Badge variant={user.enabled ? "outline" : "destructive"}>
              {user.enabled ? "Active" : "Disabled"}
            </Badge>
          </div>
          <p className="page-subtitle text-sm sm:text-base">{user.email}</p>
        </div>
        <div className="flex w-full flex-wrap gap-2 sm:w-auto">
          <Button
            variant="outline"
            size="sm"
            className="flex-1 sm:flex-none"
            onClick={() => setConfirmImpersonateOpen(true)}
            disabled={impersonationDisabled || impersonateMutation.isPending}
          >
            Impersonate
          </Button>
          <Dialog open={editOpen} onOpenChange={setEditOpen}>
            <DialogTrigger asChild>
              <Button variant="outline" size="sm" className="flex-1 sm:flex-none">
                <Pencil className="mr-1 h-3.5 w-3.5" /> Edit
              </Button>
            </DialogTrigger>
            <DialogContent className="sm:max-w-2xl">
              <DialogHeader>
                <DialogTitle>Edit User</DialogTitle>
              </DialogHeader>
              <EditUserForm user={user} onClose={() => setEditOpen(false)} />
            </DialogContent>
          </Dialog>
          <Button
            variant="destructive"
            size="sm"
            className="flex-1 sm:flex-none"
            onClick={handleDelete}
            disabled={deleteMutation.isPending}
          >
            Delete
          </Button>
        </div>
      </div>

      <Tabs defaultValue="overview">
        <TabsList
          variant="line"
          className="surface-panel-subtle h-auto w-full justify-start gap-1 overflow-x-auto rounded-xl border-0 bg-transparent p-1"
        >
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
          <TabsTrigger value="devices">Devices</TabsTrigger>
          <TabsTrigger value="profiles">Profiles</TabsTrigger>
          <TabsTrigger value="history">Watch History</TabsTrigger>
          <TabsTrigger value="ips">IP History</TabsTrigger>
        </TabsList>

        <TabsContent value="overview" className="mt-4">
          <OverviewTab user={user} />
        </TabsContent>
        <TabsContent value="settings" className="mt-4">
          <UserSettingsTab userId={userId} />
        </TabsContent>
        <TabsContent value="devices" className="mt-4">
          <DeviceOverridesTab userId={userId} />
        </TabsContent>
        <TabsContent value="profiles" className="mt-4">
          <ProfilesTab userId={userId} />
        </TabsContent>
        <TabsContent value="history" className="mt-4">
          <WatchHistoryTab userId={userId} />
        </TabsContent>
        <TabsContent value="ips" className="mt-4">
          <IPHistoryTab userId={userId} />
        </TabsContent>
      </Tabs>
      <ConfirmDialog
        open={confirmImpersonateOpen}
        onOpenChange={(open) => {
          if (!open) setConfirmImpersonateOpen(false);
        }}
        title="Impersonate user"
        description={`Continue as "${user.username}"? Admin access will be removed until you end impersonation.`}
        confirmLabel="Impersonate"
        onConfirm={() => {
          setConfirmImpersonateOpen(false);
          void impersonateMutation
            .mutateAsync(user.id)
            .then((result) => {
              beginImpersonation(result, `/admin/users/${user.id}`);
              navigate("/profiles");
            })
            .catch((error: unknown) => {
              toast.error(error instanceof Error ? error.message : "Failed to start impersonation");
            });
        }}
      />
      <ConfirmDialog
        open={confirmDeleteOpen}
        onOpenChange={(open) => {
          if (!open) setConfirmDeleteOpen(false);
        }}
        title="Delete user"
        description={`Delete user "${user.username}"? This cannot be undone.`}
        confirmLabel="Delete"
        variant="destructive"
        onConfirm={() => {
          setConfirmDeleteOpen(false);
          deleteMutation.mutate(user.id, {
            onSuccess: () => navigate("/admin/users"),
          });
        }}
      />
    </div>
  );
}

function OverviewTab({ user }: { user: AdminUser }) {
  const { data: libraries = [] } = useAdminLibraries();
  const { data: accessGroups = [] } = useAccessGroups();

  const libraryNames =
    user.library_ids === null
      ? "All libraries"
      : user.library_ids.length === 0
        ? "None"
        : user.library_ids
            .map((id) => {
              const lib = libraries.find((l) => l.id === id);
              return lib ? lib.name : `#${id}`;
            })
            .join(", ");
  const groupName =
    user.access_group_id === null
      ? "None"
      : (accessGroups.find((group) => group.id === user.access_group_id)?.name ??
        `#${user.access_group_id}`);

  return (
    <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
      <div className="surface-panel overflow-hidden rounded-2xl border-0">
        <div className="border-border border-b px-4 py-3">
          <h3 className="text-sm font-medium">Account</h3>
        </div>
        <div className="divide-border divide-y">
          <DetailRow label="Username" value={user.username} />
          <DetailRow label="Email" value={user.email} />
          <DetailRow label="Role" value={user.role} />
          <DetailRow label="Status" value={user.enabled ? "Active" : "Disabled"} />
          <DetailRow label="Created" value={formatDate(user.created_at)} />
          <DetailRow label="Updated" value={formatDate(user.updated_at)} />
        </div>
      </div>

      <div className="surface-panel overflow-hidden rounded-2xl border-0">
        <div className="border-border border-b px-4 py-3">
          <h3 className="text-sm font-medium">Permissions & Limits</h3>
        </div>
        <div className="divide-border divide-y">
          <DetailRow label="Group" value={groupName} />
          <DetailRow label="Library Access" value={libraryNames} />
          <DetailRow
            label="Marker Editing"
            value={
              hasAssignedPermission(user.permissions, PERMISSION_MARKER_EDIT)
                ? "Allowed"
                : "Not allowed"
            }
          />
          <DetailRow
            label="Metadata Curation"
            value={
              hasAssignedPermission(user.permissions, PERMISSION_METADATA_CURATION)
                ? "Allowed"
                : "Not allowed"
            }
          />
          <DetailRow
            label="Max Playback Quality"
            value={formatPlaybackQualityPreset(user.max_playback_quality)}
          />
          <DetailRow
            label="Max Streams"
            value={user.max_streams === 0 ? "Unlimited" : String(user.max_streams)}
          />
          <DetailRow
            label="Max Transcodes"
            value={user.max_transcodes === 0 ? "Unlimited" : String(user.max_transcodes)}
          />
          <DetailRow label="Max Profiles" value={String(user.max_profiles)} />
          <DetailRow label="Downloads" value={user.download_allowed ? "Allowed" : "Not allowed"} />
          <DetailRow
            label="Download Transcode"
            value={user.download_transcode_allowed ? "Allowed" : "Not allowed"}
          />
        </div>
      </div>
    </div>
  );
}

function DetailRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between px-4 py-2.5">
      <span className="text-muted-foreground text-sm">{label}</span>
      <span className="text-sm font-medium">{value}</span>
    </div>
  );
}

function ProfilesTab({ userId }: { userId: number }) {
  const { data: profiles, isLoading } = useAdminUserProfiles(userId);

  if (isLoading)
    return (
      <div className="text-muted-foreground py-8 text-center text-sm">Loading profiles...</div>
    );

  if (!profiles || profiles.length === 0)
    return (
      <div className="surface-panel text-muted-foreground rounded-2xl py-10 text-center text-sm">
        No profiles found for this user.
      </div>
    );

  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
      {profiles.map((profile: AdminUserProfile) => (
        <ProfileCard key={profile.id} profile={profile} />
      ))}
    </div>
  );
}

function ProfileCard({ profile }: { profile: AdminUserProfile }) {
  return (
    <div className="surface-panel flex items-center gap-3 rounded-xl border-0 px-4 py-3">
      <UserCircle className="text-muted-foreground h-8 w-8" />
      <div>
        <div className="text-sm font-medium">{profile.name}</div>
        <div className="text-muted-foreground text-xs">{profile.id}</div>
      </div>
    </div>
  );
}

function WatchHistoryTab({ userId }: { userId: number }) {
  const {
    data: rows = [],
    isLoading,
    error,
  } = useAdminPlaybackHistory({
    userId,
    limit: 50,
  });

  if (isLoading)
    return (
      <div className="text-muted-foreground py-8 text-center text-sm">Loading watch history...</div>
    );

  if (error)
    return (
      <div className="text-destructive py-8 text-center text-sm">Failed to load watch history.</div>
    );

  if (rows.length === 0)
    return (
      <div className="surface-panel text-muted-foreground rounded-2xl py-10 text-center text-sm">
        No playback history for this user.
      </div>
    );

  return (
    <div className="surface-panel overflow-x-auto rounded-2xl border-0">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Media</TableHead>
            <TableHead>Profile</TableHead>
            <TableHead>Method</TableHead>
            <TableHead>Watch Time</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>Ended</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((row) => {
            const title = row.media_title || row.media_item_id || `File #${row.media_file_id}`;
            return (
              <TableRow key={row.session_id}>
                <TableCell>
                  {row.media_item_id ? (
                    <Link
                      to={`/item/${encodeURIComponent(row.media_item_id)}`}
                      className="hover:text-primary block font-medium transition-colors hover:underline"
                    >
                      {title}
                    </Link>
                  ) : (
                    <div className="font-medium">{title}</div>
                  )}
                  <div className="text-muted-foreground text-xs">{row.media_type || "unknown"}</div>
                </TableCell>
                <TableCell>
                  <Link
                    to={`/admin/history?user_id=${userId}&profile_id=${encodeURIComponent(row.profile_id)}`}
                    className="hover:text-primary transition-colors hover:underline"
                  >
                    {row.profile_name || row.profile_id}
                  </Link>
                </TableCell>
                <TableCell>
                  <Badge variant="secondary">{row.play_method}</Badge>
                </TableCell>
                <TableCell>
                  <div>{formatDuration(row.watched_seconds)}</div>
                  <div className="text-muted-foreground text-xs">
                    of {formatDuration(row.duration_seconds)}
                  </div>
                </TableCell>
                <TableCell>
                  <Badge variant={row.completed ? "default" : "outline"}>
                    {row.completed ? "Completed" : "Partial"}
                  </Badge>
                </TableCell>
                <TableCell>
                  <div>{formatDateTime(row.ended_at)}</div>
                  <div className="text-muted-foreground text-xs">
                    started {formatRelative(row.started_at)}
                  </div>
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}

function IPHistoryTab({ userId }: { userId: number }) {
  const { data: ips = [], isLoading } = useUserIPs(userId);

  if (isLoading)
    return (
      <div className="text-muted-foreground py-8 text-center text-sm">Loading IP history...</div>
    );

  if (ips.length === 0)
    return (
      <div className="surface-panel text-muted-foreground rounded-2xl py-10 text-center text-sm">
        No IP history found for this user.
      </div>
    );

  return (
    <div className="surface-panel overflow-x-auto rounded-2xl border-0">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>IP Address</TableHead>
            <TableHead>First Seen</TableHead>
            <TableHead>Last Seen</TableHead>
            <TableHead className="text-right">Requests</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {ips.map((entry: UserIPEntry) => (
            <TableRow key={entry.client_ip}>
              <TableCell className="font-mono text-sm">{entry.client_ip}</TableCell>
              <TableCell>{formatDateTime(entry.first_seen)}</TableCell>
              <TableCell>{formatDateTime(entry.last_seen)}</TableCell>
              <TableCell className="text-right">{entry.request_count.toLocaleString()}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

function UserSettingsTab({ userId }: { userId: number }) {
  const { data: settings = [], isLoading } = useAdminUserSettings(userId);
  const updateSetting = useUpdateAdminUserSetting();
  const deleteSetting = useDeleteAdminUserSetting();

  if (isLoading) {
    return (
      <div className="text-muted-foreground py-8 text-center text-sm">Loading settings...</div>
    );
  }

  const renderSettingRow = (entry: AdminSettingEntry) => {
    const definition = getSettingDefinition(entry.key);
    const label = definition?.label ?? entry.key;
    const description = definition?.description ?? "Stored account preference.";

    return (
      <div
        key={entry.key}
        className="border-border/50 grid gap-3 border-t py-4 first:border-t-0 first:pt-0 md:grid-cols-[minmax(0,1fr)_auto]"
      >
        <div className="space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <div className="text-sm font-medium">{label}</div>
            <span className="rounded-full border border-white/10 bg-white/5 px-2 py-0.5 text-[11px] font-medium text-slate-200">
              Explicit
            </span>
          </div>
          <p className="text-muted-foreground text-[13px] leading-relaxed">{description}</p>
          <p className="text-muted-foreground text-xs">
            Current: {formatSettingValue(entry.key, entry.value)}
          </p>
        </div>
        <div className="flex flex-col items-stretch gap-2 md:items-end">
          {definition ? (
            <RegistrySettingControl
              definition={definition}
              value={entry.value}
              disabled={updateSetting.isPending || deleteSetting.isPending}
              onChange={(value) =>
                updateSetting.mutate({
                  userId,
                  key: entry.key,
                  value,
                })
              }
            />
          ) : (
            <Input
              key={`${entry.key}:${entry.value}`}
              defaultValue={entry.value}
              disabled={updateSetting.isPending || deleteSetting.isPending}
              className="w-full min-w-[180px] sm:w-[220px]"
              onBlur={(event) => {
                if (event.currentTarget.value === entry.value) return;
                updateSetting.mutate({
                  userId,
                  key: entry.key,
                  value: event.currentTarget.value,
                });
              }}
            />
          )}
          <Button
            variant="ghost"
            size="sm"
            className="h-7 rounded-full px-2 text-xs"
            onClick={() => deleteSetting.mutate({ userId, key: entry.key })}
            disabled={updateSetting.isPending || deleteSetting.isPending}
          >
            <RotateCcw className="mr-1 h-3 w-3" />
            Reset
          </Button>
        </div>
      </div>
    );
  };

  return (
    <div className="space-y-4">
      <div className="surface-panel overflow-hidden rounded-[1.8rem] border-0">
        <div className="border-border/60 flex items-center gap-3 border-b px-5 py-4">
          <div className="rounded-full border border-emerald-500/25 bg-emerald-500/10 p-2">
            <Settings2 className="h-4 w-4 text-emerald-100" />
          </div>
          <div>
            <h3 className="text-sm font-semibold">User Playback Settings</h3>
            <p className="text-muted-foreground text-sm">
              Account-wide playback preferences that follow this user across devices.
            </p>
          </div>
        </div>
        <div className="space-y-0 px-5 py-4">
          {settings.length > 0 ? (
            settings.map(renderSettingRow)
          ) : (
            <div className="text-muted-foreground py-6 text-center text-sm">
              No account-wide settings are stored for this user.
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function DeviceOverridesTab({ userId }: { userId: number }) {
  const { data: settings = [], isLoading } = useAdminUserDeviceSettings(userId);
  const updateSetting = useUpdateAdminUserDeviceSetting();
  const deleteSetting = useDeleteAdminUserDeviceSetting();
  const deleteDevice = useDeleteAllAdminUserDeviceSettingsForDevice();
  const [deviceToReset, setDeviceToReset] = useState<{
    deviceId: string;
    profileId: string;
    profileName?: string;
  } | null>(null);
  const [settingToReset, setSettingToReset] = useState<AdminDeviceSetting | null>(null);
  const [jsonEditor, setJsonEditor] = useState<AdminDeviceSetting | null>(null);
  const [jsonValue, setJsonValue] = useState("");

  const deviceEntries = useMemo(() => {
    const grouped = new Map<string, Map<string, AdminDeviceSetting[]>>();
    for (const entry of settings) {
      const profileKey = entry.profile_id || UNKNOWN_PROFILE_ID;
      let deviceGroup = grouped.get(entry.device_id);
      if (!deviceGroup) {
        deviceGroup = new Map();
        grouped.set(entry.device_id, deviceGroup);
      }
      const list = deviceGroup.get(profileKey);
      if (list) {
        list.push(entry);
      } else {
        deviceGroup.set(profileKey, [entry]);
      }
    }
    return Array.from(grouped, ([deviceId, profileMap]) => ({
      deviceId,
      profiles: Array.from(profileMap, ([profileId, entries]) => ({ profileId, entries })),
    }));
  }, [settings]);

  if (isLoading) {
    return (
      <div className="text-muted-foreground py-8 text-center text-sm">
        Loading device overrides...
      </div>
    );
  }

  const totalOverrides = settings.length;
  const totalProfiles = new Set(settings.map((s) => s.profile_id || UNKNOWN_PROFILE_ID)).size;

  return (
    <div className="space-y-4">
      <ConfirmDialog
        open={deviceToReset !== null}
        onOpenChange={(open) => {
          if (!open) setDeviceToReset(null);
        }}
        title={
          deviceToReset?.profileName
            ? `Reset overrides for ${deviceToReset.profileName}?`
            : "Reset profile overrides"
        }
        description="Every override for this profile on this device will be cleared. Playback falls back to account or default values."
        confirmLabel="Reset all"
        variant="destructive"
        onConfirm={() => {
          if (deviceToReset) {
            deleteDevice.mutate({
              userId,
              profileId: deviceToReset.profileId,
              deviceId: deviceToReset.deviceId,
            });
          }
          setDeviceToReset(null);
        }}
      />
      <ConfirmDialog
        open={settingToReset !== null}
        onOpenChange={(open) => {
          if (!open) setSettingToReset(null);
        }}
        title="Reset this override?"
        description="The override will be removed and the device will fall back to the profile default."
        confirmLabel="Reset override"
        variant="destructive"
        onConfirm={() => {
          if (settingToReset) {
            deleteSetting.mutate({
              userId,
              profileId: settingToReset.profile_id,
              deviceId: settingToReset.device_id,
              key: settingToReset.key,
            });
          }
          setSettingToReset(null);
        }}
      />
      <Dialog
        open={jsonEditor !== null}
        onOpenChange={(open) => {
          if (!open) {
            setJsonEditor(null);
            setJsonValue("");
          }
        }}
      >
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle className="font-mono text-sm">{jsonEditor?.key ?? "JSON"}</DialogTitle>
          </DialogHeader>
          <div className="space-y-3">
            <p className="text-muted-foreground text-[12.5px]">
              Edit the raw value. Invalid JSON is saved as-is and may cause clients to fall back to
              defaults.
            </p>
            <textarea
              spellCheck={false}
              className="border-border bg-background focus:border-foreground/40 min-h-[260px] w-full rounded-md border px-3 py-2 font-mono text-[13px] leading-relaxed transition-colors outline-none"
              value={jsonValue}
              onChange={(event) => setJsonValue(event.target.value)}
            />
            <div className="flex justify-end gap-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() => {
                  setJsonEditor(null);
                  setJsonValue("");
                }}
              >
                Cancel
              </Button>
              <Button
                size="sm"
                onClick={() => {
                  if (!jsonEditor) return;
                  updateSetting.mutate(
                    {
                      userId,
                      profileId: jsonEditor.profile_id,
                      deviceId: jsonEditor.device_id,
                      key: jsonEditor.key,
                      value: jsonValue,
                    },
                    {
                      onSuccess: () => {
                        setJsonEditor(null);
                        setJsonValue("");
                      },
                    },
                  );
                }}
              >
                Save override
              </Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>

      {deviceEntries.length > 0 && (
        <div className="text-muted-foreground flex flex-wrap items-center gap-x-2 gap-y-1 text-[13px] tabular-nums">
          <span>
            <span className="text-foreground font-medium">{deviceEntries.length}</span>{" "}
            {deviceEntries.length === 1 ? "device" : "devices"}
          </span>
          <span className="text-muted-foreground/40">·</span>
          <span>
            <span className="text-foreground font-medium">{totalOverrides}</span>{" "}
            {totalOverrides === 1 ? "override" : "overrides"}
          </span>
          <span className="text-muted-foreground/40">·</span>
          <span>
            <span className="text-foreground font-medium">{totalProfiles}</span>{" "}
            {totalProfiles === 1 ? "profile" : "profiles"}
          </span>
        </div>
      )}

      {deviceEntries.length === 0 ? (
        <div className="surface-panel rounded-xl border-0 px-6 py-12 text-center">
          <p className="text-foreground text-sm font-medium">No device overrides</p>
          <p className="text-muted-foreground mx-auto mt-1 max-w-sm text-[12.5px] leading-relaxed">
            Overrides appear here as soon as this user tunes a per-device playback setting.
          </p>
        </div>
      ) : (
        <div className="space-y-3">
          {deviceEntries.map(({ deviceId, profiles }) => {
            const allEntries = profiles.flatMap((p) => p.entries);
            const first = allEntries[0];
            if (!first) return null;
            const lastUpdated = allEntries
              .map((entry) => entry.updated_at)
              .sort((a, b) => b.localeCompare(a))[0];
            const kind = classifyPlatform(first.device_platform);
            const profileCount = profiles.length;
            const overrideCount = allEntries.length;

            return (
              <section key={deviceId} className="surface-panel overflow-hidden rounded-xl border-0">
                <header className="border-border/60 flex flex-wrap items-start justify-between gap-3 border-b px-4 py-3 sm:px-5">
                  <div className="flex min-w-0 items-start gap-3">
                    <PlatformTile kind={kind} />
                    <div className="min-w-0 space-y-1">
                      <h3 className="text-foreground text-[14px] leading-tight font-semibold">
                        {first.device_name || "Unnamed device"}
                      </h3>
                      <div className="text-muted-foreground flex flex-wrap items-center gap-x-2 gap-y-1 text-[11.5px]">
                        <span className="text-foreground/80 font-mono">
                          {shortenId(deviceId, 8)}
                        </span>
                        <span className="text-muted-foreground/40">·</span>
                        <span>{platformLabel(first.device_platform)}</span>
                        {lastUpdated && (
                          <>
                            <span className="text-muted-foreground/40">·</span>
                            <span>updated {formatRelative(lastUpdated)}</span>
                          </>
                        )}
                      </div>
                      <div className="flex flex-wrap items-center gap-1.5 pt-1">
                        <Badge
                          variant="outline"
                          className="border-border/60 bg-background/60 rounded-full px-2 py-0.5 text-[10.5px] font-normal tabular-nums"
                        >
                          <span className="text-foreground font-medium">{profileCount}</span>
                          <span className="text-muted-foreground">
                            {profileCount === 1 ? "profile" : "profiles"}
                          </span>
                        </Badge>
                        <Badge
                          variant="outline"
                          className="border-border/60 bg-background/60 rounded-full px-2 py-0.5 text-[10.5px] font-normal tabular-nums"
                        >
                          <span className="text-foreground font-medium">{overrideCount}</span>
                          <span className="text-muted-foreground">
                            {overrideCount === 1 ? "override" : "overrides"}
                          </span>
                        </Badge>
                      </div>
                    </div>
                  </div>
                  <Button variant="outline" size="sm" asChild>
                    <Link to={`/admin/devices/${userId}/${encodeURIComponent(deviceId)}`}>
                      Open device
                      <ArrowUpRight className="h-3 w-3" />
                    </Link>
                  </Button>
                </header>

                <DeviceProfileTabs
                  profiles={profiles.map(
                    ({ profileId, entries }): DeviceProfileTabEntry => ({
                      profileId,
                      profileName: entries[0]?.profile_name || profileId,
                      settings: entries,
                    }),
                  )}
                  onResetProfile={(profileId, profileName) =>
                    setDeviceToReset({ deviceId, profileId, profileName })
                  }
                  onEditJson={(setting) => {
                    setJsonEditor(setting);
                    setJsonValue(setting.value);
                  }}
                  onResetSetting={(setting) => setSettingToReset(setting)}
                  onChangeSetting={(setting, value) =>
                    updateSetting.mutate({
                      userId,
                      profileId: setting.profile_id,
                      deviceId: setting.device_id,
                      key: setting.key,
                      value,
                    })
                  }
                  updatePending={updateSetting.isPending}
                  resetPending={deleteDevice.isPending}
                />
              </section>
            );
          })}
        </div>
      )}
    </div>
  );
}

function EditUserForm({ user, onClose }: { user: AdminUser; onClose: () => void }) {
  const { data: libraries = [] } = useAdminLibraries();
  const { data: accessGroups = [] } = useAccessGroups();
  const [username, setUsername] = useState(user.username);
  const [email, setEmail] = useState(user.email);
  const [password, setPassword] = useState("");
  const [role, setRole] = useState(user.role);
  const [enabled, setEnabled] = useState(user.enabled);
  const [permissions, setPermissions] = useState<string[]>(user.permissions ?? []);
  const [libraryIDs, setLibraryIDs] = useState<number[] | null>(user.library_ids);
  const [accessGroupID, setAccessGroupID] = useState<number | null>(user.access_group_id);
  const [maxStreams, setMaxStreams] = useState(user.max_streams);
  const [maxTranscodes, setMaxTranscodes] = useState(user.max_transcodes);
  const [maxProfiles, setMaxProfiles] = useState(user.max_profiles);
  const [maxPlaybackQualityPreset, setMaxPlaybackQualityPreset] = useState<PlaybackQualityPreset>(
    playbackQualityPresetFromValue(user.max_playback_quality),
  );
  const [downloadAllowed, setDownloadAllowed] = useState(user.download_allowed);
  const [downloadTranscodeAllowed, setDownloadTranscodeAllowed] = useState(
    user.download_transcode_allowed,
  );
  const accessGroupSelectId = useId();
  const markerEditId = useId();
  const metadataCurationId = useId();
  const updateMutation = useUpdateUser();
  const accessGroupValue = accessGroupID === null ? "none" : String(accessGroupID);
  const selectedGroupMissing =
    accessGroupID !== null && !accessGroups.some((group) => group.id === accessGroupID);

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const body: UpdateUserRequest = {
      username,
      email,
      role,
      permissions,
      enabled,
      library_ids: libraryIDs,
      access_group_id: accessGroupID,
      max_streams: maxStreams,
      max_transcodes: maxTranscodes,
      max_profiles: maxProfiles,
      max_playback_quality: playbackQualityValueFromPreset(maxPlaybackQualityPreset),
      download_allowed: downloadAllowed,
      download_transcode_allowed: downloadTranscodeAllowed,
    };
    if (password) body.password = password;
    updateMutation.mutate({ id: user.id, body }, { onSuccess: onClose });
  }

  return (
    <form onSubmit={handleSubmit} className="flex max-h-[70vh] flex-col">
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
                <Label>Username</Label>
                <Input value={username} onChange={(e) => setUsername(e.target.value)} required />
              </div>
              <div className="space-y-2">
                <Label>Email</Label>
                <Input
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  required
                />
              </div>
              <div className="space-y-2">
                <Label>Password (leave blank to keep current)</Label>
                <Input
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </div>
              <div className="space-y-2">
                <Label>Role</Label>
                <Select value={role} onValueChange={setRole}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="user">User</SelectItem>
                    <SelectItem value="admin">Admin</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
            <div className="border-border flex items-center justify-between rounded-md border px-3 py-2">
              <div>
                <div className="text-sm font-medium">Account status</div>
                <div className="text-muted-foreground text-xs">
                  Disable access without deleting the user.
                </div>
              </div>
              <div className="flex items-center gap-2">
                <Label className="text-xs">Enabled</Label>
                <Switch checked={enabled} onCheckedChange={setEnabled} />
              </div>
            </div>
          </TabsContent>

          <TabsContent value="access" className="mt-0 space-y-4">
            <div className="space-y-2">
              <Label htmlFor={accessGroupSelectId}>Group</Label>
              <Select
                value={accessGroupValue}
                onValueChange={(value) => {
                  setAccessGroupID(value === "none" ? null : Number(value));
                }}
              >
                <SelectTrigger id={accessGroupSelectId} className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">No group</SelectItem>
                  {selectedGroupMissing && (
                    <SelectItem value={String(accessGroupID)}>#{accessGroupID}</SelectItem>
                  )}
                  {accessGroups.map((group) => (
                    <SelectItem key={group.id} value={String(group.id)}>
                      {group.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <LibraryAccessSelector
              libraries={libraries}
              value={libraryIDs}
              onChange={setLibraryIDs}
            />
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
            <div className="grid gap-2 sm:grid-cols-2">
              <div className="border-border flex items-center justify-between rounded-md border px-3 py-2">
                <Label>Downloads Allowed</Label>
                <Switch checked={downloadAllowed} onCheckedChange={setDownloadAllowed} />
              </div>
              <div className="border-border flex items-center justify-between rounded-md border px-3 py-2">
                <Label>Download Transcode Allowed</Label>
                <Switch
                  checked={downloadTranscodeAllowed}
                  onCheckedChange={setDownloadTranscodeAllowed}
                />
              </div>
            </div>
          </TabsContent>

          <TabsContent value="limits" className="mt-0 space-y-4">
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-1">
                <Label>Max Streams</Label>
                <Input
                  type="number"
                  min={0}
                  value={maxStreams}
                  onChange={(e) => setMaxStreams(Number(e.target.value))}
                />
                <p className="text-muted-foreground text-xs">0 = unlimited</p>
              </div>
              <div className="space-y-1">
                <Label>Max Transcodes</Label>
                <Input
                  type="number"
                  min={0}
                  value={maxTranscodes}
                  onChange={(e) => setMaxTranscodes(Number(e.target.value))}
                />
                <p className="text-muted-foreground text-xs">0 = unlimited</p>
              </div>
              <div className="space-y-1">
                <Label>Max Profiles</Label>
                <Input
                  type="number"
                  min={1}
                  value={maxProfiles}
                  onChange={(e) => setMaxProfiles(Number(e.target.value))}
                />
              </div>
              <div className="space-y-1 sm:col-span-2">
                <Label>Max Playback Quality</Label>
                <Select
                  value={maxPlaybackQualityPreset}
                  onValueChange={(value) =>
                    setMaxPlaybackQualityPreset(value as PlaybackQualityPreset)
                  }
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {PLAYBACK_QUALITY_OPTIONS.map((option) => (
                      <SelectItem key={option.value} value={option.value}>
                        {option.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <p className="text-muted-foreground text-xs">
                  {
                    PLAYBACK_QUALITY_OPTIONS.find(
                      (option) => option.value === maxPlaybackQualityPreset,
                    )?.description
                  }
                </p>
              </div>
            </div>
          </TabsContent>
        </div>
      </Tabs>

      <div className="border-border mt-4 border-t pt-4">
        <Button type="submit" className="w-full" disabled={updateMutation.isPending}>
          {updateMutation.isPending ? "Saving..." : "Save"}
        </Button>
      </div>
    </form>
  );
}

function formatDuration(seconds: number | null) {
  if (!seconds || Number.isNaN(seconds)) return "0m";
  const rounded = Math.max(0, Math.floor(seconds));
  const hours = Math.floor(rounded / 3600);
  const minutes = Math.floor((rounded % 3600) / 60);
  const secs = rounded % 60;
  if (hours > 0) return `${hours}h ${minutes}m`;
  if (minutes > 0) return `${minutes}m ${secs}s`;
  return `${secs}s`;
}

function formatDate(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return formatPreferredDate(date, "medium");
}

function formatDateTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return formatDateTimePreferred(date);
}
