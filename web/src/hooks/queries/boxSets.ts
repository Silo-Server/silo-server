import { useQuery } from "@tanstack/react-query";

import type { BoxSetSummary } from "@/api/types";
import { catalogItemFromV2, type CatalogCardItem } from "@/api/v2/catalog";
import { v2 } from "@/api/v2/request";
import { catalogKeys } from "@/hooks/queries/keys";

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
 * only come from libraries with box sets in search, so most servers answer
 * with none. Callers show none while the read fails, and a failed refresh
 * keeps the box sets already shown.
 */
export function useBoxSetSearch(
  q: string,
  options: { enabled?: boolean; libraryId?: string } = {},
) {
  const query = q.trim();
  return useQuery({
    queryKey: boxSetKeys.search(query, options.libraryId),
    queryFn: ({ signal }) => searchBoxSets(query, options.libraryId, { signal }),
    enabled: (options.enabled ?? true) && query.length > 0,
    staleTime: 60 * 1000,
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
