import {
  CATALOG_REQUEST_PAGE_PARAM,
  readCatalogRequestPage,
  withCatalogRequestPage,
} from "./catalogSearchParams";

/**
 * What a search results page shows: the overview (a few rows of each result
 * kind, so requestable titles stay near the top whatever the library returns),
 * or the full list of one kind.
 */
export type SearchResultsView = "overview" | "library" | "requests";

const VIEW_PARAM = "view";

export function readSearchView(searchParams: URLSearchParams): SearchResultsView {
  const value = searchParams.get(VIEW_PARAM);
  if (value === "overview" || value === "library" || value === "requests") return value;
  // Search URLs from before these views kept a later request page with no
  // view; it still opens the request list at that page.
  return !value && readCatalogRequestPage(searchParams) > 1 ? "requests" : "overview";
}

const BEST_MATCH_ROWS = 2;
const MORE_RESULT_ROWS = 3;

/**
 * Cuts the relevance-ordered library results into the overview's rows: two
 * rows of best matches, then up to three rows of more. Anything past that is
 * reached through the full library view, so the overview stays the same
 * height whether the search returns 20 titles or 2,000.
 */
export function splitOverviewResults<T>(
  items: readonly T[],
  columns: number,
  serverHasMore: boolean,
): { best: T[]; more: T[]; hasMoreThanShown: boolean } {
  const perRow = Math.max(1, Math.floor(columns));
  const bestEnd = perRow * BEST_MATCH_ROWS;
  const moreEnd = bestEnd + perRow * MORE_RESULT_ROWS;
  return {
    best: items.slice(0, bestEnd),
    more: items.slice(bestEnd, moreEnd),
    hasMoreThanShown: serverHasMore || items.length > moreEnd,
  };
}

/** Switching views starts the request pager over; the overview is the bare URL. */
export function withSearchView(
  searchParams: URLSearchParams,
  view: SearchResultsView,
): URLSearchParams {
  const next = new URLSearchParams(searchParams);
  next.delete(CATALOG_REQUEST_PAGE_PARAM);
  if (view === "overview") {
    next.delete(VIEW_PARAM);
  } else {
    next.set(VIEW_PARAM, view);
  }
  return next;
}

/**
 * Pages the request grid without leaving the view the URL names. The
 * overview's first page is the bare URL; past it the overview names itself,
 * since a later page with no view opens the request list (readSearchView).
 */
export function withRequestPage(
  searchParams: URLSearchParams,
  view: SearchResultsView,
  page: number,
): URLSearchParams {
  const next = withCatalogRequestPage(searchParams, page);
  if (view === "overview" && page <= 1) {
    next.delete(VIEW_PARAM);
  } else {
    next.set(VIEW_PARAM, view);
  }
  return next;
}
