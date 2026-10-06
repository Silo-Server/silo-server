import { useQuery } from "@tanstack/react-query";
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
