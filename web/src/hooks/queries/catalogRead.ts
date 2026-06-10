import { keepPreviousData, useQuery } from "@tanstack/react-query";

import { api } from "@/api/client";
import type {
  EpisodesResponse,
  FileVersion,
  ItemDetail,
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

export function useCatalogItemEpisodes(id: string | undefined, libraryId?: number) {
  return useQuery({
    queryKey: catalogKeys.itemEpisodes(id!, libraryId),
    queryFn: () => fetchCatalogItemEpisodes(id!, libraryId),
    enabled: !!id,
    placeholderData: keepPreviousData,
  });
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
