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
  search: (q: string) => [...boxSetKeys.all, "search", q] as const,
  detail: (id: string, libraryId: string) => [...boxSetKeys.all, "detail", id, libraryId] as const,
};

/** The box sets matching a search, best match first; see searchCatalogBoxSets. */
export async function searchBoxSets(
  q: string,
  options?: Pick<RequestInit, "signal">,
): Promise<BoxSetSummary[]> {
  const result = await v2("GET /api/v2/catalog/box-sets", {
    query: { q, limit: 5 },
    signal: options?.signal ?? undefined,
  });
  return result.items;
}

/**
 * Box sets for search text. Box sets only come from libraries with box sets
 * in search, so most servers answer with none; a failed read shows none
 * rather than failing the search around it.
 */
export function useBoxSetSearch(q: string, options: { enabled?: boolean } = {}) {
  const query = q.trim();
  return useQuery({
    queryKey: boxSetKeys.search(query),
    queryFn: ({ signal }) => searchBoxSets(query, { signal }).catch(() => []),
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
