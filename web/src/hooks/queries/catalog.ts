import { useEffect, useMemo, useState } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";

import type { CatalogFiltersResponse, CatalogResponse } from "@/api/types";
import { catalogFiltersFromV2, catalogItemFromV2 } from "@/api/v2/catalog";
import { v2, type V2Body, type V2Query, type V2Result } from "@/api/v2/request";
import type { CatalogParams } from "@/hooks/queries/keys";
import { catalogKeys } from "@/hooks/queries/keys";
import { createEmptyQueryDefinition, type CatalogSource } from "@/api/types";
import {
  buildCatalogApiSearchParams,
  catalogSourceAllowsOverlay,
  type CatalogSearchState,
} from "@/pages/catalogSearchParams";

// Search-as-you-type creates a distinct query key for every settled input.
// Keep enough history for a quick correction/backspace without retaining ten
// minutes of poster-heavy responses, and never automatically replay a timed
// out interactive search against PostgreSQL.
const INTERACTIVE_SEARCH_GC_TIME_MS = 30_000;

function catalogParamsForKey(
  state: CatalogSearchState,
  limit: number,
  includeTotal: boolean,
): CatalogParams {
  return {
    source: state.source,
    q: state.q,
    title: state.title,
    scope: state.scope,
    section_id: state.section_id,
    library_id: state.library_id,
    collection_id: state.collection_id,
    person_id: state.person_id,
    type: state.type_override ?? state.query_definition.media_scope,
    uses_source_order: state.uses_source_order,
    query_fingerprint: JSON.stringify([state.query_definition, state.sort_from_server]),
    include_total: includeTotal,
    limit,
  };
}

const MAX_CATALOG_SEEK = 10_000_000;

type CatalogScopeQuery = V2Query<"GET /api/v2/catalog/filters">;

/**
 * The scope a facet document is computed over: the same source and ids the
 * browse sends, minus the overlay (search text, sort, rule groups) that the
 * facet endpoints ignore. Derived from the browse parameters so the two stay
 * in step.
 */
function catalogScopeQuery(state: CatalogSearchState): CatalogScopeQuery {
  const params = buildCatalogApiSearchParams(state);
  const source = params.get("source") as CatalogScopeQuery["source"];
  const scope = params.get("scope") as CatalogScopeQuery["scope"];
  return {
    source: source ?? undefined,
    scope: scope ?? undefined,
    section_id: params.get("section_id") ?? undefined,
    library_id: params.get("library_id") ?? undefined,
    collection_id: params.get("collection_id") ?? undefined,
    person_id: params.get("person_id") ?? undefined,
    type: params.get("type") ?? undefined,
  };
}

export interface CatalogPage extends CatalogResponse {
  search_diagnostics?: V2Result<"POST /api/v2/catalog/query">["search_diagnostics"];
}

export async function fetchCatalogPage(
  state: CatalogSearchState,
  limit: number,
  offset: number,
  options?: RequestInit,
  includeTotal = true,
  snapshot?: string,
): Promise<CatalogPage> {
  if (!Number.isInteger(offset) || offset < 0 || offset > MAX_CATALOG_SEEK) {
    throw new RangeError("Narrow your filters or search to browse beyond this result window.");
  }
  const params = buildCatalogApiSearchParams(state);
  const overlay = catalogSourceAllowsOverlay(state.source);
  const body: V2Body<"POST /api/v2/catalog/query"> = {
    ...catalogScopeQuery(state),
    match: overlay ? state.query_definition.match : undefined,
    groups: overlay ? state.query_definition.groups : undefined,
    sort: params.get("sort") ?? undefined,
    order: (params.get("order") as "asc" | "desc" | null) ?? undefined,
    q: params.get("q") ?? undefined,
    query_limit: params.has("query_limit") ? Number(params.get("query_limit")) : undefined,
    limit,
    skip_total: includeTotal ? undefined : true,
    cursor: snapshot,
    seek: offset > 0 || snapshot !== undefined ? offset : undefined,
  };
  const result = await v2("POST /api/v2/catalog/query", {
    body,
    signal: options?.signal ?? undefined,
  });
  return {
    items: result.items.map(catalogItemFromV2),
    total: result.total,
    total_exact: result.total_exact,
    search_diagnostics: result.search_diagnostics,
    has_more: result.page?.has_more ?? false,
    snapshot: result.window_cursor,
    title: state.title,
    effective_sort: result.effective_sort
      ? {
          ...result.effective_sort,
          field: result.effective_sort.field as NonNullable<
            CatalogResponse["effective_sort"]
          >["field"],
          order: result.effective_sort.order as "asc" | "desc",
        }
      : undefined,
  };
}

export async function fetchCatalogFilters(
  state: CatalogSearchState,
  options?: Pick<RequestInit, "signal">,
  requestOptions: { includeTechnical?: boolean } = {},
): Promise<CatalogFiltersResponse> {
  const filters = await v2("GET /api/v2/catalog/filters", {
    query: {
      ...catalogScopeQuery(state),
      skip_technical: requestOptions.includeTechnical === false ? true : undefined,
    },
    signal: options?.signal ?? undefined,
  });
  return catalogFiltersFromV2(filters);
}

export type CatalogFacetName =
  | "genre"
  | "studio"
  | "network"
  | "country"
  | "original_language"
  | "content_rating"
  | "author"
  | "narrator"
  | "series";

export interface CatalogFacetSearchResponse {
  matches: string[];
  has_more: boolean;
}

export async function fetchCatalogFacetSearch(
  state: CatalogSearchState,
  facet: CatalogFacetName,
  prefix: string,
  limit: number,
  options?: Pick<RequestInit, "signal">,
): Promise<CatalogFacetSearchResponse> {
  return v2("GET /api/v2/catalog/filters/search", {
    query: { ...catalogScopeQuery(state), facet, q: prefix, limit },
    signal: options?.signal ?? undefined,
  });
}

export function createCatalogSearchState(
  source: CatalogSource,
  patch: Partial<CatalogSearchState> = {},
): CatalogSearchState {
  return {
    source,
    query_definition: createEmptyQueryDefinition(),
    ...patch,
  };
}

export function useCatalogWindow(
  state: CatalogSearchState,
  options: {
    limit?: number;
    visibleRange?: [number, number];
    includeTotal?: boolean;
    enabled?: boolean;
  } = {},
) {
  const limit = options.limit ?? 60;
  const includeTotal = options.includeTotal ?? true;
  const enabled = options.enabled ?? true;
  const isInteractiveSearch = state.source === "query" && Boolean(state.q);
  const page0Params = catalogParamsForKey(state, limit, includeTotal);
  const remainingPageParams = catalogParamsForKey(state, limit, false);
  const visibleRange = options.visibleRange ?? [0, limit - 1];
  const bufferPages = state.source === "query" && state.q ? 0 : 1;
  const visibleStartPage = Math.max(0, Math.floor(visibleRange[0] / limit));
  const visibleEndPage = Math.floor(visibleRange[1] / limit);
  const startPage = Math.max(0, visibleStartPage - bufferPages);
  const endPage = visibleEndPage + bufferPages;

  // Fetch page 0 separately so its snapshot timestamp is available
  // synchronously for subsequent page queries, preventing duplicate items
  // when new items are added between page fetches (e.g. during a scan).
  const page0Result = useQuery({
    queryKey: catalogKeys.list({ ...page0Params, limit, offset: 0 }),
    queryFn: ({ signal }: { signal: AbortSignal }) =>
      fetchCatalogPage(state, limit, 0, { signal }, includeTotal),
    staleTime: 10 * 60 * 1000,
    ...(isInteractiveSearch
      ? { gcTime: INTERACTIVE_SEARCH_GC_TIME_MS, retry: false as const }
      : {}),
    enabled,
    placeholderData: isInteractiveSearch ? (previousData) => previousData : undefined,
  });

  const snapshot = page0Result.data?.snapshot;
  const canFetchRemainingPages =
    enabled && page0Result.data !== undefined && !page0Result.isPlaceholderData;

  const lastKnownPage =
    page0Result.data?.total_exact === true
      ? Math.max(0, Math.ceil(page0Result.data.total / limit) - 1)
      : undefined;
  const remainingPageIndices = useMemo(() => {
    const indices = new Set<number>();
    for (let page = startPage; page <= Math.min(endPage, lastKnownPage ?? endPage); page++) {
      if (page > 0) {
        indices.add(page);
      }
    }
    return Array.from(indices).sort((a, b) => a - b);
  }, [endPage, startPage, lastKnownPage]);

  const remainingResults = useQueries({
    queries: remainingPageIndices.map((pageIndex) => {
      const offset = pageIndex * limit;
      return {
        queryKey: catalogKeys.list({
          ...remainingPageParams,
          limit,
          offset,
          snapshot,
        }),
        queryFn: ({ signal }: { signal: AbortSignal }) =>
          fetchCatalogPage(state, limit, offset, { signal }, false, snapshot),
        staleTime: 10 * 60 * 1000,
        ...(isInteractiveSearch
          ? { gcTime: INTERACTIVE_SEARCH_GC_TIME_MS, retry: false as const }
          : {}),
        enabled: canFetchRemainingPages,
      };
    }),
  });

  const title = page0Result.data?.title ?? state.title;
  const isLoading = page0Result.isLoading;
  const failedVisibleResults = remainingResults.filter((result, queryIndex) => {
    const pageIndex = remainingPageIndices[queryIndex];
    return (
      pageIndex !== undefined &&
      pageIndex >= visibleStartPage &&
      pageIndex <= visibleEndPage &&
      result.isError
    );
  });
  const remainingError = failedVisibleResults[0]?.error;

  const pageResults = useMemo(() => {
    const map = new Map<number, CatalogResponse>();
    if (page0Result.data) {
      map.set(0, page0Result.data);
    }
    remainingPageIndices.forEach((pageIndex, queryIndex) => {
      const data = remainingResults[queryIndex]?.data;
      if (data) {
        map.set(pageIndex, data);
      }
    });
    return map;
  }, [page0Result.data, remainingPageIndices, remainingResults]);

  const pages = useMemo(() => {
    const map = new Map<number, CatalogResponse["items"]>();
    pageResults.forEach((page, pageIndex) => {
      map.set(pageIndex, page.items);
    });
    return map;
  }, [pageResults]);

  const estimateKey = JSON.stringify({
    source: state.source,
    q: state.q,
    title: state.title,
    scope: state.scope,
    section_id: state.section_id,
    library_id: state.library_id,
    collection_id: state.collection_id,
    person_id: state.person_id,
    type: state.type_override ?? state.query_definition.media_scope,
    query_fingerprint: JSON.stringify([state.query_definition, state.sort_from_server]),
    limit,
  });

  const nonExactPageStats = useMemo(() => {
    let maxLoadedEnd = 0;
    let highestPageIndex = -1;
    let highestPageHasMore = false;
    pageResults.forEach((page, pageIndex) => {
      if (page.items.length > 0) {
        maxLoadedEnd = Math.max(maxLoadedEnd, pageIndex * limit + page.items.length);
      }
      if (pageIndex >= highestPageIndex) {
        highestPageIndex = pageIndex;
        highestPageHasMore = page.has_more;
      }
    });
    return { maxLoadedEnd, highestPageHasMore };
  }, [limit, pageResults]);

  const initialEstimatedTotalItems = useMemo(() => {
    if (page0Result.data?.total_exact !== false) {
      return page0Result.data?.total ?? 0;
    }
    if (nonExactPageStats.maxLoadedEnd === 0) {
      return 0;
    }
    return nonExactPageStats.maxLoadedEnd + (nonExactPageStats.highestPageHasMore ? limit * 5 : 0);
  }, [
    limit,
    nonExactPageStats.highestPageHasMore,
    nonExactPageStats.maxLoadedEnd,
    page0Result.data?.total,
    page0Result.data?.total_exact,
  ]);

  const [estimatedTotalItems, setEstimatedTotalItems] = useState(0);

  useEffect(() => {
    setEstimatedTotalItems(0);
  }, [estimateKey]);

  useEffect(() => {
    const totalExact = page0Result.data?.total_exact !== false;
    if (totalExact) {
      setEstimatedTotalItems(page0Result.data?.total ?? 0);
      return;
    }

    if (nonExactPageStats.maxLoadedEnd === 0) {
      return;
    }

    const estimateStep = limit * 5;
    setEstimatedTotalItems((current) => {
      if (!nonExactPageStats.highestPageHasMore) {
        return nonExactPageStats.maxLoadedEnd;
      }

      const seededEstimate = current > 0 ? current : nonExactPageStats.maxLoadedEnd + estimateStep;
      const needsMoreRunway = visibleRange[1] >= seededEstimate - limit * 2;
      const expandedEstimate = needsMoreRunway ? seededEstimate + estimateStep : seededEstimate;

      return Math.max(expandedEstimate, nonExactPageStats.maxLoadedEnd);
    });
  }, [
    limit,
    nonExactPageStats.highestPageHasMore,
    nonExactPageStats.maxLoadedEnd,
    page0Result.data?.total,
    page0Result.data?.total_exact,
    visibleRange,
  ]);

  const totalItems =
    page0Result.data?.total_exact !== false
      ? (page0Result.data?.total ?? 0)
      : Math.max(estimatedTotalItems, initialEstimatedTotalItems);

  return {
    data: {
      title,
      totalItems,
      pages,
      // Collection sources report the order they actually resolved in, after
      // the viewer's saved override and the collection's configured default.
      // Absent means the collection kept its own source order.
      effectiveSort: page0Result.data?.effective_sort,
    },
    isLoading,
    isError: page0Result.isError || failedVisibleResults.length > 0,
    isPlaceholderData: page0Result.isPlaceholderData,
    error: page0Result.error ?? remainingError,
    // Only the first page speaks for the source itself; a later page failing
    // says nothing about whether the collection or section exists.
    sourceError: page0Result.error,
    refetch: async () => {
      // Page 0 owns the snapshot and total, so refresh it together with every
      // failed visible page. Retrying only page 0 leaves a timed-out later page
      // missing from the grid and immediately recreates the same partial view.
      await Promise.all([
        page0Result.refetch(),
        ...failedVisibleResults.map((result) => result.refetch()),
      ]);
    },
  };
}

export function useCatalogFilters(
  state: CatalogSearchState,
  options: { enabled?: boolean; includeTechnical?: boolean } = {},
) {
  const params = catalogParamsForKey(state, 0, true);
  const enabled = options.enabled ?? true;
  const includeTechnical = options.includeTechnical ?? true;

  return useQuery({
    queryKey: catalogKeys.filters({
      source: params.source,
      q: params.q,
      title: params.title,
      scope: params.scope,
      section_id: params.section_id,
      library_id: params.library_id,
      collection_id: params.collection_id,
      person_id: state.person_id,
      query_fingerprint: params.query_fingerprint,
      include_technical: includeTechnical,
    }),
    queryFn: ({ signal }) => fetchCatalogFilters(state, { signal }, { includeTechnical }),
    enabled: enabled && catalogSourceAllowsOverlay(state.source),
    staleTime: 5 * 60 * 1000,
  });
}

export function useCatalogMetadataFilters() {
  return useCatalogFilters(createCatalogSearchState("query"), { includeTechnical: false });
}
