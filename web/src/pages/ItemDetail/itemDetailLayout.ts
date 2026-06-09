import type { ItemDetail, Season } from "@/api/types";

export interface SeriesContinueWatchingItem {
  contentId: string;
  seriesId?: string;
  title?: string;
}

export interface SeriesPrimaryAction {
  label: string;
  context?: string;
  directHref?: string;
  targetSeasonId?: string;
  targetEpisodeNumber?: number;
}

export interface LeafPrimaryAction {
  label: string;
  progress?: number;
}

export interface EpisodeNavigationState {
  parentSeasonHref?: string;
  parentSeasonLabel?: string;
}

interface ResolveSeriesPrimaryActionInput {
  seriesId: string;
  seasons: Season[];
  continueWatching: SeriesContinueWatchingItem[];
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

export function resolveSeriesPrimaryAction({
  seriesId,
  seasons,
  continueWatching,
}: ResolveSeriesPrimaryActionInput): SeriesPrimaryAction {
  const resumeItem = continueWatching.find((entry) => entry.seriesId === seriesId);
  if (resumeItem) {
    return {
      label: "Resume",
      directHref: `/watch/${resumeItem.contentId}`,
      context: resumeItem.title ? `Continue ${resumeItem.title}` : undefined,
    };
  }

  const sortedSeasons = seasons.slice().sort((a, b) => a.season_number - b.season_number);

  const latestStartedSeason = sortedSeasons
    .slice()
    .reverse()
    .find((season) => {
      if (!season.user_data) {
        return false;
      }

      return (
        season.user_data.in_progress_count > 0 ||
        season.user_data.watched_count > 0 ||
        season.user_data.unplayed_count < season.episode_count
      );
    });

  if (latestStartedSeason) {
    const nextSeason = sortedSeasons.find(
      (season) => season.season_number > latestStartedSeason.season_number,
    );
    const latestSeasonIsComplete = latestStartedSeason.user_data?.played === true;
    const targetSeason = latestSeasonIsComplete && nextSeason ? nextSeason : latestStartedSeason;
    const watchedCount = targetSeason.user_data?.watched_count ?? 0;
    const targetEpisodeNumber =
      targetSeason.content_id === latestStartedSeason.content_id
        ? Math.min(watchedCount + 1, targetSeason.episode_count)
        : 1;

    return {
      label: "Play Latest",
      targetSeasonId: targetSeason.content_id,
      targetEpisodeNumber,
      context: `Jump back into ${getSeasonDisplayTitle(targetSeason)}`,
    };
  }

  const firstSeason = sortedSeasons[0];
  if (firstSeason) {
    return {
      label: "Start From Episode 1",
      targetSeasonId: firstSeason.content_id,
      targetEpisodeNumber: 1,
      context: `Begin with ${getSeasonDisplayTitle(firstSeason)}`,
    };
  }

  return {
    label: "Browse Series",
    context: "Episodes are not available yet",
  };
}
