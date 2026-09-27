import { useQuery } from "@tanstack/react-query";
import { catalogKeys } from "@/hooks/queries/keys";
import { fetchCatalogSeriesSeasons, fetchCatalogSeasonEpisodes } from "@/hooks/queries/catalogRead";
import type { EpisodeRef } from "../types";

/**
 * Fetches the episode list for the current season and (conditionally) the next
 * season so that cross-season "next episode" navigation works.
 *
 * Returns a flat, chronologically-sorted EpisodeRef[] spanning up to two seasons.
 */
export function useSeriesEpisodes(
  seriesId: string | undefined,
  currentSeason: number,
  libraryId?: number,
): { episodes: EpisodeRef[]; isLoading: boolean } {
  // Fetch the seasons list so we know whether a next season exists.
  const { data: seasonsData, isLoading: seasonsLoading } = useQuery({
    queryKey: catalogKeys.seriesSeasons(seriesId!, libraryId),
    queryFn: ({ signal }) => fetchCatalogSeriesSeasons(seriesId!, libraryId, { signal }),
    enabled: !!seriesId,
    staleTime: 5 * 60 * 1000,
  });

  const seasons = seasonsData?.seasons ?? [];
  const currentSeasonInfo = seasons.find((s) => s.season_number === currentSeason);
  const nextSeasonInfo = seasons.find((s) => s.season_number === currentSeason + 1);

  // Fetch current season episodes.
  const { data: currentEpisodesData, isLoading: currentLoading } = useQuery({
    queryKey: catalogKeys.seasonEpisodes(seriesId!, currentSeason, libraryId),
    queryFn: ({ signal }) =>
      fetchCatalogSeasonEpisodes(seriesId!, currentSeason, libraryId, { signal }),
    enabled: !!seriesId && currentSeason >= 0 && !!currentSeasonInfo,
    staleTime: 5 * 60 * 1000,
  });

  // Fetch next season episodes only when a next season exists.
  const { data: nextEpisodesData, isLoading: nextLoading } = useQuery({
    queryKey: catalogKeys.seasonEpisodes(seriesId!, currentSeason + 1, libraryId),
    queryFn: ({ signal }) =>
      fetchCatalogSeasonEpisodes(seriesId!, currentSeason + 1, libraryId, { signal }),
    enabled: !!seriesId && !!nextSeasonInfo,
    staleTime: 5 * 60 * 1000,
  });

  const currentEpisodes = currentEpisodesData?.episodes ?? [];
  const nextEpisodes = nextEpisodesData?.episodes ?? [];

  const episodes: EpisodeRef[] = [...currentEpisodes, ...nextEpisodes].map((ep) => ({
    contentId: ep.content_id,
    seasonNumber: ep.season_number,
    episodeNumber: ep.episode_number,
    title: ep.title,
    runtime: ep.runtime,
    overview: ep.overview,
    stillUrl: ep.still_url,
    stillThumbhash: ep.still_thumbhash,
    airDate: ep.air_date,
  }));

  const isLoading = seasonsLoading || currentLoading || (!!nextSeasonInfo && nextLoading);

  return { episodes, isLoading };
}
