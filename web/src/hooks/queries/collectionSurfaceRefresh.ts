import type { QueryClient } from "@tanstack/react-query";
import {
  catalogKeys,
  collectionKeys,
  libraryCollectionKeys,
  libraryCollectionTitleKeys,
  sectionKeys,
} from "./keys";

/**
 * A row of collections shows each collection's name, poster and count, so a
 * collection change marks the cached Home and library rows stale. They read
 * again when next shown rather than all at once now.
 */
function markRowsStale(queryClient: QueryClient) {
  return queryClient.invalidateQueries({ queryKey: sectionKeys.all, refetchType: "none" });
}

export async function invalidateUserCollectionQueries(
  queryClient: QueryClient,
  collectionId?: string,
) {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: collectionKeys.all }),
    queryClient.invalidateQueries({ queryKey: catalogKeys.all }),
    markRowsStale(queryClient),
    ...(collectionId
      ? [queryClient.invalidateQueries({ queryKey: collectionKeys.items(collectionId) })]
      : []),
  ]);
}

async function invalidateLibraryCollectionQueries(queryClient: QueryClient) {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: libraryCollectionKeys.all }),
    // After the lists, so a pinned-title read joins an open tab's refetch.
    queryClient.invalidateQueries({ queryKey: libraryCollectionTitleKeys.all }),
    // The Collections page's Server collections list, which the grid there
    // reads beside each library's list.
    queryClient.invalidateQueries({ queryKey: collectionKeys.server() }),
    queryClient.invalidateQueries({ queryKey: catalogKeys.all }),
    markRowsStale(queryClient),
  ]);
}

export async function invalidateAdminCollectionQueries(queryClient: QueryClient) {
  await Promise.all([
    invalidateLibraryCollectionQueries(queryClient),
    queryClient.invalidateQueries({ queryKey: ["admin", "collections"] }),
    queryClient.invalidateQueries({ queryKey: ["admin", "collectionGroups"] }),
  ]);
}
