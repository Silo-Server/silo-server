import { isCancelledError, useQuery, type QueryClient } from "@tanstack/react-query";
import type { BrowseResponse, LibraryCollection, LibraryTabResponse } from "@/api/types";
import { catalogItemFromV2, libraryCollectionTabFromV2 } from "@/api/v2/catalog";
import { v2 } from "@/api/v2/request";
import { libraryCollectionKeys } from "./keys";

export function libraryCollectionsQueryOptions(libraryId: number) {
  return {
    queryKey: libraryCollectionKeys.list(libraryId),
    queryFn: ({ signal }: { signal?: AbortSignal }): Promise<LibraryTabResponse> =>
      v2("GET /api/v2/library/{id}/collections", { path: { id: String(libraryId) }, signal }).then(
        libraryCollectionTabFromV2,
      ),
    enabled: Number.isFinite(libraryId) && libraryId > 0,
  };
}

const COLLECTION_TITLES_STALE_MS = 5 * 60 * 1000;

/**
 * The titles of a library's collections by ID, for naming pinned collections.
 * A media change never renames a collection, so media invalidations leave this
 * query alone (see mediaSurfaceRefresh); collection edits invalidate it with
 * the rest of `libraryCollectionKeys`. It reads through the Collections tab's
 * query, so a tab read in flight or still fresh answers it without a request.
 */
export function libraryCollectionTitlesQuery(libraryId: number) {
  return {
    queryKey: libraryCollectionKeys.titles(libraryId),
    queryFn: ({
      client,
      signal,
    }: {
      client: QueryClient;
      signal: AbortSignal;
    }): Promise<ReadonlyMap<string, string>> => {
      const tabQuery = libraryCollectionsQueryOptions(libraryId);
      return client
        .fetchQuery({ ...tabQuery, staleTime: COLLECTION_TITLES_STALE_MS })
        .catch((error: unknown) => {
          // The tab's request is cancelled when the tab closes before it lands;
          // the titles still need an answer, so read them on their own.
          if (isCancelledError(error)) return tabQuery.queryFn({ signal });
          throw error;
        })
        .then(
          (tab) =>
            new Map(
              getLibraryCollectionList(tab).map((collection) => [collection.id, collection.title]),
            ),
        );
    },
    staleTime: COLLECTION_TITLES_STALE_MS,
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
