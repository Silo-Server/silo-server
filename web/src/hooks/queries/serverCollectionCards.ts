import { useState } from "react";
import { useQueries } from "@tanstack/react-query";

import type { LibraryTabCollection, ServerCollectionsLibrary } from "@/api/types";
import { libraryCollectionsQueryOptions } from "@/hooks/queries/libraryCollections";

export interface ServerCollectionCard {
  collection: LibraryTabCollection;
  /** Unset for a collection in several libraries, which opens across all of them. */
  libraryId: number | undefined;
}

/** A library whose rest is on screen from an earlier read, which failed to refresh. */
export interface ServerCollectionsRefreshFailure {
  library: ServerCollectionsLibrary;
  error: unknown;
  retry: () => void;
  retrying: boolean;
}

export interface ServerCollectionCards {
  cards: ServerCollectionCard[];
  /** A requested library's rest is on its way. */
  loading: boolean;
  /** The next library whose rest the default view has not asked for yet. */
  next: ServerCollectionsLibrary | undefined;
  /** Libraries whose full list failed; their first 20 stay. */
  failed: ServerCollectionsLibrary[];
  /** Libraries whose cached rest stays on screen after its refresh failed. */
  refreshFailed: ServerCollectionsRefreshFailure[];
  /** Asks for the next library's rest. */
  loadNext: () => void;
}

/**
 * Every server collection of these libraries, for one grid. The aggregated
 * answer carries only each library's first 20, so the rest come from the
 * libraries' Collections tabs, which list the same collections in the same
 * order. Without loadOnScroll every library's rest loads straight away. With
 * it, one library's rest loads at a time, each time the caller asks through
 * loadNext. Every library's first 20 come before any library's rest.
 */
export function useServerCollectionCards(
  libraries: ServerCollectionsLibrary[],
  loadOnScroll: boolean,
): ServerCollectionCards {
  // The libraries whose rest the default view asked for, one per end of grid.
  const [requested, setRequested] = useState<ReadonlySet<number>>(() => new Set());
  const cutShort = (entry: ServerCollectionsLibrary) =>
    entry.collections.length < entry.total_count;
  const asked = (entry: ServerCollectionsLibrary) =>
    cutShort(entry) && (!loadOnScroll || requested.has(entry.library_id));
  const fullLists = useQueries({
    queries: libraries.map((entry) => {
      const options = libraryCollectionsQueryOptions(entry.library_id);
      return { ...options, enabled: options.enabled && asked(entry) };
    }),
  });
  const lists = libraries.map((entry, index) => {
    // A library not asked for yet shows nothing from the cache, even if its
    // Collections tab left a full list there: a disabled query never
    // refetches, and its rest would land among the rests asked for later.
    const query = asked(entry) ? fullLists[index] : undefined;
    // The first 20 always come from the aggregated answer, which is never
    // older than a cached full list; the full list only adds the rest.
    const full: LibraryTabCollection[] | undefined = query?.data?.collections;
    const inFirst = new Set(entry.collections.map((collection) => collection.id));
    return {
      entry,
      query,
      first: entry.collections,
      rest: full ? full.filter((collection) => !inFirst.has(collection.id)) : [],
      // Still to load: asked for and on its way, or waiting for the next ask.
      loading: query !== undefined && !full && !query.isError && query.fetchStatus !== "idle",
      waiting: cutShort(entry) && query === undefined,
      failed: !full && query?.isError === true,
      // A refetch (on focus, or after an admin change) failed: the cached rest
      // stays, so the failure would otherwise go unseen.
      refreshFailed: query?.isRefetchError === true,
    };
  });
  // Each collection once. A collection in several libraries opens across all
  // of them, so its card carries no library (and so no sidebar pin, which
  // needs one).
  const libraryCount = new Map<string, number>();
  for (const { first, rest } of lists) {
    for (const collection of [...first, ...rest]) {
      libraryCount.set(collection.id, (libraryCount.get(collection.id) ?? 0) + 1);
    }
  }
  const seen = new Set<string>();
  const cards = [
    ...lists.map(({ entry, first }) => ({ entry, collections: first })),
    ...lists.map(({ entry, rest }) => ({ entry, collections: rest })),
  ].flatMap(({ entry, collections }) =>
    collections.flatMap((collection) => {
      if (seen.has(collection.id)) return [];
      seen.add(collection.id);
      const inSeveral = (libraryCount.get(collection.id) ?? 0) > 1;
      return [{ collection, libraryId: inSeveral ? undefined : entry.library_id }];
    }),
  );
  const next = lists.find((list) => list.waiting)?.entry;
  const loadNext = () => {
    if (next) setRequested((asked) => new Set(asked).add(next.library_id));
  };
  return {
    cards,
    loading: lists.some((list) => list.loading),
    next,
    failed: lists.filter((list) => list.failed).map(({ entry }) => entry),
    refreshFailed: lists.flatMap(({ entry, query, refreshFailed }) =>
      refreshFailed && query
        ? [
            {
              library: entry,
              error: query.error,
              retry: () => void query.refetch(),
              retrying: query.fetchStatus === "fetching",
            },
          ]
        : [],
    ),
    loadNext,
  };
}
