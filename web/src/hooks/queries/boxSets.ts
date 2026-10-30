import { useQuery } from "@tanstack/react-query";

import type { BoxSetSummary } from "@/api/types";
import { catalogItemFromV2, type CatalogCardItem } from "@/api/v2/catalog";
import { v2 } from "@/api/v2/request";
import { catalogKeys } from "@/hooks/queries/keys";
import { useAvailableUserLibraries } from "@/hooks/queries/libraries";

/** A box set with its movies the viewer can see, in collection order. */
export interface BoxSetDetail extends BoxSetSummary {
  items: CatalogCardItem[];
}

// Box set reads live under the catalog keys, so every invalidation that
// refreshes catalog cards (watched state, a refresh, a scan) refreshes them too.
export const boxSetKeys = {
  all: [...catalogKeys.all, "box-sets"] as const,
  search: (q: string, libraryId?: string) =>
    [...boxSetKeys.all, "search", q, libraryId ?? ""] as const,
  detail: (id: string, libraryId: string) => [...boxSetKeys.all, "detail", id, libraryId] as const,
  item: (contentId: string, libraryId: number | undefined) =>
    [...boxSetKeys.all, "item", contentId, libraryId ?? ""] as const,
};

/** The box sets matching a search, best match first; see searchCatalogBoxSets. */
export async function searchBoxSets(
  q: string,
  libraryId?: string,
  options?: Pick<RequestInit, "signal">,
): Promise<BoxSetSummary[]> {
  const result = await v2("GET /api/v2/catalog/box-sets", {
    query: { q, limit: 5, library_id: libraryId || undefined },
    signal: options?.signal ?? undefined,
  });
  return result.items;
}

/**
 * Box sets for search text, from one library when libraryId is set. Box sets
 * only come from libraries with box sets on, so the read waits until one of
 * the viewer's libraries has them and is skipped otherwise. Callers show none
 * while the read fails, and a failed refresh keeps the box sets already
 * shown. A failure is not retried, so it never holds back "No matches".
 */
export function useBoxSetSearch(
  q: string,
  options: { enabled?: boolean; libraryId?: string } = {},
) {
  const query = q.trim();
  const libraries = useAvailableUserLibraries();
  const hasBoxSetLibrary = (libraries.data ?? []).some(
    (library) =>
      library.box_sets_enabled === true &&
      (!options.libraryId || String(library.id) === options.libraryId),
  );
  return useQuery({
    queryKey: boxSetKeys.search(query, options.libraryId),
    queryFn: ({ signal }) => searchBoxSets(query, options.libraryId, { signal }),
    enabled: (options.enabled ?? true) && hasBoxSetLibrary && query.length > 0,
    staleTime: 60 * 1000,
    retry: false,
  });
}

export async function fetchBoxSet(
  id: string,
  libraryId: string,
  options?: Pick<RequestInit, "signal">,
): Promise<BoxSetDetail> {
  const result = await v2("GET /api/v2/catalog/box-sets/{id}", {
    path: { id },
    query: { library_id: libraryId },
    signal: options?.signal ?? undefined,
  });
  const { items, ...boxSet } = result;
  return { ...boxSet, items: items.map(catalogItemFromV2) };
}

export function useBoxSet(id: string | undefined, libraryId: string | undefined) {
  return useQuery({
    queryKey: boxSetKeys.detail(id ?? "", libraryId ?? ""),
    queryFn: ({ signal }) => fetchBoxSet(id!, libraryId!, { signal }),
    enabled: Boolean(id && libraryId),
  });
}

/** The box set a movie's page shows, or null; see getCatalogItemBoxSet. */
export async function fetchItemBoxSet(
  contentId: string,
  libraryId?: number,
  options?: Pick<RequestInit, "signal">,
): Promise<BoxSetDetail | null> {
  const result = await v2("GET /api/v2/catalog/items/{id}/box-set", {
    path: { id: contentId },
    query: { library_id: libraryId !== undefined ? String(libraryId) : undefined },
    signal: options?.signal ?? undefined,
  });
  if (!result.box_set) return null;
  const { items, ...boxSet } = result.box_set;
  return { ...boxSet, items: items.map(catalogItemFromV2) };
}

/**
 * The box set of the movie a detail page shows. The read is skipped until
 * one of the viewer's libraries has box sets on, and a failure shows no row
 * rather than an error on the movie's page.
 */
export function useItemBoxSet(contentId: string, libraryId?: number) {
  const libraries = useAvailableUserLibraries();
  const hasBoxSetLibrary = (libraries.data ?? []).some(
    (library) => library.box_sets_enabled === true,
  );
  return useQuery({
    queryKey: boxSetKeys.item(contentId, libraryId),
    queryFn: ({ signal }) => fetchItemBoxSet(contentId, libraryId, { signal }),
    enabled: hasBoxSetLibrary && contentId !== "",
    retry: false,
  });
}
