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
import { useT, type MessageKey } from "@/i18n";

import { StatStrip, StatTile, SubTabs } from "../ui";
import { DeviceDownloadsCard } from "./DeviceDownloadsCard";
import { MonitoredSeriesCard } from "./MonitoredSeriesCard";
import { IN_PROGRESS_STATUSES, isAndroidPlatform } from "./downloadPresentation";

const ALL = "all";
/** Keeps the space between the error and its Retry button. */
const SPACE = " ";

type StatusFilter = "all" | "on_device" | "in_progress" | "requested" | "failed" | "revoked";

const STATUS_OPTIONS: { value: StatusFilter; label: MessageKey }[] = [
  { value: "all", label: "web.admin.users.detail.downloads.filterAll" },
  { value: "on_device", label: "web.admin.users.detail.downloads.status.onDevice" },
  { value: "in_progress", label: "web.admin.users.detail.downloads.inProgress" },
  { value: "requested", label: "web.admin.users.detail.downloads.requestedOnAndroid" },
  { value: "failed", label: "web.admin.users.detail.downloads.status.failed" },
  { value: "revoked", label: "web.admin.users.detail.downloads.status.revoked" },
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

/** The note for a capped list, or null when both lists loaded in full. */
function partialListNote(downloads?: boolean, monitors?: boolean): MessageKey | null {
  if (downloads && monitors) return "web.admin.users.detail.downloads.partial.both";
  if (downloads) return "web.admin.users.detail.downloads.partial.downloads";
  if (monitors) return "web.admin.users.detail.downloads.partial.monitors";
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
  const t = useT();
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
  const partial = partialListNote(downloads.data?.truncated, monitors.data?.truncated);
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
    return device
      ? device.device_name || t("web.admin.users.detail.common.unnamedDevice")
      : t("web.admin.users.detail.common.unknownDevice");
  };
  const android = (id: string) => isAndroidPlatform(deviceById.get(id)?.device_platform);

  // Devices in the order their newest row arrived (the list is newest first).
  const deviceIds = useMemo(() => [...new Set(rows.map((row) => row.device_id))], [rows]);

  if (capabilities.isLoading) {
    return <Message>{t("web.admin.users.detail.downloads.loading")}</Message>;
  }
  if (!available) return <Message>{t("web.admin.users.detail.downloads.unavailable")}</Message>;
  if (downloads.isError || monitors.isError)
    return (
      <Message role="alert">
        <span className="text-destructive">{t("web.admin.users.detail.downloads.failed")}</span>
        {SPACE}
        <Button
          variant="outline"
          size="sm"
          className="ms-2"
          onClick={() => {
            void downloads.refetch();
            void monitors.refetch();
          }}
        >
          {t("common.retry")}
        </Button>
      </Message>
    );
  if (downloads.isLoading || monitors.isLoading) {
    return <Message>{t("web.admin.users.detail.downloads.loading")}</Message>;
  }

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
        <StatTile
          label={t("web.admin.users.detail.downloads.status.onDevice")}
          value={onDevice}
          detail={t("web.admin.users.detail.downloads.stats.onDeviceDetail")}
        />
        <StatTile
          label={t("web.admin.users.detail.downloads.inProgress")}
          value={inProgress}
          detail={t("web.admin.users.detail.downloads.stats.inProgressDetail")}
        />
        {anyAndroid && (
          <StatTile
            label={t("web.admin.users.detail.downloads.requestedOnAndroid")}
            value={requestedCount}
            detail={t("web.admin.users.detail.downloads.stats.requestedDetail")}
          />
        )}
        <StatTile
          label={t("web.admin.users.detail.downloads.monitoredSeries")}
          value={activeMonitors}
          detail={t("web.admin.users.detail.downloads.stats.monitoredDetail")}
        />
      </StatStrip>

      {partial && (
        <p
          role="note"
          className="border-border/60 bg-muted/30 rounded-lg border px-3 py-2 text-[13px] leading-relaxed"
        >
          {t(partial)}
        </p>
      )}

      {rows.length > 0 && (
        <div className="flex flex-wrap items-center justify-between gap-3">
          <SubTabs
            ariaLabel={t("web.admin.users.detail.downloads.devicesLabel")}
            value={selectedDevice}
            onValueChange={setDeviceFilter}
            items={[
              { value: ALL, label: t("web.admin.users.detail.downloads.allDevices") },
              ...deviceIds.map((id) => ({ value: id, label: deviceName(id) })),
            ]}
          />
          <div className="flex flex-wrap items-center gap-2">
            <Select value={profileFilter} onValueChange={setProfileFilter}>
              <SelectTrigger
                size="sm"
                className="w-40"
                aria-label={t("web.admin.users.detail.common.profileFilter")}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>
                  {t("web.admin.users.detail.common.allProfiles")}
                </SelectItem>
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
              <SelectTrigger
                size="sm"
                className="w-40"
                aria-label={t("web.admin.users.detail.downloads.statusFilter")}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {STATUS_OPTIONS.filter((option) => option.value !== "requested" || anyAndroid).map(
                  (option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {t(option.label)}
                    </SelectItem>
                  ),
                )}
              </SelectContent>
            </Select>
          </div>
        </div>
      )}

      {rows.length === 0 ? (
        <Message>{t("web.admin.users.detail.downloads.empty")}</Message>
      ) : cards.length === 0 ? (
        <Message>{t("web.admin.users.detail.downloads.noMatch")}</Message>
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
        {t("web.admin.users.detail.downloads.wipedNote")}
      </p>
    </div>
  );
}
