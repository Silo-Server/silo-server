import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";

import type {
  EpisodesResponse,
  FileVersion,
  ItemDetail,
  MangaSeriesFiles,
  SeasonDetailResponse,
  SeasonsResponse,
} from "@/api/types";
import {
  catalogItemDetailFromV2,
  episodeFromV2,
  fileVersionFromV2,
  mangaFilesFromV2,
  seasonFromV2,
} from "@/api/v2/catalog";
import { v2 } from "@/api/v2/request";
import { catalogKeys } from "./keys";

type ReadOptions = Pick<RequestInit, "signal"> | undefined;

function signalOf(options: ReadOptions): AbortSignal | undefined {
  return options?.signal ?? undefined;
}

function libraryQuery(libraryId?: number): { library_id?: string } {
  return libraryId ? { library_id: String(libraryId) } : {};
}

export async function fetchCatalogItemDetail(
  id: string,
  libraryId?: number,
  options?: ReadOptions,
): Promise<ItemDetail> {
  const detail = await v2("GET /api/v2/catalog/items/{id}", {
    path: { id },
    query: libraryQuery(libraryId),
    signal: signalOf(options),
  });
  return catalogItemDetailFromV2(detail);
}

export async function fetchCatalogItemVersions(
  id: string,
  options?: ReadOptions,
): Promise<FileVersion[]> {
  const versions = await v2("GET /api/v2/catalog/items/{id}/versions", {
    path: { id },
    signal: signalOf(options),
  });
  return versions.items.map(fileVersionFromV2);
}

export async function fetchCatalogItemEpisodes(
  id: string,
  libraryId?: number,
  options?: ReadOptions,
): Promise<EpisodesResponse> {
  const episodes = await v2("GET /api/v2/catalog/items/{id}/episodes", {
    path: { id },
    query: libraryQuery(libraryId),
    signal: signalOf(options),
  });
  return { episodes: episodes.items.map(episodeFromV2) };
}

export async function fetchCatalogSeriesSeasons(
  seriesId: string,
  libraryId?: number,
  options?: ReadOptions,
): Promise<SeasonsResponse> {
  const seasons = await v2("GET /api/v2/catalog/series/{id}/seasons", {
    path: { id: seriesId },
    query: libraryQuery(libraryId),
    signal: signalOf(options),
  });
  return { seasons: seasons.items.map(seasonFromV2) };
}

export async function fetchCatalogSeasonDetail(
  seriesId: string,
  seasonNum: number,
  libraryId?: number,
  options?: ReadOptions,
): Promise<SeasonDetailResponse> {
  const season = await v2("GET /api/v2/catalog/series/{id}/seasons/{num}", {
    path: { id: seriesId, num: seasonNum },
    query: libraryQuery(libraryId),
    signal: signalOf(options),
  });
  return { season: seasonFromV2(season) };
}

export async function fetchCatalogSeasonEpisodes(
  seriesId: string,
  seasonNum: number,
  libraryId?: number,
  options?: ReadOptions,
): Promise<EpisodesResponse> {
  const episodes = await v2("GET /api/v2/catalog/series/{id}/seasons/{num}/episodes", {
    path: { id: seriesId, num: seasonNum },
    query: libraryQuery(libraryId),
    signal: signalOf(options),
  });
  return { episodes: episodes.items.map(episodeFromV2) };
}

export function useCatalogItemDetail(id: string | undefined, libraryId?: number) {
  return useQuery({
    queryKey: catalogKeys.itemDetail(id!, libraryId),
    queryFn: ({ signal }) => fetchCatalogItemDetail(id!, libraryId, { signal }),
    enabled: !!id,
  });
}

export function useCatalogItemVersions(id: string | undefined) {
  return useQuery({
    queryKey: catalogKeys.itemVersions(id!),
    queryFn: ({ signal }) => fetchCatalogItemVersions(id!, { signal }),
    enabled: !!id,
  });
}

export async function fetchMangaSeriesFiles(
  id: string,
  options?: ReadOptions,
): Promise<MangaSeriesFiles> {
  const files = await v2("GET /api/v2/catalog/items/{id}/manga-files", {
    path: { id },
    signal: signalOf(options),
  });
  return mangaFilesFromV2(files);
}

// useMangaSeriesFiles backs the series "View Details" dialog; enabled defers
// the fetch until the dialog actually opens.
export function useMangaSeriesFiles(id: string | undefined, enabled: boolean) {
  return useQuery({
    queryKey: [...catalogKeys.itemDetail(id!), "manga-files"],
    queryFn: ({ signal }) => fetchMangaSeriesFiles(id!, { signal }),
    enabled: !!id && enabled,
  });
}

export function useCatalogItemEpisodes(id: string | undefined, libraryId?: number) {
  return useQuery({
    queryKey: catalogKeys.itemEpisodes(id!, libraryId),
    queryFn: ({ signal }) => fetchCatalogItemEpisodes(id!, libraryId, { signal }),
    enabled: !!id,
  });
}

/**
 * Returns a callback that warms the cache for a season detail page (item
 * detail + episodes), so navigating there from a series page renders without
 * a request waterfall. Prefetches are no-ops while the cached data is fresh.
 */
export function usePrefetchCatalogSeason(libraryId?: number) {
  const queryClient = useQueryClient();
  return useCallback(
    (seasonId: string) => {
      void queryClient.prefetchQuery({
        queryKey: catalogKeys.itemDetail(seasonId, libraryId),
        queryFn: ({ signal }) => fetchCatalogItemDetail(seasonId, libraryId, { signal }),
      });
      void queryClient.prefetchQuery({
        queryKey: catalogKeys.itemEpisodes(seasonId, libraryId),
        queryFn: ({ signal }) => fetchCatalogItemEpisodes(seasonId, libraryId, { signal }),
      });
    },
    [queryClient, libraryId],
  );
}

/** Warms an episode's detail query when the user shows intent to open it. */
export function usePrefetchCatalogItemDetail(libraryId?: number) {
  const queryClient = useQueryClient();
  return useCallback(
    (itemId: string) => {
      void queryClient.prefetchQuery({
        queryKey: catalogKeys.itemDetail(itemId, libraryId),
        queryFn: ({ signal }) => fetchCatalogItemDetail(itemId, libraryId, { signal }),
      });
    },
    [queryClient, libraryId],
  );
}

export function useCatalogSeriesSeasons(seriesId: string | undefined, libraryId?: number) {
  return useQuery({
    queryKey: catalogKeys.seriesSeasons(seriesId!, libraryId),
    queryFn: ({ signal }) => fetchCatalogSeriesSeasons(seriesId!, libraryId, { signal }),
    enabled: !!seriesId,
  });
}

export function useCatalogSeasonDetail(
  seriesId: string | undefined,
  seasonNum: number,
  libraryId?: number,
) {
  return useQuery({
    queryKey: catalogKeys.seasonDetail(seriesId!, seasonNum, libraryId),
    queryFn: ({ signal }) => fetchCatalogSeasonDetail(seriesId!, seasonNum, libraryId, { signal }),
    select: (data) => data.season,
    enabled: !!seriesId && seasonNum >= 0,
  });
}

export function useCatalogSeasonEpisodes(
  seriesId: string | undefined,
  seasonNum: number,
  libraryId?: number,
) {
  return useQuery({
    queryKey: catalogKeys.seasonEpisodes(seriesId!, seasonNum, libraryId),
    queryFn: ({ signal }) =>
      fetchCatalogSeasonEpisodes(seriesId!, seasonNum, libraryId, { signal }),
    enabled: !!seriesId && seasonNum >= 0,
  });
}
