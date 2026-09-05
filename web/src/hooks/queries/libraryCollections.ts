import { useQuery } from "@tanstack/react-query";
import type {
  BrowseItem,
  LibraryCollection,
  LibraryTabCollection,
  LibraryTabResponse,
  ServerVisibleUserCollection,
} from "@/api/types";
import {
  catalogItemFromV2,
  libraryCollectionTabFromV2,
  userCollectionFromV2,
} from "@/api/v2/catalog";
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

export function flattenLibraryCollections(
  resp: LibraryTabResponse | undefined,
): LibraryTabCollection[] {
  if (!resp) return [];
  const out: LibraryTabCollection[] = [];
  for (const group of resp.groups ?? []) {
    out.push(...(group.collections ?? []));
  }
  out.push(...(resp.ungrouped?.collections ?? []));
  return out;
}

export function useLibraryUserCollections(libraryId: number) {
  return useQuery({
    queryKey: libraryCollectionKeys.userContributed(libraryId),
    queryFn: ({ signal }): Promise<ServerVisibleUserCollection[]> =>
      v2("GET /api/v2/library/{id}/user-collections", {
        path: { id: String(libraryId) },
        signal,
      }).then((data) => data.items.map(userCollectionFromV2)),
    enabled: Number.isFinite(libraryId) && libraryId > 0,
  });
}

export function getLibraryCollectionList(
  resp: LibraryTabResponse | undefined,
): LibraryCollection[] {
  return resp?.collections ?? [];
}

// The endpoint pages by cursor (default 50, max 200); this hook reads the
// first page only for now, so the collection page shows at most the default
// page size until it grows a cursor-following loader.
export function useLibraryCollectionItems(libraryId: number, collectionId: string | null) {
  return useQuery({
    queryKey: libraryCollectionKeys.items(libraryId, collectionId ?? ""),
    queryFn: ({ signal }): Promise<BrowseItem[]> =>
      v2("GET /api/v2/library/{id}/collections/{collection_id}/items", {
        path: { id: String(libraryId), collection_id: collectionId ?? "" },
        signal,
      }).then((data) => data.items.map(catalogItemFromV2)),
    enabled:
      Number.isFinite(libraryId) &&
      libraryId > 0 &&
      collectionId !== null &&
      collectionId.length > 0,
  });
}
