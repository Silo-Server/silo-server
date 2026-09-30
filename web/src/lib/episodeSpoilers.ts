/**
 * Spoiler protection for episodes the profile has not started. The profile
 * settings catalog.hide_unwatched_episode_images and
 * catalog.hide_unwatched_episode_overviews choose what to hide; this module
 * owns the rule for which episodes they apply to, shared by every surface.
 */

export interface EpisodeWatchState {
  played?: boolean;
  is_in_progress?: boolean;
  position_seconds?: number;
}

/**
 * An episode is unwatched while it is neither played nor in progress. Missing
 * watch state counts as unwatched, so an episode is never revealed just
 * because its payload left the state out.
 */
export function isEpisodeUnwatched(state: EpisodeWatchState | null | undefined): boolean {
  if (!state) return true;
  if (state.played || state.is_in_progress) return false;
  return (state.position_seconds ?? 0) <= 0;
}

/**
 * Classes for a hidden episode still, used in place of the card's hover zoom.
 * The scale pushes the blur's soft edge outside the card's clipped frame.
 */
export const SPOILER_IMAGE_CLASS = "scale-110 blur-xl";

/** Older payloads leave provenance unknown; preserve their spoiler protection. */
export function isEpisodeStill(provenance: boolean | undefined): boolean {
  return provenance !== false;
}
