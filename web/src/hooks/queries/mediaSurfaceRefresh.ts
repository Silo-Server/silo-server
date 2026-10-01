import type { QueryClient } from "@tanstack/react-query";
import type { HomeSectionItemsResponse, ItemDetail } from "@/api/types";
import {
  adminKeys,
  catalogKeys,
  favoriteKeys,
  historyKeys,
  itemKeys,
  libraryCollectionKeys,
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
import { bumpHomeRefreshSignal } from "@/pages/homeSurfaceRefresh";

interface InvalidateMediaSurfaceOptions {
  itemId?: string;
  libraryId?: number;
  watchedKeys?: Array<readonly unknown[]>;
  skipItemDetail?: boolean;
  skipSimilarItems?: boolean;
}

const MEDIA_SURFACE_REFRESH_DELAY_MS = 600;
// Upper bound on how long coalescing may postpone a refresh. Without it a
// sustained event stream (a history import, a watch-provider sync) re-arms the
// timer indefinitely and the surfaces never refresh at all.
const MEDIA_SURFACE_REFRESH_MAX_WAIT_MS = 3000;
const MEDIA_SURFACE_PREFIXES = [
  itemKeys.all,
  progressKeys.all,
  historyKeys.all,
  favoriteKeys.all,
  watchlistKeys.all,
  personKeys.all,
  adminKeys.playbackHistory({}).slice(0, 2),
];
const scheduledInvalidations = new WeakMap<
  QueryClient,
  Map<
    string,
    {
      timer: ReturnType<typeof setTimeout>;
      deadline: number;
      options: InvalidateMediaSurfaceOptions;
    }
  >
>();

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

// Callers must await this before writing optimistic state. The default
// `revert: true` restores the pre-fetch snapshot and leaves the query idle;
// `revert: false` would instead put an in-flight query into an error state
// carrying a CancelledError, which surfaces as a bogus "CancelledError" toast.
export function cancelItemDetailQueries(queryClient: QueryClient, itemId: string) {
  return queryClient.cancelQueries({
    predicate: (query) => isItemDetailQueryKey(query.queryKey, itemId),
  });
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

export function isItemDetailQueryKey(queryKey: unknown, itemId: string) {
  return (
    Array.isArray(queryKey) &&
    ((queryKey[0] === "catalog" &&
      queryKey[1] === "items" &&
      queryKey[2] === itemId &&
      queryKey[3] === "detail") ||
      (queryKey[0] === "items" && queryKey[1] === "detail" && queryKey[2] === itemId))
  );
}

function queryKeyStartsWith(queryKey: readonly unknown[], prefix: readonly unknown[]) {
  return (
    prefix.length <= queryKey.length &&
    prefix.every((part, index) => {
      const candidate = queryKey[index];
      if (Object.is(part, candidate)) return true;
      if (
        typeof part !== "object" ||
        part === null ||
        typeof candidate !== "object" ||
        candidate === null
      ) {
        return false;
      }
      return JSON.stringify(part) === JSON.stringify(candidate);
    })
  );
}

function shouldInvalidateMediaSurfaceQuery(
  queryKey: readonly unknown[],
  options: InvalidateMediaSurfaceOptions,
) {
  if (options.skipItemDetail && options.itemId && isItemDetailQueryKey(queryKey, options.itemId)) {
    return false;
  }

  if (queryKeyStartsWith(queryKey, catalogKeys.all)) {
    return activeCatalogQueryMatchesLibrary(queryKey, options.libraryId);
  }
  if (queryKeyStartsWith(queryKey, sectionKeys.all)) {
    return activeSectionQueryMatchesLibrary(queryKey, options.libraryId);
  }
  if (queryKeyStartsWith(queryKey, recKeys.all)) {
    return !(options.skipSimilarItems && queryKey[1] === "similar");
  }
  if (queryKeyStartsWith(queryKey, libraryCollectionKeys.all)) {
    return options.libraryId === undefined || queryKey[2] === options.libraryId;
  }

  if (MEDIA_SURFACE_PREFIXES.some((prefix) => queryKeyStartsWith(queryKey, prefix))) {
    return true;
  }

  return (options.watchedKeys ?? []).some((key) => queryKeyStartsWith(queryKey, key));
}

export async function invalidateMediaSurfaceQueries(
  queryClient: QueryClient,
  options: InvalidateMediaSurfaceOptions = {},
) {
  // Duplicate refetches are held down by the coalescing scheduler below, not by
  // `cancelRefetch: false` — reusing an in-flight request would let a response
  // that predates the mutation satisfy the invalidation and land in the cache
  // as fresh.
  await queryClient.invalidateQueries({
    predicate: (query) => shouldInvalidateMediaSurfaceQuery(query.queryKey, options),
  });
}

export function scheduleMediaSurfaceInvalidation(
  queryClient: QueryClient,
  options: InvalidateMediaSurfaceOptions = {},
) {
  let clientInvalidations = scheduledInvalidations.get(queryClient);
  if (!clientInvalidations) {
    clientInvalidations = new Map();
    scheduledInvalidations.set(queryClient, clientInvalidations);
  }

  const key = `${options.itemId ?? "all"}:${options.libraryId ?? "all"}`;
  const existing = clientInvalidations.get(key);
  if (existing) clearTimeout(existing.timer);

  const mergedOptions: InvalidateMediaSurfaceOptions = {
    ...existing?.options,
    ...options,
    // Skipping is safe only when every coalesced caller has already supplied
    // the canonical detail state. A single full-refresh request must win.
    skipItemDetail: existing
      ? Boolean(existing.options.skipItemDetail && options.skipItemDetail)
      : options.skipItemDetail,
    skipSimilarItems: existing
      ? Boolean(existing.options.skipSimilarItems && options.skipSimilarItems)
      : options.skipSimilarItems,
    watchedKeys: [...(existing?.options.watchedKeys ?? []), ...(options.watchedKeys ?? [])],
  };
  const now = Date.now();
  const deadline = existing?.deadline ?? now + MEDIA_SURFACE_REFRESH_MAX_WAIT_MS;
  const timer = setTimeout(
    () => {
      clientInvalidations?.delete(key);
      // Home reads its sections through one-shot `fetchQuery` calls with no
      // observers, so the signal has to be bumped after the invalidation lands
      // or Home re-reads a cache that is still marked fresh.
      void invalidateMediaSurfaceQueries(queryClient, mergedOptions).then(
        () => bumpHomeRefreshSignal(queryClient),
        () => bumpHomeRefreshSignal(queryClient),
      );
    },
    Math.max(0, Math.min(MEDIA_SURFACE_REFRESH_DELAY_MS, deadline - now)),
  );

  clientInvalidations.set(key, { timer, deadline, options: mergedOptions });
}
