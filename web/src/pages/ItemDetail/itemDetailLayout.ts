import type { EpisodeListItem, ItemDetail, Season } from "@/api/types";
import { t } from "@/i18n";

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

/**
 * The library an episode card links to the episode with. An episode with no
 * file in the list keeps none: with catalog.scope_versions_to_library on, the
 * list holds only the opened library's files, and that episode then opens
 * with every version instead of nothing to play.
 */
export function episodeLinkLibraryId(
  episode: Pick<EpisodeListItem, "files">,
  libraryId: number | undefined,
): number | undefined {
  return (episode.files?.length ?? 0) > 0 ? libraryId : undefined;
}

function clampProgress(progress: number): number {
  return Math.max(0, Math.min(100, progress));
}

export function resolveLeafPrimaryAction(
  item: Pick<ItemDetail, "user_data">,
  defaultLabel?: string,
): LeafPrimaryAction {
  const userData = item.user_data;

  const playLabel = defaultLabel ?? t("web.detail.actions.play");
  if (!userData || !("position_seconds" in userData)) {
    return { label: playLabel };
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
      label: t("web.detail.actions.resume"),
      progress,
    };
  }

  return {
    label: playLabel,
    progress,
  };
}

export function getSeasonDisplayTitle(season: Season): string {
  if (season.is_specials || season.season_number === 0) {
    return t("web.detail.seasons.specials");
  }

  if (season.title && season.title !== `Season ${season.season_number}`) {
    return season.title;
  }

  return t("web.detail.seasons.numbered", { season: String(season.season_number) });
}

export function formatEpisodeCount(count: number): string {
  return t("web.detail.seasons.episodeCount", { count, count_text: String(count) });
}

export function formatSeasonMeta(season: Season): string {
  return formatEpisodeCount(season.episode_count);
}

/** Watched progress for a season card, e.g. "1 of 2 episodes". */
export function formatSeasonProgress(season: Season, watchedCount: number): string {
  return t("web.detail.seasons.watchedProgress", {
    count: season.episode_count,
    watched: String(watchedCount),
    count_text: String(season.episode_count),
  });
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
    return { label: t("web.detail.actions.browseSeries") };
  }

  const href = `/watch/${item.play_content_id}`;
  const rollup = item.user_data && "in_progress_count" in item.user_data ? item.user_data : null;
  if (rollup && rollup.in_progress_count > 0) {
    return { label: t("web.detail.actions.resume"), href };
  }
  if (rollup && rollup.watched_count > 0 && !rollup.played) {
    return { label: t("web.detail.actions.playNext"), href };
  }
  // Unstarted and fully watched series both resolve to the first episode.
  return { label: t("web.detail.actions.startFromFirstEpisode", { episode: 1 }), href };
}
