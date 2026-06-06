import { useEffect, useMemo, useState } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";

import { api } from "@/api/client";
import type { CatalogFiltersResponse, CatalogResponse } from "@/api/types";
import type { CatalogParams } from "@/hooks/queries/keys";
import { catalogKeys } from "@/hooks/queries/keys";
import { createEmptyQueryDefinition, type CatalogSource } from "@/api/types";
import {
  buildCatalogApiSearchParams,
  catalogSourceAllowsOverlay,
  type CatalogSearchState,
} from "@/pages/catalogSearchParams";

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
    query_fingerprint: JSON.stringify(state.query_definition),
    include_total: includeTotal,
    limit,
  };
}

function buildCatalogUrl(
  state: CatalogSearchState,
  limit: number,
  offset: number,
  includeTotal: boolean,
  snapshot?: string,
): string {
  const params = buildCatalogApiSearchParams(state);
  params.set("limit", String(limit));
  params.set("offset", String(offset));
  if (!includeTotal) {
    params.set("include_total", "false");
  }
  if (snapshot) {
    params.set("snapshot", snapshot);
  }
  return `/catalog?${params.toString()}`;
}

function buildCatalogFiltersUrlWithOptions(
  state: CatalogSearchState,
  options: { includeTechnical?: boolean } = {},
): string {
  const params = buildCatalogApiSearchParams(state);
  if (options.includeTechnical === false) {
    params.set("include_technical", "false");
  }
  return `/catalog/filters?${params.toString()}`;
}

export async function fetchCatalogPage(
  state: CatalogSearchState,
  limit: number,
  offset: number,
  options?: RequestInit,
  includeTotal = true,
  snapshot?: string,
): Promise<CatalogResponse> {
  return api<CatalogResponse>(
    buildCatalogUrl(state, limit, offset, includeTotal, snapshot),
    options,
  );
}

export async function fetchCatalogFilters(
  state: CatalogSearchState,
  options?: RequestInit,
  requestOptions: { includeTechnical?: boolean } = {},
): Promise<CatalogFiltersResponse> {
  return api<CatalogFiltersResponse>(
    buildCatalogFiltersUrlWithOptions(state, requestOptions),
    options,
  );
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
  options?: RequestInit,
): Promise<CatalogFacetSearchResponse> {
  const params = buildCatalogApiSearchParams(state);
  params.set("facet", facet);
  params.set("q", prefix);
  params.set("limit", String(limit));
  return api<CatalogFacetSearchResponse>(
    `/catalog/filters/search?${params.toString()}`,
    options,
  );
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
  options: { limit?: number; visibleRange?: [number, number]; includeTotal?: boolean } = {},
) {
  const limit = options.limit ?? 60;
  const includeTotal = options.includeTotal ?? true;
  const page0Params = catalogParamsForKey(state, limit, includeTotal);
  const remainingPageParams = catalogParamsForKey(state, limit, false);
  const visibleRange = options.visibleRange ?? [0, limit - 1];
  const bufferPages = state.source === "query" && state.q ? 0 : 1;
  const startPage = Math.max(0, Math.floor(visibleRange[0] / limit) - bufferPages);
  const endPage = Math.floor(visibleRange[1] / limit) + bufferPages;

  // Fetch page 0 separately so its snapshot timestamp is available
  // synchronously for subsequent page queries, preventing duplicate items
  // when new items are added between page fetches (e.g. during a scan).
  const page0Result = useQuery({
    queryKey: catalogKeys.list({ ...page0Params, limit, offset: 0 }),
    queryFn: ({ signal }: { signal: AbortSignal }) =>
      fetchCatalogPage(state, limit, 0, { signal }, includeTotal),
    staleTime: 10 * 60 * 1000,
  });

  const snapshot = page0Result.data?.snapshot;
  const canFetchRemainingPages = page0Result.data !== undefined;

  const remainingPageIndices = useMemo(() => {
    const indices = new Set<number>();
    for (let page = startPage; page <= endPage; page++) {
      if (page > 0) {
        indices.add(page);
      }
    }
    return Array.from(indices).sort((a, b) => a - b);
  }, [endPage, startPage]);

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
        enabled: canFetchRemainingPages,
      };
    }),
  });

  const title = page0Result.data?.title ?? state.title;
  const isLoading = page0Result.isLoading;

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
    query_fingerprint: JSON.stringify(state.query_definition),
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
    },
    isLoading,
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
