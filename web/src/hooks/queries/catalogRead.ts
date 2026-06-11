import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";

import { api } from "@/api/client";
import type {
  EpisodesResponse,
  FileVersion,
  ItemDetail,
  MangaSeriesFiles,
  SeasonDetailResponse,
  SeasonsResponse,
} from "@/api/types";
import { catalogKeys } from "./keys";

function catalogPathID(id: string): string {
  return encodeURIComponent(id);
}

export async function fetchCatalogItemDetail(
  id: string,
  libraryId?: number,
  options?: RequestInit,
): Promise<ItemDetail> {
  const query = libraryId ? `?library_id=${libraryId}` : "";
  return api<ItemDetail>(`/catalog/items/${catalogPathID(id)}${query}`, options);
}

export async function fetchCatalogItemVersions(
  id: string,
  options?: RequestInit,
): Promise<FileVersion[]> {
  return api<FileVersion[]>(`/catalog/items/${catalogPathID(id)}/versions`, options);
}

export async function fetchCatalogItemEpisodes(
  id: string,
  libraryId?: number,
  options?: RequestInit,
): Promise<EpisodesResponse> {
  const query = libraryId ? `?library_id=${libraryId}` : "";
  return api<EpisodesResponse>(`/catalog/items/${catalogPathID(id)}/episodes${query}`, options);
}

export async function fetchCatalogSeriesSeasons(
  seriesId: string,
  libraryId?: number,
  options?: RequestInit,
): Promise<SeasonsResponse> {
  const query = libraryId ? `?library_id=${libraryId}` : "";
  return api<SeasonsResponse>(
    `/catalog/series/${catalogPathID(seriesId)}/seasons${query}`,
    options,
  );
}

export async function fetchCatalogSeasonDetail(
  seriesId: string,
  seasonNum: number,
  libraryId?: number,
  options?: RequestInit,
): Promise<SeasonDetailResponse> {
  const query = libraryId ? `?library_id=${libraryId}` : "";
  return api<SeasonDetailResponse>(
    `/catalog/series/${catalogPathID(seriesId)}/seasons/${seasonNum}${query}`,
    options,
  );
}

export async function fetchCatalogSeasonEpisodes(
  seriesId: string,
  seasonNum: number,
  libraryId?: number,
  options?: RequestInit,
): Promise<EpisodesResponse> {
  const query = libraryId ? `?library_id=${libraryId}` : "";
  return api<EpisodesResponse>(
    `/catalog/series/${catalogPathID(seriesId)}/seasons/${seasonNum}/episodes${query}`,
    options,
  );
}

export function useCatalogItemDetail(id: string | undefined, libraryId?: number) {
  return useQuery({
    queryKey: catalogKeys.itemDetail(id!, libraryId),
    queryFn: () => fetchCatalogItemDetail(id!, libraryId),
    enabled: !!id,
    placeholderData: keepPreviousData,
  });
}

export function useCatalogItemVersions(id: string | undefined) {
  return useQuery({
    queryKey: catalogKeys.itemVersions(id!),
    queryFn: () => fetchCatalogItemVersions(id!),
    enabled: !!id,
    placeholderData: keepPreviousData,
  });
}

export async function fetchMangaSeriesFiles(
  id: string,
  options?: RequestInit,
): Promise<MangaSeriesFiles> {
  return api<MangaSeriesFiles>(`/catalog/items/${catalogPathID(id)}/manga-files`, options);
}

// useMangaSeriesFiles backs the series "View Details" dialog; enabled defers
// the fetch until the dialog actually opens.
export function useMangaSeriesFiles(id: string | undefined, enabled: boolean) {
  return useQuery({
    queryKey: [...catalogKeys.itemDetail(id!), "manga-files"],
    queryFn: () => fetchMangaSeriesFiles(id!),
    enabled: !!id && enabled,
  });
}

export function useCatalogItemEpisodes(id: string | undefined, libraryId?: number) {
  return useQuery({
    queryKey: catalogKeys.itemEpisodes(id!, libraryId),
    queryFn: () => fetchCatalogItemEpisodes(id!, libraryId),
    enabled: !!id,
    placeholderData: keepPreviousData,
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
        queryFn: () => fetchCatalogItemDetail(seasonId, libraryId),
      });
      void queryClient.prefetchQuery({
        queryKey: catalogKeys.itemEpisodes(seasonId, libraryId),
        queryFn: () => fetchCatalogItemEpisodes(seasonId, libraryId),
      });
    },
    [queryClient, libraryId],
  );
}

export function useCatalogSeriesSeasons(seriesId: string | undefined, libraryId?: number) {
  return useQuery({
    queryKey: catalogKeys.seriesSeasons(seriesId!, libraryId),
    queryFn: () => fetchCatalogSeriesSeasons(seriesId!, libraryId),
    enabled: !!seriesId,
    placeholderData: keepPreviousData,
  });
}

export function useCatalogSeasonDetail(
  seriesId: string | undefined,
  seasonNum: number,
  libraryId?: number,
) {
  return useQuery({
    queryKey: catalogKeys.seasonDetail(seriesId!, seasonNum, libraryId),
    queryFn: () => fetchCatalogSeasonDetail(seriesId!, seasonNum, libraryId),
    select: (data) => data.season,
    enabled: !!seriesId && seasonNum >= 0,
    placeholderData: keepPreviousData,
  });
}

export function useCatalogSeasonEpisodes(
  seriesId: string | undefined,
  seasonNum: number,
  libraryId?: number,
) {
  return useQuery({
    queryKey: catalogKeys.seasonEpisodes(seriesId!, seasonNum, libraryId),
    queryFn: () => fetchCatalogSeasonEpisodes(seriesId!, seasonNum, libraryId),
    enabled: !!seriesId && seasonNum >= 0,
    placeholderData: keepPreviousData,
  });
}
