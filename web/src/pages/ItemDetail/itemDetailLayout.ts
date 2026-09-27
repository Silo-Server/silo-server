import type { ItemDetail, Season } from "@/api/types";

export interface SeriesPrimaryAction {
  label: string;
  href?: string;
}

export interface LeafPrimaryAction {
  label: string;
  progress?: number;
}

export interface EpisodeNavigationState {
  parentSeasonHref?: string;
  parentSeasonLabel?: string;
}

function clampProgress(progress: number): number {
  return Math.max(0, Math.min(100, progress));
}

export function resolveLeafPrimaryAction(
  item: Pick<ItemDetail, "user_data">,
  defaultLabel = "Play",
): LeafPrimaryAction {
  const userData = item.user_data;

  if (!userData || !("position_seconds" in userData)) {
    return { label: defaultLabel };
  }

  const positionSeconds = userData.position_seconds ?? 0;
  const durationSeconds = userData.duration_seconds ?? 0;
  const progress =
    positionSeconds > 0 && durationSeconds > 0
      ? clampProgress((positionSeconds / durationSeconds) * 100)
      : undefined;
  // Watched items store position 0, so any nonzero position is a live resume
  // point — including a rewatch in flight (played stays true).
  const isInProgress = (userData.is_in_progress ?? false) || positionSeconds > 0;

  if (isInProgress) {
    return {
      label: "Resume",
      progress,
    };
  }

  return {
    label: defaultLabel,
    progress,
  };
}

export function getSeasonDisplayTitle(season: Season): string {
  if (season.is_specials || season.season_number === 0) {
    return "Specials";
  }

  if (season.title && season.title !== `Season ${season.season_number}`) {
    return season.title;
  }

  return `Season ${season.season_number}`;
}

export function formatSeasonMeta(season: Season): string {
  return `${season.episode_count} episodes`;
}

export function resolveEpisodeSiblingSeason(
  item: Pick<ItemDetail, "series_id" | "season_number">,
): { seriesId: string; seasonNumber: number } | null {
  if (!item.series_id || item.season_number == null) {
    return null;
  }

  return {
    seriesId: item.series_id,
    seasonNumber: item.season_number,
  };
}

/**
 * The series play button. The server resolves `play_content_id` for the
 * acting profile with the rule every series card uses: the newest in-progress
 * episode, else the first unwatched one, else the first available one. The
 * label reads the series watch rollup from the same detail response, so the
 * button is final as soon as the detail arrives.
 */
export function resolveSeriesPrimaryAction(
  item: Pick<ItemDetail, "play_content_id" | "user_data">,
): SeriesPrimaryAction {
  if (!item.play_content_id) {
    return { label: "Browse Series" };
  }

  const href = `/watch/${item.play_content_id}`;
  const rollup = item.user_data && "in_progress_count" in item.user_data ? item.user_data : null;
  if (rollup && rollup.in_progress_count > 0) {
    return { label: "Resume", href };
  }
  if (rollup && rollup.watched_count > 0 && !rollup.played) {
    return { label: "Play Next", href };
  }
  // Unstarted and fully watched series both resolve to the first episode.
  return { label: "Start From Episode 1", href };
}
