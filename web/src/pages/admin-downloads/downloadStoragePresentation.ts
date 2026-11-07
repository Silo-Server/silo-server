import type {
  AdminDownloadStorage,
  AdminDownloadStorageEvent,
  AdminDownloadStorageFile,
  AdminDownloadStorageLocation,
} from "@/api/v2/adminDownloadStorage";
import { t, type MessageKey } from "@/i18n";
import { formatDecimal } from "@/lib/numberFormat";

/**
 * Values for a plural message: `count` selects the form, `count_text` prints
 * the number as given (digits by default, so 1234 is not grouped).
 */
export function countValues(count: number, text = String(count)) {
  return { count, count_text: text };
}

/**
 * Bytes in decimal units, the way drives are sold and the way the budget
 * settings take them, so a 500 GB budget reads "500 GB" here too. Zero reads
 * "0 B", not blank.
 */
export function formatStorageBytes(bytes: number | null | undefined): string {
  const value = Math.max(bytes ?? 0, 0);
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let index = 0;
  let scaled = value;
  // 999.5 and up would round to "1000", so it moves to the next unit.
  while (scaled >= 999.5 && index < units.length - 1) {
    scaled /= 1000;
    index++;
  }
  if (index === 0) return `${Math.round(scaled)} B`;
  return `${scaled >= 100 ? Math.round(scaled) : formatDecimal(scaled, scaled >= 10 ? 0 : 1)} ${units[index]}`;
}

/** Fill at which a node stops taking playback sessions (scratch pressure). */
export const SCRATCH_ADMISSION_PERCENT = 95;

export type MeterSegmentKind = "in_use" | "cached" | "untracked" | "other" | "free";

export interface MeterSegment {
  kind: MeterSegmentKind;
  label: string;
  bytes: number;
  /** Share of the filesystem, 0..1. */
  fraction: number;
}

const SEGMENT_LABELS: Record<MeterSegmentKind, MessageKey> = {
  in_use: "web.admin.downloads.presentation.segment.inUse",
  cached: "web.admin.downloads.presentation.segment.cached",
  untracked: "web.admin.downloads.presentation.segment.untracked",
  other: "web.admin.downloads.presentation.segment.otherData",
  free: "web.admin.downloads.presentation.segment.free",
};

/**
 * Splits a location's filesystem into prepared files (in use, cached,
 * untracked), everything else on the disk, and free space. Prepared bytes come
 * from Silo's records, scaled down to what was measured on disk when the
 * records claim more than the files hold (a file already deleted, say), so the
 * bar never shows more prepared bytes than exist.
 */
/** Bytes a location's directory holds: finished, partial, and other files. */
function directoryBytes(usage: NonNullable<AdminDownloadStorageLocation["usage"]>): number {
  return usage.bytes + usage.partial_bytes + usage.other_bytes;
}

/** Bytes Silo's records say the location's prepared files hold. */
export function preparedBytes(location: AdminDownloadStorageLocation): number {
  return location.in_use_bytes + location.cached_bytes;
}

export function meterSegments(location: AdminDownloadStorageLocation): MeterSegment[] | null {
  const usage = location.usage;
  if (!usage || usage.fs_total_bytes <= 0) return null;
  const total = usage.fs_total_bytes;
  const onDisk = Math.max(directoryBytes(usage), 0);
  let inUse = Math.max(location.in_use_bytes, 0);
  let cached = Math.max(location.cached_bytes, 0);
  const untracked = Math.min(Math.max(location.untracked_bytes, 0), onDisk);
  const tracked = inUse + cached;
  const room = Math.max(onDisk - untracked, 0);
  if (tracked > room && tracked > 0) {
    inUse = Math.round((inUse / tracked) * room);
    cached = room - inUse;
  }
  const used = Math.min(Math.max(usage.fs_used_bytes, 0), total);
  const other = Math.max(used - inUse - cached - untracked, 0);
  const free = Math.max(total - used, 0);
  const parts: [MeterSegmentKind, number][] = [
    ["in_use", inUse],
    ["cached", cached],
    ["untracked", untracked],
    ["other", other],
    ["free", free],
  ];
  return parts.map(([kind, bytes]) => ({
    kind,
    label: t(SEGMENT_LABELS[kind]),
    bytes,
    fraction: bytes / total,
  }));
}

/** How full the location's filesystem is, 0..100, or null when unmeasured. */
export function diskFillPercent(location: AdminDownloadStorageLocation): number | null {
  const usage = location.usage;
  if (!usage || usage.fs_total_bytes <= 0) return null;
  return (usage.fs_used_bytes / usage.fs_total_bytes) * 100;
}

export type StorageWarningTone = "warning" | "info";

export interface StorageWarning {
  id: string;
  tone: StorageWarningTone;
  title: string;
  body: string;
  /** Location the warning is about; undefined for server-wide warnings. */
  location?: string;
  action?: "edit_location" | "review_untracked" | "review_devices" | "view_location";
}

/** Whole percent, the way df prints it. */
export function formatPercent(value: number): string {
  return `${Math.round(value)}%`;
}

/**
 * Things an administrator should act on, most urgent first: a disk at or near
 * its ceiling (especially one that also carries transcode scratch), prepared
 * files on storage that does not survive a restart, a node that is offline
 * with files on it, untracked files, and bytes kept only for stale devices.
 */
export function storageWarnings(storage: AdminDownloadStorage): StorageWarning[] {
  const warnings: StorageWarning[] = [];
  const ceiling = storage.disk_ceiling_percent;
  for (const location of storage.locations) {
    const fill = diskFillPercent(location);
    const usage = location.usage;
    if (fill !== null && fill >= ceiling - 5) {
      warnings.push({
        id: `fill:${location.key}`,
        tone: "warning",
        title: t("web.admin.downloads.presentation.warning.fill.title", {
          name: location.name,
          percent: formatPercent(fill),
        }),
        body: usage?.shares_scratch
          ? t("web.admin.downloads.presentation.warning.fill.bodyScratch", {
              ceiling: String(ceiling),
              scratch: String(SCRATCH_ADMISSION_PERCENT),
            })
          : t("web.admin.downloads.presentation.warning.fill.body", { ceiling: String(ceiling) }),
        location: location.key,
        action: "edit_location",
      });
    }
    if (usage?.ephemeral) {
      warnings.push({
        id: `ephemeral:${location.key}`,
        tone: "warning",
        title: t("web.admin.downloads.presentation.warning.ephemeral.title", {
          name: location.name,
        }),
        body: usage.fs_type
          ? t("web.admin.downloads.presentation.warning.ephemeral.bodyWithType", {
              fs_type: usage.fs_type,
            })
          : t("web.admin.downloads.presentation.warning.ephemeral.body"),
        location: location.key,
        action: "edit_location",
      });
    }
    if (location.kind === "node" && !location.online) {
      const held = preparedBytes(location);
      if (held > 0) {
        warnings.push({
          id: `offline:${location.key}`,
          tone: "info",
          title: t("web.admin.downloads.presentation.warning.offline.title", {
            name: location.name,
          }),
          body: t("web.admin.downloads.presentation.warning.offline.body", {
            ...countValues(location.waiting_downloads),
            size: formatStorageBytes(held),
          }),
          location: location.key,
          action: "view_location",
        });
      }
    }
    if (location.untracked_bytes > 0) {
      warnings.push({
        id: `untracked:${location.key}`,
        tone: "info",
        title: t("web.admin.downloads.presentation.warning.untracked.title", {
          size: formatStorageBytes(location.untracked_bytes),
          name: location.name,
        }),
        body: t("web.admin.downloads.presentation.warning.untracked.body"),
        location: location.key,
        action: "review_untracked",
      });
    }
    if (location.replicas_disagree) {
      warnings.push({
        id: `replicas:${location.key}`,
        tone: "warning",
        title: t("web.admin.downloads.presentation.warning.replicas.title"),
        body: t("web.admin.downloads.presentation.warning.replicas.body"),
        location: location.key,
        action: "edit_location",
      });
    }
  }
  const stale = storage.locations.reduce((sum, l) => sum + l.stale_waiting_bytes, 0);
  if (stale > 0) {
    warnings.push({
      id: "stale",
      tone: "info",
      title: t("web.admin.downloads.presentation.warning.stale.title", {
        size: formatStorageBytes(stale),
        days: storage.stale_device_days,
        days_text: String(storage.stale_device_days),
      }),
      body: t("web.admin.downloads.presentation.warning.stale.body"),
      action: "review_devices",
    });
  }
  return warnings;
}

export interface StorageTotals {
  onDisk: number;
  inUse: number;
  cached: number;
  untracked: number;
  files: number;
}

export function storageTotals(storage: AdminDownloadStorage): StorageTotals {
  return storage.locations.reduce<StorageTotals>(
    (sum, l) => ({
      onDisk: sum.onDisk + (l.usage ? directoryBytes(l.usage) : 0),
      inUse: sum.inUse + l.in_use_bytes,
      cached: sum.cached + l.cached_bytes,
      untracked: sum.untracked + l.untracked_bytes,
      files: sum.files + (l.usage?.files ?? l.in_use_files + l.cached_files),
    }),
    { onDisk: 0, inUse: 0, cached: 0, untracked: 0, files: 0 },
  );
}

/** "/srv/x", or what the node uses when no directory is configured or known yet. */
export function locationDirectoryLabel(location: AdminDownloadStorageLocation): string {
  if (location.dir) return location.dir;
  return t("web.admin.downloads.presentation.directoryDefault");
}

export function budgetLabel(location: AdminDownloadStorageLocation): string {
  if (location.budget_source === "none" || location.budget_bytes <= 0) {
    return t("web.admin.downloads.presentation.budget.none");
  }
  return t("web.admin.downloads.presentation.budget.amount", {
    size: formatStorageBytes(location.budget_bytes),
  });
}

export function budgetSourceLabel(location: AdminDownloadStorageLocation): string {
  switch (location.budget_source) {
    case "override":
      return t("web.admin.downloads.presentation.budget.sourceCustom");
    case "setting":
      return t("web.admin.downloads.presentation.budget.sourceDefault");
    default:
      return "";
  }
}

/** Records and disk agree when they are within 1 GB or 2% of each other. */
export function recordsDrift(location: AdminDownloadStorageLocation): number {
  const usage = location.usage;
  if (!usage) return 0;
  const indexed = preparedBytes(location);
  const drift = usage.bytes - indexed;
  const tolerance = Math.max(1e9, indexed * 0.02);
  return Math.abs(drift) > tolerance ? drift : 0;
}

/** Title line for a prepared file; the episode goes in the subtitle. */
export function storageFileTitle(file: AdminDownloadStorageFile): string {
  return file.title || t("web.admin.downloads.presentation.file.unknownTitle");
}

export function storageFileSubtitle(file: AdminDownloadStorageFile): string {
  if (file.season_number != null && file.episode_number != null) {
    const values = { season: String(file.season_number), episode: String(file.episode_number) };
    return file.episode_title
      ? t("web.admin.downloads.presentation.file.episodeWithTitle", {
          ...values,
          title: file.episode_title,
        })
      : t("web.admin.downloads.presentation.file.episode", values);
  }
  return file.year ? String(file.year) : "";
}

export function storageFileRecipe(file: AdminDownloadStorageFile): {
  main: string;
  detail: string;
} {
  if (file.format === "remux") {
    return {
      main: t("web.admin.downloads.presentation.recipe.remux"),
      detail: t("web.admin.downloads.presentation.recipe.originalQuality"),
    };
  }
  const codec = file.video_codec === "hevc" ? "HEVC" : file.video_codec.toUpperCase();
  const main = [file.resolution, codec].filter(Boolean).join(" ");
  const detail = file.bitrate_kbps
    ? t("web.admin.downloads.presentation.recipe.bitrate", {
        mbps: String(Math.round(file.bitrate_kbps / 1000)),
      })
    : "";
  return { main: main || t("web.admin.downloads.presentation.recipe.transcode"), detail };
}

/** Who a file serves, in a few words. */
export function storageFileUsage(file: AdminDownloadStorageFile): string {
  const parts: string[] = [];
  if (file.downloading > 0) {
    parts.push(
      t("web.admin.downloads.presentation.usage.downloading", countValues(file.downloading)),
    );
  }
  if (file.waiting > 0) {
    parts.push(t("web.admin.downloads.presentation.usage.waiting", countValues(file.waiting)));
  }
  if (file.finished > 0) {
    parts.push(t("web.admin.downloads.presentation.usage.finished", countValues(file.finished)));
  }
  return parts.join(" · ") || t("web.admin.downloads.presentation.usage.none");
}

/** "in 22 h", "in 3 d", or "now" for a cached file's expiry. */
export function formatExpiresIn(value: string | undefined, now = Date.now()): string {
  if (!value) return "";
  const ms = Date.parse(value) - now;
  if (Number.isNaN(ms)) return "";
  if (ms <= 60_000) return t("web.admin.downloads.presentation.expires.now");
  const hours = ms / 3_600_000;
  if (hours < 1) {
    return t(
      "web.admin.downloads.presentation.expires.minutes",
      countValues(Math.round(ms / 60_000)),
    );
  }
  if (hours < 48) {
    return t("web.admin.downloads.presentation.expires.hours", countValues(Math.round(hours)));
  }
  return t("web.admin.downloads.presentation.expires.days", countValues(Math.round(hours / 24)));
}

const REASON_LABELS: Record<string, MessageKey> = {
  cache_expired: "web.admin.downloads.presentation.reason.cacheExpired",
  budget: "web.admin.downloads.presentation.reason.budget",
  disk_ceiling: "web.admin.downloads.presentation.reason.diskCeiling",
  admin_delete: "web.admin.downloads.presentation.reason.adminDelete",
  untracked: "web.admin.downloads.presentation.reason.untracked",
  missing: "web.admin.downloads.presentation.reason.missing",
  revoked: "web.admin.downloads.presentation.reason.revoked",
  device_removed: "web.admin.downloads.presentation.reason.deviceRemoved",
};

/** A history reason's label; an unknown reason shows as the server sent it. */
export function storageEventReasonLabel(reason: string): string {
  const key = Object.hasOwn(REASON_LABELS, reason) ? REASON_LABELS[reason] : undefined;
  return key ? t(key) : reason;
}

export const STORAGE_EVENT_REASONS = Object.keys(REASON_LABELS);

/** The item column of a history row. */
export function storageEventItem(event: AdminDownloadStorageEvent): string {
  if (event.reason === "revoked" || event.reason === "device_removed") {
    const device = event.location_name || t("web.admin.downloads.presentation.event.unknownDevice");
    const owner = event.account?.username;
    return owner
      ? t("web.admin.downloads.presentation.event.downloadsWithOwner", {
          ...countValues(event.count),
          device,
          owner,
        })
      : t("web.admin.downloads.presentation.event.downloads", {
          ...countValues(event.count),
          device,
        });
  }
  const first = event.titles[0];
  if (event.count === 1 && first) return first;
  if (event.detail && event.reason === "untracked") return event.detail;
  return t("web.admin.downloads.presentation.event.files", countValues(event.count));
}

export function storageEventLocation(event: AdminDownloadStorageEvent): string {
  if (event.location === "device")
    return t("web.admin.downloads.presentation.event.locationDevice");
  if (event.location === "server")
    return t("web.admin.downloads.presentation.event.locationServer");
  return event.location_name || event.location;
}
