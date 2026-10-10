/**
 * The values a rule can pick for a facet (genre, studio, …), searched in the
 * rule's own libraries and kind of titles.
 */
import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";

import { catalogFiltersFromV2 } from "@/api/v2/catalog";
import { v2 } from "@/api/v2/request";

import type { CatalogFacetName } from "./catalog";
import { fetchPeopleSearchCapabilities } from "./personSearch";

/** Where a picker looks: the rule's libraries (none: all of the viewer's) and kind of titles. */
export interface FacetValueScope {
  libraryIds?: readonly number[];
  /** A query media scope ("movie", "video", …); "all" or none takes every kind. */
  mediaScope?: string;
}

export interface FacetValues {
  /** Values in the server's order; `count` is the titles that carry one, when the server says. */
  values: Array<{ value: string; count?: number }>;
  hasMore: boolean;
}

export const FACET_VALUE_LIMIT = 50;

export function fetchFacetValues(
  facet: CatalogFacetName,
  q: string,
  scope: FacetValueScope,
  options: { limit?: number; signal?: AbortSignal } = {},
) {
  const libraryIds = scope.libraryIds ?? [];
  return v2("GET /api/v2/catalog/filters/search", {
    query: {
      source: "query",
      facet,
      q,
      limit: options.limit ?? FACET_VALUE_LIMIT,
      type: scope.mediaScope && scope.mediaScope !== "all" ? scope.mediaScope : undefined,
      library_ids: libraryIds.length > 0 ? libraryIds.map(String) : undefined,
    },
    signal: options.signal,
  });
}

/**
 * Searches a facet's values as the user types. A server that advertises
 * `facet_value_search` answers an empty `q` with the most common values and
 * ranks matches by title count; an older one only searches typed text by
 * prefix, so with nothing typed the answer is `null`. The last answer stays
 * on screen while the next loads.
 */
export function useFacetValues(facet: CatalogFacetName, q: string, scope: FacetValueScope) {
  const queryClient = useQueryClient();
  const libraryIds = [...(scope.libraryIds ?? [])].sort((a, b) => a - b);
  const mediaScope = scope.mediaScope && scope.mediaScope !== "all" ? scope.mediaScope : "all";
  return useQuery({
    queryKey: ["catalog", "facetValues", facet, q, { libraryIds, mediaScope }] as const,
    queryFn: async ({ signal }): Promise<FacetValues | null> => {
      const capabilities = await fetchPeopleSearchCapabilities(queryClient).catch(() => null);
      const ranked = capabilities?.facet_value_search === true;
      if (!ranked && q === "") return null;
      // An older server may not take library_ids; it searches the whole kind instead.
      const answer = await fetchFacetValues(
        facet,
        q,
        ranked ? { libraryIds, mediaScope } : { mediaScope },
        { signal },
      );
      // An older server answers names only, in matches.
      return ranked
        ? { values: answer.values, hasMore: answer.values_has_more }
        : { values: answer.matches.map((value) => ({ value })), hasMore: answer.has_more };
    },
    placeholderData: keepPreviousData,
    staleTime: 60 * 1000,
  });
}

/**
 * The catalog filter lists for the titles in a rule's libraries and kind of
 * titles, which a language picker reads its languages from. Audio and
 * subtitle languages come from files, so only `includeTechnical` asks for them.
 */
export function useRuleLanguages(
  scope: FacetValueScope,
  { includeTechnical }: { includeTechnical: boolean },
) {
  const queryClient = useQueryClient();
  const libraryIds = [...(scope.libraryIds ?? [])].sort((a, b) => a - b);
  const type = scope.mediaScope && scope.mediaScope !== "all" ? scope.mediaScope : undefined;
  return useQuery({
    queryKey: ["catalog", "ruleLanguages", { libraryIds, type, includeTechnical }] as const,
    queryFn: async ({ signal }) => {
      // A server without facet_value_search refuses library_ids, so it is
      // asked for the whole kind instead. A failed capability request fails
      // the load, so the picker asks again rather than keeping that list.
      const capabilities = await fetchPeopleSearchCapabilities(queryClient);
      const byLibrary = capabilities.facet_value_search === true && libraryIds.length > 0;
      const filters = await v2("GET /api/v2/catalog/filters", {
        query: {
          source: "query",
          type,
          library_ids: byLibrary ? libraryIds.map(String) : undefined,
          skip_technical: includeTechnical ? undefined : true,
        },
        signal,
      });
      return catalogFiltersFromV2(filters);
    },
    staleTime: 5 * 60 * 1000,
  });
}
