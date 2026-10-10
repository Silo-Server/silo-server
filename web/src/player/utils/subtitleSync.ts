import type { components } from "@/api/v2/schema";
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
  return `${sign}${seconds.toFixed(1)} s`;
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
        return `${from}→${to} fps`;
      }
    }
  }
  return `×${Number(scale.toFixed(4))} speed`;
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
      return "Matching lines to speech…";
    case "analyzing":
      return "Listening to the audio…";
    default:
      return "Waiting to start…";
  }
}

/** Why a failed job failed, in plain words. */
export function syncFailureMessage(failure: SubtitleSyncFailure | undefined): string {
  switch (failure) {
    case "subtitle_changed":
      return "The subtitle changed while it was syncing. Try again.";
    case "no_audio":
      return "This video has no audio Silo can read.";
    case "unavailable":
      return "The server is busy. Try again in a few minutes.";
    default:
      return "Sync failed. Try again.";
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
      return percent ? `Syncing… ${percent}%` : "Syncing…";
    }
    case "no_match":
      return "Doesn't match this video";
    case "failed":
      return "Sync failed";
    case "already_synced":
      if (isIdentityTiming(timing)) return "Already in sync";
      break;
    case "synced":
      if (isIdentityTiming(timing)) return "Original timing";
      if (sameTiming(sync.result, timing)) return `Synced ${describeTiming(timing)}`;
      break;
  }
  return isIdentityTiming(timing) ? null : `Timing adjusted ${describeTiming(timing)}`;
}
