import type { components } from "@/api/v2/schema";
import type { PlayerSubtitleInfo } from "../types";

export type StoredSubtitle = components["schemas"]["StoredSubtitle"];
export type SubtitleSyncJob = components["schemas"]["SubtitleSyncJob"];
export type SubtitleTiming = components["schemas"]["SubtitleTiming"];
export type SubtitleSyncStatus = SubtitleSyncJob["status"];

/** The query parameter that pins a downloaded-track URL to its stored row. */
const STORED_ID_PARAM = "downloaded_subtitle_id";

/**
 * The stored-subtitle ID behind a downloaded track, or null for embedded,
 * external, live, and unpinned tracks.
 *
 * The plan's inventory does not name stored rows; the server pins each
 * downloaded track's sidecar URL to its row with `downloaded_subtitle_id`
 * (playback protocol v3, subtitle artifact routes). That pin is the only
 * stored-ID carrier the player has, so it is read here and nowhere else.
 */
export function storedSubtitleIdOf(track: PlayerSubtitleInfo | null | undefined): string | null {
  if (!track || track.source !== "downloaded" || track.live || !track.url) return null;
  let raw: string | null;
  try {
    raw = new URL(track.url, "http://player.invalid").searchParams.get(STORED_ID_PARAM);
  } catch {
    return null;
  }
  return raw && /^[1-9][0-9]*$/.test(raw) ? raw : null;
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

function describeTiming(timing: SubtitleTiming): string {
  const scale = describeSyncScale(timing.scale);
  const offset = timing.offset_ms !== 0 || !scale ? formatSyncOffset(timing.offset_ms) : null;
  return [offset, scale].filter(Boolean).join(" · ");
}

/**
 * One short line describing a stored subtitle's timing, or null when there
 * is nothing to say (never synced and never adjusted).
 */
export function syncStatusLabel(subtitle: Pick<StoredSubtitle, "timing" | "sync">): string | null {
  const { timing, sync } = subtitle;
  switch (sync?.status) {
    case "pending":
    case "running":
      return "Syncing…";
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
