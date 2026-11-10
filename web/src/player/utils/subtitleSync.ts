import type { components } from "@/api/v2/schema";
import { t } from "@/i18n";
import type { PlayerSubtitleInfo } from "../types";

export type StoredSubtitle = components["schemas"]["StoredSubtitle"];
export type SubtitleSyncState = components["schemas"]["SubtitleSyncState"];
export type SubtitleSyncJob = components["schemas"]["SubtitleSyncJobState"];
export type SubtitleTiming = components["schemas"]["SubtitleTiming"];
export type SubtitleSyncStatus = SubtitleSyncJob["status"];
export type SubtitleSyncFailure = NonNullable<SubtitleSyncJob["failure"]>;

/** The query parameter that pins a downloaded-track URL to its stored row. */
const STORED_ID_PARAM = "downloaded_subtitle_id";

/**
 * The sync key of a track whose timing the server can correct (a stored
 * subtitle or a subtitle file next to the media), or null. The plan's
 * inventory publishes it as `sync_key`; a live translation has none yet.
 *
 * A plan from a server before sidecar sync (an older API server during a
 * rolling upgrade) has no `sync_key`. Its downloaded tracks still pin their
 * URL to the stored row with `downloaded_subtitle_id`, which names the key.
 */
export function syncKeyOf(track: PlayerSubtitleInfo | null | undefined): string | null {
  if (!track || track.live) return null;
  if (track.sync_key) return track.sync_key;
  if (track.source !== "downloaded" || !track.url) return null;
  let raw: string | null;
  try {
    raw = new URL(track.url, "http://player.invalid").searchParams.get(STORED_ID_PARAM);
  } catch {
    return null;
  }
  return raw && /^[1-9][0-9]*$/.test(raw) ? `stored-${raw}` : null;
}

/** The sync state a stored subtitle returned by a download or upload carries. */
export function storedSyncState(subtitle: StoredSubtitle): SubtitleSyncState {
  return {
    key: `stored-${subtitle.id}`,
    media_file_id: subtitle.media_file_id,
    source: "downloaded",
    stored_subtitle_id: subtitle.id,
    language: subtitle.language,
    format: subtitle.format,
    label: subtitle.release_name || subtitle.provider,
    timing: subtitle.timing,
    sync: subtitle.sync,
  };
}

export function isIdentityTiming(timing: SubtitleTiming | undefined): boolean {
  return !timing || (timing.offset_ms === 0 && timing.scale === 1);
}

export function sameTiming(a: SubtitleTiming | undefined, b: SubtitleTiming | undefined): boolean {
  if (!a || !b) return isIdentityTiming(a) && isIdentityTiming(b);
  return a.offset_ms === b.offset_ms && a.scale === b.scale;
}

export function isSyncInProgress(status: SubtitleSyncStatus | undefined): boolean {
  return status === "pending" || status === "running";
}

/** "+2.3 s" / "−0.4 s"; a whole-second shift keeps one decimal for scanability. */
export function formatSyncOffset(offsetMs: number): string {
  const seconds = Math.abs(offsetMs) / 1000;
  const sign = offsetMs < 0 ? "−" : "+";
  return t("web.subtitles.sync.offsetSeconds", { offset: `${sign}${seconds.toFixed(1)}` });
}

const FRAME_RATES = [23.976, 24, 25, 29.97, 30, 50, 59.94, 60];
// The server reports a frame-rate conversion as its exact ratio; anything
// else is drift between two cuts, which only looks like a nearby ratio.
const FRAME_RATE_TOLERANCE = 1e-6;

/**
 * Names a scale correction as the frame-rate conversion it most likely is
 * ("25→23.976 fps"), or as a plain speed factor when it matches none.
 *
 * Original time t plays at t * scale, so a subtitle cut for the faster
 * source rate `from` stretched onto the slower video rate `to` has
 * scale = from / to.
 */
export function describeSyncScale(scale: number): string | null {
  if (!Number.isFinite(scale) || scale === 1) return null;
  for (const from of FRAME_RATES) {
    for (const to of FRAME_RATES) {
      if (from !== to && Math.abs(from / to - scale) <= FRAME_RATE_TOLERANCE) {
        return t("web.subtitles.sync.frameRate", { from: String(from), to: String(to) });
      }
    }
  }
  return t("web.subtitles.sync.speed", { factor: String(Number(scale.toFixed(4))) });
}

/** "+2.3 s · 25→23.976 fps": a correction in a few characters. */
export function describeTiming(timing: SubtitleTiming): string {
  const scale = describeSyncScale(timing.scale);
  const offset = timing.offset_ms !== 0 || !scale ? formatSyncOffset(timing.offset_ms) : null;
  return [offset, scale].filter(Boolean).join(" · ");
}

/** An active job's progress as a whole percentage, or null once it finished. */
export function syncProgressPercent(job: SubtitleSyncJob | undefined): number | null {
  if (!job || !isSyncInProgress(job.status)) return null;
  return Math.round(Math.min(1, Math.max(0, job.progress ?? 0)) * 100);
}

/** What an active job is doing, in a few words. */
export function syncPhaseLabel(job: SubtitleSyncJob | undefined): string | null {
  if (!job || !isSyncInProgress(job.status)) return null;
  switch (job.phase) {
    case "matching":
      return t("web.subtitles.sync.phase.matching");
    case "analyzing":
      return t("web.subtitles.sync.phase.analyzing");
    default:
      return t("web.subtitles.sync.phase.waiting");
  }
}

/** Why a failed job failed, in plain words. */
export function syncFailureMessage(failure: SubtitleSyncFailure | undefined): string {
  switch (failure) {
    case "subtitle_changed":
      return t("web.subtitles.sync.failure.subtitleChanged");
    case "no_audio":
      return t("web.subtitles.sync.failure.noAudio");
    case "unavailable":
      return t("web.subtitles.sync.failure.unavailable");
    default:
      return t("web.subtitles.sync.failure.generic");
  }
}

/**
 * One short line describing a subtitle's timing, or null when there is
 * nothing to say (never synced and never adjusted).
 */
export function syncStatusLabel(
  subtitle: Pick<SubtitleSyncState, "timing" | "sync">,
): string | null {
  const { timing, sync } = subtitle;
  switch (sync?.status) {
    case "pending":
    case "running": {
      const percent = syncProgressPercent(sync);
      return percent
        ? t("web.subtitles.sync.status.syncingPercent", { percent: String(percent) })
        : t("web.subtitles.sync.status.syncing");
    }
    case "no_match":
      return t("web.subtitles.sync.status.noMatch");
    case "failed":
      return t("web.subtitles.sync.status.failed");
    case "already_synced":
      if (isIdentityTiming(timing)) return t("web.subtitles.sync.status.alreadyInSync");
      break;
    case "synced":
      if (isIdentityTiming(timing)) return t("web.subtitles.sync.status.originalTiming");
      if (sameTiming(sync.result, timing))
        return t("web.subtitles.sync.status.synced", { timing: describeTiming(timing) });
      break;
  }
  return isIdentityTiming(timing)
    ? null
    : t("web.subtitles.sync.status.adjusted", { timing: describeTiming(timing) });
}
