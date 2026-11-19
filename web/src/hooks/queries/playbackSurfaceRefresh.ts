import type { Query, QueryClient, QueryFilters } from "@tanstack/react-query";
import { isLibraryLayoutQueryKey } from "@/lib/queryInvalidation";
import { catalogKeys, historyKeys, personKeys, progressKeys, recKeys, sectionKeys } from "./keys";

const playbackSurfaceFilters: QueryFilters[] = [
  { queryKey: catalogKeys.all },
  { queryKey: progressKeys.all },
  { queryKey: historyKeys.all },
  { queryKey: recKeys.all },
  { queryKey: personKeys.all },
  { queryKey: sectionKeys.homeItemsRoot() },
  {
    queryKey: sectionKeys.libraryRoot(),
    predicate: (query) => !isLibraryLayoutQueryKey(query.queryKey),
  },
];

/**
 * Marks playback-derived surfaces stale so the next mounted screen revalidates
 * continue-watching and related rows without a full app refresh.
 */
export async function invalidatePlaybackSurfaceQueries(queryClient: QueryClient) {
  await Promise.all(
    playbackSurfaceFilters.map((filters) => queryClient.invalidateQueries(filters)),
  );
}

const isFirstFetch = (query: Query) =>
  query.state.data === undefined && query.state.fetchStatus === "fetching";

/**
 * Re-reads playback-derived surfaces after a write has landed. A first fetch
 * still in flight may have read before the write, and invalidating it would
 * reuse that fetch, so it is cancelled and started again.
 */
export async function refreshPlaybackSurfaceQueries(queryClient: QueryClient) {
  await Promise.all(
    playbackSurfaceFilters.map((filters) =>
      queryClient.cancelQueries({
        ...filters,
        predicate: (query) => isFirstFetch(query) && (filters.predicate?.(query) ?? true),
      }),
    ),
  );
  await invalidatePlaybackSurfaceQueries(queryClient);
}
