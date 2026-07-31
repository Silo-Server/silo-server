import type { PlayerSubtitleInfo } from "../types";

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
