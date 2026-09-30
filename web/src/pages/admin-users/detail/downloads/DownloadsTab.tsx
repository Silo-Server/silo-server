import { useMemo, useState, type ReactNode } from "react";

import type { AdminUser } from "@/api/types";
import type { AdminUserDeviceRow, AdminUserDownload } from "@/api/v2/adminUserActivity";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  useAdminUserDevices,
  useAdminUserDownloadSubscriptions,
  useAdminUserDownloads,
  useAdminUserProfileActivity,
} from "@/hooks/queries/admin/userActivity";
import { useAdminUserCapabilities } from "@/hooks/queries/admin/users";

import { StatStrip, StatTile, SubTabs } from "../ui";
import { DeviceDownloadsCard } from "./DeviceDownloadsCard";
import { MonitoredSeriesCard } from "./MonitoredSeriesCard";
import { IN_PROGRESS_STATUSES, isAndroidPlatform } from "./downloadPresentation";

const ALL = "all";

type StatusFilter = "all" | "on_device" | "in_progress" | "requested" | "failed" | "revoked";

const STATUS_OPTIONS: { value: StatusFilter; label: string }[] = [
  { value: "all", label: "Everything" },
  { value: "on_device", label: "On device" },
  { value: "in_progress", label: "In progress" },
  { value: "requested", label: "Requested on Android" },
  { value: "failed", label: "Failed" },
  { value: "revoked", label: "Revoked" },
];

/**
 * Whether a row counts as "requested on Android": the Android app never
 * reports progress, so its ready and downloading rows are only requests.
 */
function requestedOnAndroid(row: AdminUserDownload, android: boolean): boolean {
  return android && (row.status === "ready" || row.status === "downloading");
}

/** The status filters count rows the same way as the stat tiles above them. */
function matchesStatus(row: AdminUserDownload, filter: StatusFilter, android: boolean): boolean {
  switch (filter) {
    case "all":
      return true;
    case "on_device":
      return row.status === "completed";
    case "in_progress":
      return !android && IN_PROGRESS_STATUSES.has(row.status);
    case "requested":
      return requestedOnAndroid(row, android);
    default:
      return row.status === filter;
  }
}

/** What a capped list covers, or null when both lists loaded in full. */
function partialListText(downloads?: boolean, monitors?: boolean): string | null {
  if (downloads && monitors) return "the first 5,000 downloads and 2,000 monitored series";
  if (downloads) return "the first 5,000 downloads";
  if (monitors) return "the first 2,000 monitored series";
  return null;
}

function Message({ children, role }: { children: ReactNode; role?: "alert" }) {
  return (
    <div
      role={role}
      className="surface-panel text-muted-foreground rounded-2xl border-0 px-5 py-8 text-center text-sm"
    >
      {children}
    </div>
  );
}

/** What each of the account's devices holds, and which series download on their own. */
export function DownloadsTab({ user }: { user: AdminUser }) {
  const capabilities = useAdminUserCapabilities();
  const available = capabilities.data?.account_downloads === true;
  const devicesAvailable = capabilities.data?.account_devices === true;
  const downloads = useAdminUserDownloads(user.id, available);
  const monitors = useAdminUserDownloadSubscriptions(user.id, available);
  const devices = useAdminUserDevices(user.id, available && devicesAvailable);
  const profiles = useAdminUserProfileActivity(user.id);

  const [deviceFilter, setDeviceFilter] = useState<string>(ALL);
  const [profileFilter, setProfileFilter] = useState<string>(ALL);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");

  const rows = useMemo(() => downloads.data?.items ?? [], [downloads.data]);
  const monitorRows = useMemo(() => monitors.data?.items ?? [], [monitors.data]);
  const partial = partialListText(downloads.data?.truncated, monitors.data?.truncated);
  const deviceById = useMemo(
    () => new Map<string, AdminUserDeviceRow>((devices.data ?? []).map((d) => [d.device_id, d])),
    [devices.data],
  );
  const profileNames = useMemo(
    () => new Map((profiles.data ?? []).map((p) => [p.id, p.name])),
    [profiles.data],
  );
  const deviceName = (id: string) => {
    const device = deviceById.get(id);
    return device ? device.device_name || "Unnamed device" : "Unknown device";
  };
  const android = (id: string) => isAndroidPlatform(deviceById.get(id)?.device_platform);

  // Devices in the order their newest row arrived (the list is newest first).
  const deviceIds = useMemo(() => [...new Set(rows.map((row) => row.device_id))], [rows]);

  if (capabilities.isLoading) return <Message>Loading downloads...</Message>;
  if (!available) return <Message>Downloads aren&apos;t available on this server.</Message>;
  if (downloads.isError || monitors.isError)
    return (
      <Message role="alert">
        <span className="text-destructive">Couldn&apos;t load this account&apos;s downloads.</span>{" "}
        <Button
          variant="outline"
          size="sm"
          className="ml-2"
          onClick={() => {
            void downloads.refetch();
            void monitors.refetch();
          }}
        >
          Retry
        </Button>
      </Message>
    );
  if (downloads.isLoading || monitors.isLoading) return <Message>Loading downloads...</Message>;

  const counted = (predicate: (row: AdminUserDownload) => boolean) => rows.filter(predicate).length;
  const onDevice = counted((row) => !android(row.device_id) && row.status === "completed");
  const inProgress = counted(
    (row) => !android(row.device_id) && IN_PROGRESS_STATUSES.has(row.status),
  );
  const anyAndroid = rows.some((row) => android(row.device_id));
  const requestedCount = counted((row) => requestedOnAndroid(row, android(row.device_id)));
  const activeMonitors = monitorRows.filter((monitor) => monitor.active).length;

  const selectedDevice = deviceIds.includes(deviceFilter) ? deviceFilter : ALL;
  const filtered = rows.filter(
    (row) =>
      (selectedDevice === ALL || row.device_id === selectedDevice) &&
      (profileFilter === ALL || row.profile_id === profileFilter) &&
      matchesStatus(row, statusFilter, android(row.device_id)),
  );
  const shownMonitors = monitorRows.filter(
    (monitor) =>
      (selectedDevice === ALL || monitor.device_id === selectedDevice) &&
      (profileFilter === ALL || monitor.profile_id === profileFilter),
  );
  const cards = deviceIds
    .map((id) => ({ id, rows: filtered.filter((row) => row.device_id === id) }))
    .filter((card) => card.rows.length > 0);

  return (
    <div className="space-y-4">
      <StatStrip columns={4}>
        <StatTile label="On device" value={onDevice} detail="confirmed by the app" />
        <StatTile
          label="In progress"
          value={inProgress}
          detail="downloading, preparing, or waiting"
        />
        {anyAndroid && (
          <StatTile
            label="Requested on Android"
            value={requestedCount}
            detail="not confirmed by the app"
          />
        )}
        <StatTile label="Monitored series" value={activeMonitors} detail="synced by the app" />
      </StatStrip>

      {partial && (
        <p
          role="note"
          className="border-border/60 bg-muted/30 rounded-lg border px-3 py-2 text-[13px] leading-relaxed"
        >
          The lists and counts here cover only {partial}; this account has more.
        </p>
      )}

      {rows.length > 0 && (
        <div className="flex flex-wrap items-center justify-between gap-3">
          <SubTabs
            ariaLabel="Devices"
            value={selectedDevice}
            onValueChange={setDeviceFilter}
            items={[
              { value: ALL, label: "All devices" },
              ...deviceIds.map((id) => ({ value: id, label: deviceName(id) })),
            ]}
          />
          <div className="flex flex-wrap items-center gap-2">
            <Select value={profileFilter} onValueChange={setProfileFilter}>
              <SelectTrigger size="sm" className="w-40" aria-label="Profile">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>All profiles</SelectItem>
                {(profiles.data ?? []).map((profile) => (
                  <SelectItem key={profile.id} value={profile.id}>
                    {profile.name || profile.id}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select
              value={statusFilter}
              onValueChange={(value) => setStatusFilter(value as StatusFilter)}
            >
              <SelectTrigger size="sm" className="w-40" aria-label="Status">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {STATUS_OPTIONS.filter((option) => option.value !== "requested" || anyAndroid).map(
                  (option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {option.label}
                    </SelectItem>
                  ),
                )}
              </SelectContent>
            </Select>
          </div>
        </div>
      )}

      {rows.length === 0 ? (
        <Message>No downloads on this account&apos;s devices.</Message>
      ) : cards.length === 0 ? (
        <Message>No downloads match these filters.</Message>
      ) : (
        cards.map((card) => (
          <DeviceDownloadsCard
            key={card.id}
            userId={user.id}
            deviceId={card.id}
            device={deviceById.get(card.id)}
            rows={card.rows}
            summaryRows={rows.filter((row) => row.device_id === card.id)}
            monitors={monitorRows}
            profileNames={profileNames}
          />
        ))
      )}

      <MonitoredSeriesCard
        monitors={shownMonitors}
        deviceName={deviceName}
        profileNames={profileNames}
        filtered={selectedDevice !== ALL || profileFilter !== ALL}
      />

      <p className="text-muted-foreground text-xs">
        A device that was wiped or had the app removed keeps its rows here until it&apos;s removed
        from the account.
      </p>
    </div>
  );
}
