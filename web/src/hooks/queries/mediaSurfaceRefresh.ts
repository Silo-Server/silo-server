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

interface InvalidateMediaSurfaceOptions {
  itemId?: string;
  watchedKeys?: Array<readonly unknown[]>;
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
  const invalidations: Array<Promise<void>> = [
    queryClient.invalidateQueries({ queryKey: itemKeys.all }),
    queryClient.invalidateQueries({ queryKey: catalogKeys.all }),
    queryClient.invalidateQueries({ queryKey: sectionKeys.all }),
    queryClient.invalidateQueries({ queryKey: progressKeys.all }),
    queryClient.invalidateQueries({ queryKey: historyKeys.all }),
    queryClient.invalidateQueries({ queryKey: favoriteKeys.all }),
    queryClient.invalidateQueries({ queryKey: watchlistKeys.all }),
    queryClient.invalidateQueries({ queryKey: recKeys.all }),
    queryClient.invalidateQueries({ queryKey: personKeys.all }),
    queryClient.invalidateQueries({
      predicate: (query) =>
        Array.isArray(query.queryKey) &&
        query.queryKey[0] === adminKeys.playbackHistory({})[0] &&
        query.queryKey[1] === adminKeys.playbackHistory({})[1],
    }),
  ];

  if (options.itemId) {
    invalidations.push(
      queryClient.invalidateQueries({ queryKey: ["catalog", "items", options.itemId, "detail"] }),
      queryClient.invalidateQueries({ queryKey: ["items", "detail", options.itemId] }),
      queryClient.invalidateQueries({ queryKey: ["items", "watchDetail", options.itemId] }),
      queryClient.invalidateQueries({ queryKey: favoriteKeys.check(options.itemId) }),
      queryClient.invalidateQueries({ queryKey: watchlistKeys.check(options.itemId) }),
    );
  }

  for (const key of options.watchedKeys ?? []) {
    invalidations.push(queryClient.invalidateQueries({ queryKey: key }));
  }

  await Promise.all(invalidations);
}
