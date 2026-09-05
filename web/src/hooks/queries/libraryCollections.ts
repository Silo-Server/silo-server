import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
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

/** Page size of a collection's items: the endpoint's default. */
export const LIBRARY_COLLECTION_ITEMS_PAGE_LIMIT = 50;

export interface LibraryCollectionItemsPage {
  items: BrowseItem[];
  /** Cursor of the next page, or undefined on the last page. */
  nextCursor: string | undefined;
}

export async function fetchLibraryCollectionItemsPage(
  libraryId: number,
  collectionId: string,
  cursor?: string,
  signal?: AbortSignal,
): Promise<LibraryCollectionItemsPage> {
  const page = await v2("GET /api/v2/library/{id}/collections/{collection_id}/items", {
    path: { id: String(libraryId), collection_id: collectionId },
    query: {
      limit: LIBRARY_COLLECTION_ITEMS_PAGE_LIMIT,
      ...(cursor === undefined ? {} : { cursor }),
    },
    signal,
  });
  return {
    items: page.items.map(catalogItemFromV2),
    nextCursor: page.page?.has_more && page.page.next_cursor ? page.page.next_cursor : undefined,
  };
}

/**
 * Pages a collection's items by cursor. A page can come back with no items
 * while `has_more` is still set (access filtering emptied that window), so a
 * caller that needs the first visible item keeps fetching while
 * `hasNextPage` holds.
 */
export function useLibraryCollectionItems(libraryId: number, collectionId: string | null) {
  return useInfiniteQuery({
    queryKey: libraryCollectionKeys.items(libraryId, collectionId ?? ""),
    queryFn: ({ pageParam, signal }) =>
      fetchLibraryCollectionItemsPage(libraryId, collectionId ?? "", pageParam, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.nextCursor,
    enabled:
      Number.isFinite(libraryId) &&
      libraryId > 0 &&
      collectionId !== null &&
      collectionId.length > 0,
  });
}

/** Flattens the loaded pages of useLibraryCollectionItems into one list. */
export function flattenLibraryCollectionItems(
  data: { pages: LibraryCollectionItemsPage[] } | undefined,
): BrowseItem[] {
  return data?.pages.flatMap((page) => page.items) ?? [];
}
