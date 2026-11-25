import { useInfiniteQuery, useQuery, type UseQueryResult } from "@tanstack/react-query";

import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { v2, type V2Query, type V2Result } from "@/api/v2/request";
import type { WidgetRange } from "@/components/admin/dashboard/types";
import {
  detectBrowserTimezone,
  widgetRangeToInsightsRange,
} from "@/pages/admin-insights/insightsHelpers";
import { adminKeys } from "../keys";

export type AdminInsightsCapabilities = V2Result<"GET /api/v2/admin/insights/capabilities">;
export type AdminInsightsActivity = V2Result<"GET /api/v2/admin/insights/activity">;
export type AdminInsightsPeople = V2Result<"GET /api/v2/admin/insights/people">;
export type AdminInsightsDevices = V2Result<"GET /api/v2/admin/insights/devices">;
export type AdminInsightsBandwidth = V2Result<"GET /api/v2/admin/insights/bandwidth">;
export type AdminInsightsTitles = V2Result<"GET /api/v2/admin/insights/titles">;
export type AdminInsightsTitle = V2Result<"GET /api/v2/admin/insights/titles/{content_id}">;
export type AdminInsightsLibrary = V2Result<"GET /api/v2/admin/insights/library">;

export type AdminInsightsActivityQuery = V2Query<"GET /api/v2/admin/insights/activity">;
export type AdminInsightsPeopleQuery = V2Query<"GET /api/v2/admin/insights/people">;
export type AdminInsightsDevicesQuery = V2Query<"GET /api/v2/admin/insights/devices">;
export type AdminInsightsBandwidthQuery = V2Query<"GET /api/v2/admin/insights/bandwidth">;
export type AdminInsightsTitlesQuery = V2Query<"GET /api/v2/admin/insights/titles">;
export type AdminInsightsTitleQuery = V2Query<"GET /api/v2/admin/insights/titles/{content_id}">;
export type AdminInsightsLibraryQuery = V2Query<"GET /api/v2/admin/insights/library">;

const INSIGHTS_STALE_TIME = 45_000;

let lastAuthority: ProfileRequestContextSnapshot | null = null;
let authorityGeneration = 0;
let refreshWindowUntil = 0;

/**
 * Marks the next few seconds of Insights queries, on the Insights page and the
 * dashboard's Insights widgets, as user-triggered refreshes so they pass
 * `refresh=true`, which drops the server's cached Insights and reads fresh
 * data, without changing the React Query cache key.
 */
export function markAdminInsightsRefresh(): void {
  refreshWindowUntil = Date.now() + 5_000;
}

function withRefreshFlag<T extends { refresh?: boolean }>(query: T): T {
  return Date.now() <= refreshWindowUntil ? { ...query, refresh: true } : query;
}

function insightsScope(): string {
  const current = captureProfileRequestContext();
  if (
    current?.authContextVersion !== lastAuthority?.authContextVersion ||
    current?.serverOrigin !== lastAuthority?.serverOrigin ||
    current?.profileId !== lastAuthority?.profileId ||
    current?.profileToken !== lastAuthority?.profileToken
  ) {
    authorityGeneration++;
    lastAuthority = current;
  }
  return `insights:${authorityGeneration}`;
}

async function readInsight<T>(
  scope: string,
  read: (profileContext: ProfileRequestContextSnapshot) => Promise<T>,
): Promise<T> {
  const profileContext = captureProfileRequestContext();
  if (!profileContext || insightsScope() !== scope) {
    throw new StaleApiRequestContextError();
  }
  const result = await read(profileContext);
  if (!isCapturedProfileAuthorityActive(profileContext) || insightsScope() !== scope) {
    throw new StaleApiRequestContextError();
  }
  return result;
}

/**
 * Whether a tab may keep showing the previous result while a new sort, search,
 * or filter loads, so it dims its old numbers instead of falling back to
 * skeletons. Only a result read under the same signed-in profile carries over;
 * the scope is the last key segment.
 */
function readInScope(previousQuery: { queryKey: readonly unknown[] } | undefined, scope: string) {
  return previousQuery?.queryKey.at(-1) === scope;
}

interface InsightQueryOptions {
  enabled?: boolean;
  staleTime?: number;
  refetchInterval?: number | false;
  /**
   * Show the previous result while a changed key loads. Tabs only: a detail
   * sheet would show another person's or title's numbers.
   */
  keepPrevious?: boolean;
}

/** Options for an Insights read that a tab and a detail sheet both use. */
export type InsightTabQueryOptions = Pick<InsightQueryOptions, "enabled" | "keepPrevious">;

function useAdminInsightQuery<T>(
  key: readonly unknown[],
  read: (profileContext: ProfileRequestContextSnapshot, signal: AbortSignal) => Promise<T>,
  {
    enabled = true,
    staleTime = INSIGHTS_STALE_TIME,
    refetchInterval = false,
    keepPrevious = false,
  }: InsightQueryOptions,
): UseQueryResult<T> {
  const scope = insightsScope();
  return useQuery({
    queryKey: [...key, scope],
    queryFn: ({ signal }) => readInsight(scope, (profileContext) => read(profileContext, signal)),
    enabled,
    staleTime,
    refetchInterval,
    placeholderData: keepPrevious
      ? (previous, previousQuery) => (readInScope(previousQuery, scope) ? previous : undefined)
      : undefined,
    retry: false,
  });
}

/**
 * How often a dashboard widget re-reads its Insights window.
 *
 * The dashboard's own 60-second loop never touches Insights queries: a year or
 * all-time read takes seconds on a large history, and re-running it every
 * minute on every open dashboard would keep the read gate busy for numbers
 * that barely move. Short windows refresh every 5 minutes, long ones every
 * 15, and the dashboard's Refresh button marks them stale on demand. React
 * Query pauses the interval while the tab is hidden.
 */
export function dashboardInsightsInterval(range: WidgetRange): number {
  return range === "week" || range === "month" ? 5 * 60_000 : 15 * 60_000;
}

/**
 * The server-wide window a dashboard widget reads, in the browser's timezone.
 * Its shape matches the Insights page's unfiltered query, so a widget and the
 * page showing the same window share one cache entry.
 */
export function dashboardInsightsWindow(range: WidgetRange) {
  return { range: widgetRangeToInsightsRange(range), tz: detectBrowserTimezone() };
}

export function useDashboardInsightsActivity(range: WidgetRange) {
  const query: AdminInsightsActivityQuery = dashboardInsightsWindow(range);
  const interval = dashboardInsightsInterval(range);
  return useAdminInsightQuery(
    adminKeys.insightsActivity(query),
    (profileContext, signal) =>
      v2("GET /api/v2/admin/insights/activity", {
        query: withRefreshFlag(query),
        profileContext,
        signal,
      }),
    { staleTime: interval, refetchInterval: interval },
  );
}

export function useDashboardInsightsTitles(range: WidgetRange, limit: number) {
  const query: AdminInsightsTitlesQuery = {
    ...dashboardInsightsWindow(range),
    sort: "plays" as const,
    limit,
  };
  const interval = dashboardInsightsInterval(range);
  return useAdminInsightQuery(
    adminKeys.insightsTitles(query),
    (profileContext, signal) =>
      v2("GET /api/v2/admin/insights/titles", {
        query: withRefreshFlag(query),
        profileContext,
        signal,
      }),
    { staleTime: interval, refetchInterval: interval },
  );
}

export function useDashboardInsightsLibrary(range: WidgetRange) {
  const query: AdminInsightsLibraryQuery = dashboardInsightsWindow(range);
  const interval = dashboardInsightsInterval(range);
  return useAdminInsightQuery(
    adminKeys.insightsLibrary(query),
    (profileContext, signal) =>
      v2("GET /api/v2/admin/insights/library", {
        query: withRefreshFlag(query),
        profileContext,
        signal,
      }),
    { staleTime: interval, refetchInterval: interval },
  );
}

/** People with one account row: the widget reads only `top_profiles`. */
export function useDashboardInsightsPeople(range: WidgetRange) {
  const query: AdminInsightsPeopleQuery = { ...dashboardInsightsWindow(range), limit: 1 };
  const interval = dashboardInsightsInterval(range);
  return useAdminInsightQuery(
    adminKeys.insightsPeople(query),
    (profileContext, signal) =>
      v2("GET /api/v2/admin/insights/people", {
        query: withRefreshFlag(query),
        profileContext,
        signal,
      }),
    { staleTime: interval, refetchInterval: interval },
  );
}

export function useAdminInsightsCapabilities(enabled = true) {
  return useAdminInsightQuery(
    adminKeys.insightsCapabilities(),
    (profileContext, signal) =>
      v2("GET /api/v2/admin/insights/capabilities", { profileContext, signal }),
    { enabled, staleTime: 5 * 60_000 },
  );
}

export function useAdminInsightsActivity(
  query: AdminInsightsActivityQuery,
  options: InsightTabQueryOptions = {},
) {
  return useAdminInsightQuery(
    adminKeys.insightsActivity(query as Record<string, unknown>),
    (profileContext, signal) =>
      v2("GET /api/v2/admin/insights/activity", {
        query: withRefreshFlag(query),
        profileContext,
        signal,
      }),
    options,
  );
}

export function useAdminInsightsPeople(
  query: AdminInsightsPeopleQuery,
  options: InsightTabQueryOptions = {},
) {
  return useAdminInsightQuery(
    adminKeys.insightsPeople(query as Record<string, unknown>),
    (profileContext, signal) =>
      v2("GET /api/v2/admin/insights/people", {
        query: withRefreshFlag(query),
        profileContext,
        signal,
      }),
    options,
  );
}

const INSIGHTS_PEOPLE_PAGE_SIZE = 50;

export function useAdminInsightsPeopleInfinite(
  query: Omit<AdminInsightsPeopleQuery, "limit" | "offset">,
  enabled = true,
) {
  const scope = insightsScope();
  return useInfiniteQuery({
    queryKey: [...adminKeys.insightsPeople(query as Record<string, unknown>), "infinite", scope],
    initialPageParam: 0,
    queryFn: ({ pageParam, signal }) =>
      readInsight(scope, (profileContext) =>
        v2("GET /api/v2/admin/insights/people", {
          query: withRefreshFlag({
            ...query,
            limit: INSIGHTS_PEOPLE_PAGE_SIZE,
            offset: pageParam,
          }),
          profileContext,
          signal,
        }),
      ),
    getNextPageParam: (lastPage, allPages) => {
      const loaded = allPages.reduce((sum, page) => sum + page.accounts.length, 0);
      if (lastPage.accounts.length === 0 || loaded >= lastPage.total_accounts) {
        return undefined;
      }
      return loaded;
    },
    enabled,
    staleTime: INSIGHTS_STALE_TIME,
    placeholderData: (previous, previousQuery) =>
      readInScope(previousQuery, scope) ? previous : undefined,
    retry: false,
  });
}

export function useAdminInsightsDevices(
  query: AdminInsightsDevicesQuery,
  options: InsightTabQueryOptions = {},
) {
  return useAdminInsightQuery(
    adminKeys.insightsDevices(query as Record<string, unknown>),
    (profileContext, signal) =>
      v2("GET /api/v2/admin/insights/devices", {
        query: withRefreshFlag(query),
        profileContext,
        signal,
      }),
    options,
  );
}

export function useAdminInsightsBandwidth(
  query: AdminInsightsBandwidthQuery,
  options: InsightTabQueryOptions = {},
) {
  return useAdminInsightQuery(
    adminKeys.insightsBandwidth(query as Record<string, unknown>),
    (profileContext, signal) =>
      v2("GET /api/v2/admin/insights/bandwidth", {
        query: withRefreshFlag(query),
        profileContext,
        signal,
      }),
    options,
  );
}

export function useAdminInsightsTitles(
  query: AdminInsightsTitlesQuery,
  options: InsightTabQueryOptions = {},
) {
  return useAdminInsightQuery(
    adminKeys.insightsTitles(query as Record<string, unknown>),
    (profileContext, signal) =>
      v2("GET /api/v2/admin/insights/titles", {
        query: withRefreshFlag(query),
        profileContext,
        signal,
      }),
    options,
  );
}

export function useAdminInsightsLibrary(
  query: AdminInsightsLibraryQuery,
  options: InsightTabQueryOptions = {},
) {
  return useAdminInsightQuery(
    adminKeys.insightsLibrary(query as Record<string, unknown>),
    (profileContext, signal) =>
      v2("GET /api/v2/admin/insights/library", {
        query: withRefreshFlag(query),
        profileContext,
        signal,
      }),
    options,
  );
}

/** Reads one movie or series. The server does not cache title detail, so it takes no refresh flag. */
export function useAdminInsightsTitle(
  contentId: string | undefined,
  query: AdminInsightsTitleQuery,
  enabled = true,
) {
  return useAdminInsightQuery(
    adminKeys.insightsTitle(contentId ?? "", query as Record<string, unknown>),
    (profileContext, signal) =>
      v2("GET /api/v2/admin/insights/titles/{content_id}", {
        path: { content_id: contentId ?? "" },
        query,
        profileContext,
        signal,
      }),
    { enabled: enabled && Boolean(contentId) },
  );
}
