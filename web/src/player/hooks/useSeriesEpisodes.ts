import { useQuery } from "@tanstack/react-query";
import type { EpisodeListItem } from "@/api/types";
import { catalogKeys } from "@/hooks/queries/keys";
import { fetchCatalogSeriesSeasons, fetchCatalogSeasonEpisodes } from "@/hooks/queries/catalogRead";
import type { EpisodeRef } from "../types";

/**
 * Fetches the episode list for the current season and (conditionally) the next
 * season so that cross-season "next episode" navigation works.
 *
 * Returns a flat, chronologically-sorted EpisodeRef[] spanning up to two seasons,
 * holding only the episodes this viewer can play.
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
  const { episodes: currentEpisodes, isLoading: currentLoading } = usePlayableSeasonEpisodes(
    seriesId,
    currentSeason,
    libraryId,
    !!seriesId && currentSeason >= 0 && !!currentSeasonInfo,
  );

  // Fetch next season episodes only when a next season exists.
  const { episodes: nextEpisodes, isLoading: nextLoading } = usePlayableSeasonEpisodes(
    seriesId,
    currentSeason + 1,
    libraryId,
    !!seriesId && !!nextSeasonInfo,
  );

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

/**
 * Fetches one season's episodes and keeps those the viewer can play, so Next,
 * Previous and autoplay step over the rest.
 *
 * The server lists an episode this viewer can't play (every file outside their
 * access) with no files. With catalog.scope_versions_to_library on, a listing
 * for a library also leaves out files stored in the viewer's other libraries,
 * which playback still uses, so an episode listed without files there is
 * checked against the listing without the library before it is dropped.
 */
function usePlayableSeasonEpisodes(
  seriesId: string | undefined,
  seasonNum: number,
  libraryId: number | undefined,
  enabled: boolean,
): { episodes: EpisodeListItem[]; isLoading: boolean } {
  const { data, isLoading } = useQuery({
    queryKey: catalogKeys.seasonEpisodes(seriesId!, seasonNum, libraryId),
    queryFn: ({ signal }) =>
      fetchCatalogSeasonEpisodes(seriesId!, seasonNum, libraryId, { signal }),
    enabled,
    staleTime: 5 * 60 * 1000,
  });

  const listed = data?.episodes ?? [];
  const checkAllLibraries = !!libraryId && listed.some((ep) => ep.files.length === 0);
  const { data: allLibrariesData, isLoading: allLibrariesLoading } = useQuery({
    queryKey: catalogKeys.seasonEpisodes(seriesId!, seasonNum),
    queryFn: ({ signal }) =>
      fetchCatalogSeasonEpisodes(seriesId!, seasonNum, undefined, { signal }),
    enabled: enabled && checkAllLibraries,
    staleTime: 5 * 60 * 1000,
  });

  const playableElsewhere = new Set(
    (allLibrariesData?.episodes ?? [])
      .filter((ep) => ep.files.length > 0)
      .map((ep) => ep.content_id),
  );
  return {
    episodes: listed.filter((ep) => ep.files.length > 0 || playableElsewhere.has(ep.content_id)),
    isLoading: isLoading || allLibrariesLoading,
  };
}
