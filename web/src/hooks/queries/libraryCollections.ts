import { isCancelledError, useQuery, type QueryClient } from "@tanstack/react-query";
import type { BrowseResponse, LibraryCollection, LibraryTabResponse } from "@/api/types";
import { catalogItemFromV2, libraryCollectionTabFromV2 } from "@/api/v2/catalog";
import { v2 } from "@/api/v2/request";
import { libraryCollectionKeys, libraryCollectionTitleKeys } from "./keys";

export function libraryCollectionsQueryOptions(libraryId: number) {
  return {
    queryKey: libraryCollectionKeys.list(libraryId),
    queryFn: ({
      client,
      signal,
    }: {
      client?: QueryClient;
      signal?: AbortSignal;
    }): Promise<LibraryTabResponse> =>
      v2("GET /api/v2/library/{id}/collections", { path: { id: String(libraryId) }, signal })
        .then(libraryCollectionTabFromV2)
        .then((tab) => {
          // Every tab read also refreshes the titles that name pinned
          // collections, so the sidebar never lags the tab it sits beside.
          if (!signal?.aborted) {
            client?.setQueryData(
              libraryCollectionTitleKeys.library(libraryId),
              collectionTitles(tab),
            );
          }
          return tab;
        }),
    enabled: Number.isFinite(libraryId) && libraryId > 0,
  };
}

function collectionTitles(tab: LibraryTabResponse): ReadonlyMap<string, string> {
  return new Map(
    getLibraryCollectionList(tab).map((collection) => [collection.id, collection.title]),
  );
}

const COLLECTION_TITLES_STALE_MS = 5 * 60 * 1000;

/**
 * The titles of a library's collections by ID, for naming pinned collections.
 * Every Collections tab read refreshes it, and only server-collection edits
 * invalidate it (see libraryCollectionTitleKeys). It reads through the tab's
 * query, so a tab read already in flight answers it without a second request.
 * A finished tab response is not reused: its age would add to this query's
 * stale time and keep a renamed collection's old title longer.
 */
export function libraryCollectionTitlesQuery(libraryId: number) {
  return {
    queryKey: libraryCollectionTitleKeys.library(libraryId),
    queryFn: ({
      client,
      signal,
    }: {
      client: QueryClient;
      signal: AbortSignal;
    }): Promise<ReadonlyMap<string, string>> => {
      const tabQuery = libraryCollectionsQueryOptions(libraryId);
      const readAlone = () => tabQuery.queryFn({ signal });
      const tabUpdates = () => client.getQueryState(tabQuery.queryKey)?.dataUpdateCount ?? 0;
      const updatesBefore = tabUpdates();
      // The tab's request is cancelled when the tab closes before it lands.
      // That either rejects or, over an older cached response, hands that
      // response back; either way the titles read on their own.
      return client
        .fetchQuery({ ...tabQuery, staleTime: 0 })
        .then(
          (tab) => (tabUpdates() > updatesBefore ? tab : readAlone()),
          (error: unknown) => {
            if (isCancelledError(error)) return readAlone();
            throw error;
          },
        )
        .then(collectionTitles);
    },
    staleTime: COLLECTION_TITLES_STALE_MS,
    // The tab read retries on its own; retrying here as well would repeat it.
    retry: false,
  };
}

export function useLibraryCollections(libraryId: number) {
  return useQuery(libraryCollectionsQueryOptions(libraryId));
}

export function getLibraryCollectionList(
  resp: LibraryTabResponse | undefined,
): LibraryCollection[] {
  return resp?.collections ?? [];
}

// Pinned rows are bounded teasers; the title opens the full catalog collection.
export function useLibraryCollectionItems(libraryId: number, collectionId: string | null) {
  return useQuery({
    queryKey: libraryCollectionKeys.items(libraryId, collectionId ?? ""),
    queryFn: ({ signal }) =>
      v2("GET /api/v2/library/{id}/collections/{collection_id}/items", {
        path: { id: String(libraryId), collection_id: collectionId ?? "" },
        query: { limit: 50 },
        signal,
      }).then(
        (data): BrowseResponse => ({
          items: data.items.map(catalogItemFromV2),
          total: data.items.length,
          has_more: data.page?.has_more ?? false,
        }),
      ),
    enabled:
      Number.isFinite(libraryId) &&
      libraryId > 0 &&
      collectionId !== null &&
      collectionId.length > 0,
  });
}
