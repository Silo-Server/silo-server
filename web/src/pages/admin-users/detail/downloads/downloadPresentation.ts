import type { AdminUserDownload, AdminUserDownloadSubscription } from "@/api/v2/adminUserActivity";
import { t, type MessageKey } from "@/i18n";
import { episodeCode } from "../format";

export type DownloadBadgeTone = "ok" | "accent" | "neutral" | "warn" | "danger";

export interface DownloadBadge {
  label: string;
  tone: DownloadBadgeTone;
}

export function isAndroidPlatform(platform: string | undefined): boolean {
  return /android/i.test(platform ?? "");
}

/**
 * The Android app registers and deletes managed downloads but never reports
 * progress or completion, and serving the file records no transfer. A `ready`
 * or `downloading` row on Android therefore only says the phone asked for it.
 */
function observedStatus(status: string, android: boolean): string {
  return android && status === "downloading" ? "ready" : status;
}

/** On Android a ready or downloading row reads "Requested" (see observedStatus). */
export function downloadStatusBadge(status: string, android: boolean): DownloadBadge {
  switch (observedStatus(status, android)) {
    case "completed":
      return { label: t("web.admin.users.detail.downloads.status.onDevice"), tone: "ok" };
    case "downloading":
      return { label: t("web.admin.users.detail.downloads.status.downloading"), tone: "accent" };
    case "ready":
      return {
        label: android
          ? t("web.admin.users.detail.downloads.status.requested")
          : t("web.admin.users.detail.downloads.status.waitingForDevice"),
        tone: "neutral",
      };
    case "preparing":
      return {
        label: t("web.admin.users.detail.downloads.status.serverPreparing"),
        tone: "neutral",
      };
    case "failed":
      return { label: t("web.admin.users.detail.downloads.status.failed"), tone: "danger" };
    case "revoked":
      return { label: t("web.admin.users.detail.downloads.status.revoked"), tone: "warn" };
    default:
      return { label: status, tone: "neutral" };
  }
}

/** Statuses that count as "in progress": the server or the device still has work. */
export const IN_PROGRESS_STATUSES: ReadonlySet<string> = new Set([
  "preparing",
  "ready",
  "downloading",
]);

function formatMbps(kbps: number): string {
  return (kbps / 1000).toFixed(1).replace(/\.0$/, "");
}

/** "Original" for the source file; "10 Mbps · server-prepared" for a server-made copy. */
export function downloadQualityLabel(d: AdminUserDownload): string {
  if (d.delivery_format !== "transcode")
    return t("web.admin.users.detail.downloads.quality.original");
  let kbps = d.target_bitrate_kbps;
  if (!(kbps > 0)) {
    const pattern = /^(\d+(?:\.\d+)?)mbps$/i;
    const preset = pattern.exec(d.effective_quality) ?? pattern.exec(d.quality);
    kbps = preset ? Number(preset[1]) * 1000 : 0;
  }
  return kbps > 0
    ? t("web.admin.users.detail.downloads.quality.serverPreparedBitrate", {
        mbps: formatMbps(kbps),
      })
    : t("web.admin.users.detail.downloads.quality.serverPrepared");
}

export interface DownloadGroup {
  key: string;
  contentId: string;
  title: string;
  /** Episode rows of a series, in season and episode order. Empty for a movie. */
  episodes: AdminUserDownload[];
  /** The row itself when the group is a single non-episode title. */
  movie?: AdminUserDownload;
  /** An active series monitor on the same device and profile covers this series. */
  monitored: boolean;
}

function episodeOrder(a: AdminUserDownload, b: AdminUserDownload): number {
  const sa = a.episode?.season_number ?? Number.MAX_SAFE_INTEGER;
  const sb = b.episode?.season_number ?? Number.MAX_SAFE_INTEGER;
  if (sa !== sb) return sa - sb;
  const ea = a.episode?.episode_number ?? Number.MAX_SAFE_INTEGER;
  const eb = b.episode?.episode_number ?? Number.MAX_SAFE_INTEGER;
  if (ea !== eb) return ea - eb;
  return a.id.localeCompare(b.id);
}

/**
 * Groups one device's rows: every episode of a series (per profile) folds
 * into one group, every other title stands alone. Groups keep the order in
 * which their newest row arrived (the list is newest first).
 */
export function groupDeviceDownloads(
  rows: AdminUserDownload[],
  monitors: AdminUserDownloadSubscription[],
): DownloadGroup[] {
  const groups = new Map<string, DownloadGroup>();
  for (const row of rows) {
    if (!row.episode_id) {
      groups.set(`item:${row.id}`, {
        key: `item:${row.id}`,
        contentId: row.content_id,
        title: row.title || t("web.admin.users.detail.common.unknownTitle"),
        episodes: [],
        movie: row,
        monitored: false,
      });
      continue;
    }
    const key = `series:${row.profile_id}:${row.content_id}`;
    let group = groups.get(key);
    if (!group) {
      group = {
        key,
        contentId: row.content_id,
        title: row.title || t("web.admin.users.detail.downloads.unknownSeries"),
        episodes: [],
        monitored: monitors.some(
          (monitor) =>
            monitor.active &&
            monitor.device_id === row.device_id &&
            monitor.profile_id === row.profile_id &&
            monitor.series_id === row.content_id,
        ),
      };
      groups.set(key, group);
    }
    group.episodes.push(row);
  }
  for (const group of groups.values()) group.episodes.sort(episodeOrder);
  return [...groups.values()];
}

/** A count message whose number prints as typed, without grouping. */
function counted(key: MessageKey, n: number): string {
  return t(key, { count: n, count_text: String(n) });
}

/** "Season 3", "Seasons 1, 2". */
function seasonList(seasons: number[]): string {
  return t("web.admin.users.detail.downloads.seasonList", {
    count: seasons.length,
    seasons: seasons.join(", "),
  });
}

/** "6 episodes · S02E01–E06", "3 episodes · Season 3", "4 episodes · Seasons 1, 2". */
export function seriesGroupSubtitle(group: DownloadGroup): string {
  const count = counted("web.admin.users.detail.downloads.episodeCount", group.episodes.length);
  const known = group.episodes
    .map((row) => row.episode)
    .filter((episode): episode is NonNullable<AdminUserDownload["episode"]> => episode !== null);
  if (known.length === 0) return count;
  const seasons = [...new Set(known.map((episode) => episode.season_number))].sort((a, b) => a - b);
  if (seasons.length > 1) return `${count} · ${seasonList(seasons)}`;
  const numbers = [...new Set(known.map((episode) => episode.episode_number))].sort(
    (a, b) => a - b,
  );
  const first = numbers[0]!;
  const last = numbers[numbers.length - 1]!;
  if (numbers.length === 1) return `${count} · ${episodeCode(seasons[0], first)}`;
  const consecutive = last - first + 1 === numbers.length && known.length === group.episodes.length;
  if (consecutive) {
    const range = t("web.admin.users.detail.downloads.episodeRange", {
      season: String(seasons[0]).padStart(2, "0"),
      first: String(first).padStart(2, "0"),
      last: String(last).padStart(2, "0"),
    });
    return `${count} · ${range}`;
  }
  return `${count} · ${seasonList(seasons)}`;
}

/**
 * One badge for a whole series group. A group whose episodes share a status
 * reads like a single row; a mixed group names the outstanding work, most
 * urgent first, and counts rows in different stages of work together as
 * "N in progress".
 */
export function seriesGroupStatus(group: DownloadGroup, android: boolean): DownloadBadge {
  const rows = group.movie ? [group.movie] : group.episodes;
  const counts = new Map<string, number>();
  for (const row of rows) {
    const status = observedStatus(row.status, android);
    counts.set(status, (counts.get(status) ?? 0) + 1);
  }
  if (counts.size === 1) return downloadStatusBadge([...counts.keys()][0]!, android);
  const n = (status: string) => counts.get(status) ?? 0;
  // On Android a ready row is only a request, not work in progress.
  const working = ["preparing", "downloading", ...(android ? [] : ["ready"])].filter(
    (status) => n(status) > 0,
  );
  if (working.length > 1) {
    const total = working.reduce((sum, status) => sum + n(status), 0);
    return {
      label: counted("web.admin.users.detail.downloads.count.inProgress", total),
      tone: "accent",
    };
  }
  if (n("downloading") > 0) {
    return {
      label: counted("web.admin.users.detail.downloads.count.downloading", n("downloading")),
      tone: "accent",
    };
  }
  if (n("failed") > 0) {
    return {
      label: counted("web.admin.users.detail.downloads.count.failed", n("failed")),
      tone: "danger",
    };
  }
  if (n("preparing") > 0) {
    return {
      label: counted("web.admin.users.detail.downloads.count.preparing", n("preparing")),
      tone: "neutral",
    };
  }
  if (n("ready") > 0)
    return {
      label: android
        ? counted("web.admin.users.detail.downloads.count.requested", n("ready"))
        : counted("web.admin.users.detail.downloads.count.waiting", n("ready")),
      tone: "neutral",
    };
  if (n("revoked") > 0) {
    return {
      label: counted("web.admin.users.detail.downloads.count.revoked", n("revoked")),
      tone: "warn",
    };
  }
  if (n("completed") === rows.length) return downloadStatusBadge("completed", android);
  return { label: t("web.admin.users.detail.downloads.status.mixed"), tone: "neutral" };
}

/** What a series monitor keeps on the device. */
export function monitorKeepsLabel(s: AdminUserDownloadSubscription): string {
  switch (s.mode) {
    case "all":
      return t("web.admin.users.detail.downloads.monitor.allEpisodes");
    case "future":
      return t("web.admin.users.detail.downloads.monitor.newOnly");
    case "latest_season":
      return s.target_season !== null
        ? t("web.admin.users.detail.downloads.monitor.latestSeasonNumber", {
            season: String(s.target_season).padStart(2, "0"),
          })
        : t("web.admin.users.detail.downloads.monitor.latestSeason");
    case "specific_seasons": {
      const seasons = [...s.season_numbers].sort((a, b) => a - b);
      if (seasons.length === 0) return t("web.admin.users.detail.downloads.monitor.chosenSeasons");
      return seasonList(seasons);
    }
    default:
      return s.mode;
  }
}

/** Where a monitor stands now: what's on the device and what's on its way. */
export function monitorNowLabel(s: AdminUserDownloadSubscription): string {
  if (!s.active) return t("web.admin.users.detail.downloads.monitor.paused");
  const parts: string[] = [];
  if (s.on_device > 0)
    parts.push(counted("web.admin.users.detail.downloads.count.onDevice", s.on_device));
  if (s.removed_episodes > 0) {
    parts.push(counted("web.admin.users.detail.downloads.count.removedByUser", s.removed_episodes));
  }
  if (s.in_progress > 0) {
    parts.push(counted("web.admin.users.detail.downloads.count.inProgress", s.in_progress));
  }
  if (parts.length > 0) return parts.join(" · ");
  return s.mode === "future"
    ? t("web.admin.users.detail.downloads.monitor.waitingNext")
    : t("web.admin.users.detail.downloads.monitor.nothingYet");
}
