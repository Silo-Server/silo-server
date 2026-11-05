import { useQueries } from "@tanstack/react-query";

import type { ServerCollectionsLibrary } from "@/api/types";
import { useScopeSnapshot } from "@/hooks/queries/collectionScope";
import { useServerCollections } from "@/hooks/queries/collections";
import { libraryCollectionsQueryOptions } from "@/hooks/queries/libraryCollections";
import type { CollectionPageTarget } from "@/hooks/useCollectionPageAccess";
import { PERSONAL_SCOPE } from "@/lib/collections/scope";

/**
 * What a collection page shows in its header besides the name. `pending`
 * while that is still being read, so the header doesn't show the generic
 * subtitle and then swap in the description.
 */
export type CollectionPageHeader =
  | { status: "pending" }
  | {
      status: "ready";
      description?: string;
      backdropUrl?: string;
      backdropThumbhash?: string;
    };

const PENDING: CollectionPageHeader = { status: "pending" };
const EMPTY: CollectionPageHeader = { status: "ready" };

/**
 * The libraries whose Collections tab may list server collection `id`: the
 * one the page was opened in; else the first the server-collections list
 * shows it under; else, when it is past a library's teaser cap there, every
 * library whose teaser was cut short. `undefined` while the list is loading.
 */
export function serverCollectionLibraryIds(
  id: string,
  libraryId: number | undefined,
  libraries: readonly ServerCollectionsLibrary[] | undefined,
): number[] | undefined {
  if (libraryId && libraryId > 0) return [libraryId];
  if (!libraries) return undefined;
  const listing = libraries.find((library) => library.collections.some((c) => c.id === id));
  if (listing) return [listing.library_id];
  return libraries
    .filter((library) => library.total_count > library.collections.length)
    .map((library) => library.library_id);
}

/**
 * The description and backdrop of the collection a catalog page shows. A
 * server collection reads them from its library's Collections tab, the query
 * that tab already makes; a personal collection, which has no backdrop, from
 * the snapshot the page's actions already load.
 */
export function useCollectionPageHeader(target: CollectionPageTarget | null): CollectionPageHeader {
  const isServer = target?.scope === "server";
  const openedInLibrary = Boolean(target?.libraryId && target.libraryId > 0);
  const serverList = useServerCollections({ enabled: isServer && !openedInLibrary });
  const libraryIds =
    isServer && target
      ? serverCollectionLibraryIds(
          target.id,
          target.libraryId,
          // A first load that failed reads as no library, so the page stops
          // waiting; a failed refetch keeps the list it already has.
          serverList.data ?? (serverList.isError ? [] : undefined),
        )
      : [];
  const libraryLists = useQueries({
    queries: (libraryIds ?? []).map((libraryId) => libraryCollectionsQueryOptions(libraryId)),
  });
  const personal = useScopeSnapshot(PERSONAL_SCOPE, target?.id, {
    enabled: target?.scope === "personal",
  });

  if (!target) return EMPTY;
  if (target.scope === "personal") {
    if (personal.isPending) return PENDING;
    const description = personal.data?.view.description.trim();
    return description ? { status: "ready", description } : EMPTY;
  }
  if (!libraryIds) return PENDING;
  for (const list of libraryLists) {
    const found = list.data?.collections?.find((c) => c.id === target.id);
    if (found) {
      return {
        status: "ready",
        description: found.description?.trim() || undefined,
        backdropUrl: found.backdrop_url || undefined,
        backdropThumbhash: found.backdrop_thumbhash || undefined,
      };
    }
  }
  return libraryLists.some((list) => list.isPending) ? PENDING : EMPTY;
}
