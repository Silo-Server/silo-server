import { hashKey, type QueryClient, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect } from "react";

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
import { fetchPeopleSearchCapabilities } from "./personSearch";

type ReadOptions = Pick<RequestInit, "signal"> | undefined;
type ItemDetailReadOptions = (Pick<RequestInit, "signal"> & { prefetch?: boolean }) | undefined;

function signalOf(options: ReadOptions): AbortSignal | undefined {
  return options?.signal ?? undefined;
}

function libraryQuery(libraryId?: number): { library_id?: string } {
  return libraryId ? { library_id: String(libraryId) } : {};
}

/**
 * Reads one item's detail. A plain read counts as opening the title: the
 * server can queue a metadata refresh and promote a watchlist entry.
 * `prefetch` marks a speculative cache warm-up that must do neither.
 */
export async function fetchCatalogItemDetail(
  id: string,
  libraryId?: number,
  options?: ItemDetailReadOptions,
): Promise<ItemDetail> {
  const detail = await v2("GET /api/v2/catalog/items/{id}", {
    path: { id },
    query: { ...libraryQuery(libraryId), ...(options?.prefetch ? { prefetch: true } : {}) },
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

// Item detail queries a prefetch filled, by query hash. The item page reads
// such a detail again as a view, so the server sees the title opened. Keyed by
// query rather than by detail object, since cache patches and structural
// sharing replace or keep objects independently of how the data was read.
const pendingItemDetailViews = new Set<string>();

/**
 * The item page's detail query. A detail a prefetch cached renders at once,
 * and the page then reads it again as a view in the background. The pending
 * view is consumed when the read starts, so a failing view read is not
 * retried in a loop.
 */
export function useCatalogItemDetailView(id: string | undefined, libraryId?: number) {
  const query = useCatalogItemDetail(id, libraryId);
  const { data, isFetching, refetch } = query;
  useEffect(() => {
    if (!id || !data || isFetching) return;
    if (!pendingItemDetailViews.delete(hashKey(catalogKeys.itemDetail(id, libraryId)))) return;
    void refetch();
  }, [id, libraryId, data, isFetching, refetch]);
  return query;
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
      void prefetchCatalogItemDetail(queryClient, seasonId, libraryId);
      void queryClient.prefetchQuery({
        queryKey: catalogKeys.itemEpisodes(seasonId, libraryId),
        queryFn: ({ signal }) => fetchCatalogItemEpisodes(seasonId, libraryId, { signal }),
      });
    },
    [queryClient, libraryId],
  );
}

/**
 * Warms an item's detail query when the user shows intent to open it. The
 * read is marked as a prefetch so it has no server side effects; a server that
 * does not advertise `item_detail_prefetch` would reject the marker, so
 * nothing is prefetched from it.
 */
export function usePrefetchCatalogItemDetail(libraryId?: number) {
  const queryClient = useQueryClient();
  return useCallback(
    (itemId: string) => {
      void prefetchCatalogItemDetail(queryClient, itemId, libraryId);
    },
    [queryClient, libraryId],
  );
}

// A write that overlaps a prefetch read (a favorite or watchlist quick action
// on the hovered card) can leave the read holding the state from before it.
// Such a read is not cached as fresh.
function writeOverlaps(queryClient: QueryClient, since: number) {
  return queryClient
    .getMutationCache()
    .getAll()
    .some((mutation) => mutation.state.status === "pending" || mutation.state.submittedAt >= since);
}

class DiscardedItemDetailPrefetch extends Error {}

async function prefetchCatalogItemDetail(
  queryClient: QueryClient,
  itemId: string,
  libraryId?: number,
) {
  const capabilities = await fetchPeopleSearchCapabilities(queryClient).catch(() => null);
  if (!capabilities?.item_detail_prefetch || queryClient.isMutating() > 0) return;
  const queryKey = catalogKeys.itemDetail(itemId, libraryId);
  await queryClient.prefetchQuery({
    queryKey,
    queryFn: async ({ signal }) => {
      const startedAt = Date.now();
      const detail = await fetchCatalogItemDetail(itemId, libraryId, { signal, prefetch: true });
      if (!writeOverlaps(queryClient, startedAt)) {
        pendingItemDetailViews.add(hashKey(queryKey));
        return detail;
      }
      // A page that opened while the read was in flight is waiting on it, so
      // it gets an ordinary read (a view) in place of the discarded one.
      if (queryClient.getQueryCache().find({ queryKey, exact: true })?.getObserversCount()) {
        return fetchCatalogItemDetail(itemId, libraryId, { signal });
      }
      throw new DiscardedItemDetailPrefetch();
    },
    retry: false,
  });
  // Leave no error behind for a page that opens later; it reads afresh.
  queryClient.removeQueries({
    queryKey,
    exact: true,
    predicate: (query) =>
      query.state.error instanceof DiscardedItemDetailPrefetch && query.getObserversCount() === 0,
  });
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
