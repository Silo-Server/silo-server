import type { QueryClient } from "@tanstack/react-query";
import type { HomeSectionItemsResponse, ItemDetail } from "@/api/types";
import {
  adminKeys,
  catalogKeys,
  favoriteKeys,
  historyKeys,
  itemKeys,
  personKeys,
  progressKeys,
  recKeys,
  sectionKeys,
  watchlistKeys,
} from "./keys";
import {
  activeCatalogQueryMatchesLibrary,
  activeSectionQueryMatchesLibrary,
} from "@/lib/queryInvalidation";

interface InvalidateMediaSurfaceOptions {
  itemId?: string;
  libraryId?: number;
  watchedKeys?: Array<readonly unknown[]>;
  refetchActive?: boolean;
}

export function updateCatalogItemDetail(
  queryClient: QueryClient,
  itemId: string,
  updater: (detail: ItemDetail) => ItemDetail,
) {
  queryClient.setQueriesData<ItemDetail>(
    {
      predicate: (query) => isItemDetailQueryKey(query.queryKey, itemId),
    },
    (current) => (current ? updater(current) : current),
  );
}

export function setCachedItemDetail(queryClient: QueryClient, itemId: string, detail: ItemDetail) {
  queryClient.setQueriesData<ItemDetail>(
    {
      predicate: (query) => isItemDetailQueryKey(query.queryKey, itemId),
    },
    detail,
  );
}

export function removeItemFromHomeSectionCaches(
  queryClient: QueryClient,
  itemId: string,
  sectionType?: string,
) {
  queryClient.setQueriesData<HomeSectionItemsResponse>(
    {
      predicate: (query) =>
        Array.isArray(query.queryKey) &&
        query.queryKey[0] === sectionKeys.homeItemsRoot()[0] &&
        query.queryKey[1] === sectionKeys.homeItemsRoot()[1] &&
        query.queryKey[2] === sectionKeys.homeItemsRoot()[2],
    },
    (current) => {
      if (!current?.section) {
        return current;
      }
      if (sectionType && current.section.section_type !== sectionType) {
        return current;
      }
      const nextItems = current.section.items.filter((item) => item.content_id !== itemId);
      if (nextItems.length === current.section.items.length) {
        return current;
      }
      return {
        ...current,
        section: {
          ...current.section,
          total_count: Math.max(
            0,
            current.section.total_count - (current.section.items.length - nextItems.length),
          ),
          items: nextItems,
        },
      };
    },
  );
}

function isItemDetailQueryKey(queryKey: unknown, itemId: string) {
  return (
    Array.isArray(queryKey) &&
    ((queryKey[0] === "catalog" &&
      queryKey[1] === "items" &&
      queryKey[2] === itemId &&
      queryKey[3] === "detail") ||
      (queryKey[0] === "items" && queryKey[1] === "detail" && queryKey[2] === itemId))
  );
}

export async function invalidateMediaSurfaceQueries(
  queryClient: QueryClient,
  options: InvalidateMediaSurfaceOptions = {},
) {
  const invalidate = (filters: Parameters<QueryClient["invalidateQueries"]>[0]) =>
    queryClient.invalidateQueries(
      options.refetchActive === false ? { ...filters, refetchType: "none" } : filters,
    );

  const invalidations: Array<Promise<void>> = [
    invalidate({ queryKey: itemKeys.all }),
    invalidate({
      queryKey: catalogKeys.all,
      predicate: (query) => activeCatalogQueryMatchesLibrary(query.queryKey, options.libraryId),
    }),
    invalidate({
      queryKey: sectionKeys.all,
      predicate: (query) => activeSectionQueryMatchesLibrary(query.queryKey, options.libraryId),
    }),
    invalidate({ queryKey: progressKeys.all }),
    invalidate({ queryKey: historyKeys.all }),
    invalidate({ queryKey: favoriteKeys.all }),
    invalidate({ queryKey: watchlistKeys.all }),
    invalidate({ queryKey: recKeys.all }),
    invalidate({ queryKey: personKeys.all }),
    invalidate({
      predicate: (query) =>
        Array.isArray(query.queryKey) &&
        query.queryKey[0] === adminKeys.playbackHistory({})[0] &&
        query.queryKey[1] === adminKeys.playbackHistory({})[1],
    }),
  ];

  if (options.itemId) {
    invalidations.push(
      invalidate({ queryKey: ["catalog", "items", options.itemId, "detail"] }),
      invalidate({ queryKey: ["items", "detail", options.itemId] }),
      invalidate({ queryKey: ["items", "watchDetail", options.itemId] }),
      invalidate({ queryKey: favoriteKeys.check(options.itemId) }),
      invalidate({ queryKey: watchlistKeys.check(options.itemId) }),
    );
  }

  for (const key of options.watchedKeys ?? []) {
    invalidations.push(invalidate({ queryKey: key }));
  }

  await Promise.all(invalidations);
}
