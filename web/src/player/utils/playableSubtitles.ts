import type { PlayerSubtitleInfo } from "../types";
import type { SubtitleModeV3 } from "../protocol-v3";

function hasPlayableUrl(track: PlayerSubtitleInfo): boolean {
  return track.url.trim().length > 0;
}

/**
 * A track from the plan's inventory is selectable when the client can fetch it
 * *or* when the server publishes it as `burn_in_only` — the latter has no URL
 * on purpose, because selecting it asks the server to composite it into the
 * video instead of handing over a sidecar.
 */
function isSelectableSessionTrack(track: PlayerSubtitleInfo): boolean {
  return hasPlayableUrl(track) || track.burn_in_only === true;
}

export function resolvePlayableSubtitles(
  sessionTracks: PlayerSubtitleInfo[],
  fallbackTracks: PlayerSubtitleInfo[],
): PlayerSubtitleInfo[] {
  const selectableSessionTracks = sessionTracks.filter(isSelectableSessionTrack);
  if (selectableSessionTracks.length > 0) {
    return selectableSessionTracks;
  }
  // The watch-detail fallback is not an inventory: it carries no delivery
  // information, so a track without a URL there is simply unplayable.
  return fallbackTracks.filter(hasPlayableUrl);
}

/**
 * Returns the selection that must be sent to settle the plan's authoritative
 * selected_tracks state, or undefined when the current plan already matches
 * the UI. Sidecar selections are included: the server owns durable track
 * intent even when the browser renders the selected artifact itself.
 */
export function pendingServerSubtitleSelection(
  planMode: SubtitleModeV3,
  planSelectedIndex: number | null,
  activeIndex: number | null,
  activeRequiresBurnIn: boolean,
): number | null | undefined {
  const planBurnsIn = planMode === "burn_in";
  // A selected track in an already burn-in plan is authoritative even when
  // the inventory also exposes a sidecar artifact. Re-requesting that same
  // ordinal cannot improve the route; it only reloads the identical stream.
  if (planSelectedIndex === activeIndex && (planBurnsIn || !activeRequiresBurnIn)) {
    return undefined;
  }
  return activeIndex;
}
