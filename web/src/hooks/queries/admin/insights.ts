import { useInfiniteQuery, useQuery } from "@tanstack/react-query";

import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { v2, type V2Query, type V2Result } from "@/api/v2/request";
import { adminKeys } from "../keys";

export type AdminInsightsCapabilities = V2Result<"GET /api/v2/admin/insights/capabilities">;
export type AdminInsightsActivity = V2Result<"GET /api/v2/admin/insights/activity">;
export type AdminInsightsPeople = V2Result<"GET /api/v2/admin/insights/people">;
export type AdminInsightsDevices = V2Result<"GET /api/v2/admin/insights/devices">;
export type AdminInsightsBandwidth = V2Result<"GET /api/v2/admin/insights/bandwidth">;
export type AdminInsightsTitles = V2Result<"GET /api/v2/admin/insights/titles">;
export type AdminInsightsTitle = V2Result<"GET /api/v2/admin/insights/titles/{content_id}">;

export type AdminInsightsActivityQuery = V2Query<"GET /api/v2/admin/insights/activity">;
export type AdminInsightsPeopleQuery = V2Query<"GET /api/v2/admin/insights/people">;
export type AdminInsightsDevicesQuery = V2Query<"GET /api/v2/admin/insights/devices">;
export type AdminInsightsBandwidthQuery = V2Query<"GET /api/v2/admin/insights/bandwidth">;
export type AdminInsightsTitlesQuery = V2Query<"GET /api/v2/admin/insights/titles">;
export type AdminInsightsTitleQuery = V2Query<"GET /api/v2/admin/insights/titles/{content_id}">;

const INSIGHTS_STALE_TIME = 45_000;

let lastAuthority: ProfileRequestContextSnapshot | null = null;
let authorityGeneration = 0;
let refreshWindowUntil = 0;

/**
 * Marks the next few seconds of Insights queries as user-triggered refreshes so
 * they pass `refresh=true` to bypass the server's 60s cache without changing
 * the React Query cache key.
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

function useAdminInsightQuery<T>(
  key: readonly unknown[],
  read: (profileContext: ProfileRequestContextSnapshot, signal: AbortSignal) => Promise<T>,
  enabled: boolean,
  staleTime = INSIGHTS_STALE_TIME,
) {
  const scope = insightsScope();
  return useQuery({
    queryKey: [...key, scope],
    queryFn: ({ signal }) => readInsight(scope, (profileContext) => read(profileContext, signal)),
    enabled,
    staleTime,
    retry: false,
  });
}

export function useAdminInsightsCapabilities(enabled = true) {
  return useAdminInsightQuery(
    adminKeys.insightsCapabilities(),
    (profileContext, signal) =>
      v2("GET /api/v2/admin/insights/capabilities", { profileContext, signal }),
    enabled,
    5 * 60_000,
  );
}

export function useAdminInsightsActivity(query: AdminInsightsActivityQuery, enabled = true) {
  return useAdminInsightQuery(
    adminKeys.insightsActivity(query as Record<string, unknown>),
    (profileContext, signal) =>
      v2("GET /api/v2/admin/insights/activity", {
        query: withRefreshFlag(query),
        profileContext,
        signal,
      }),
    enabled,
  );
}

export function useAdminInsightsPeople(query: AdminInsightsPeopleQuery, enabled = true) {
  return useAdminInsightQuery(
    adminKeys.insightsPeople(query as Record<string, unknown>),
    (profileContext, signal) =>
      v2("GET /api/v2/admin/insights/people", {
        query: withRefreshFlag(query),
        profileContext,
        signal,
      }),
    enabled,
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
    retry: false,
  });
}

export function useAdminInsightsDevices(query: AdminInsightsDevicesQuery, enabled = true) {
  return useAdminInsightQuery(
    adminKeys.insightsDevices(query as Record<string, unknown>),
    (profileContext, signal) =>
      v2("GET /api/v2/admin/insights/devices", {
        query: withRefreshFlag(query),
        profileContext,
        signal,
      }),
    enabled,
  );
}

export function useAdminInsightsBandwidth(query: AdminInsightsBandwidthQuery, enabled = true) {
  return useAdminInsightQuery(
    adminKeys.insightsBandwidth(query as Record<string, unknown>),
    (profileContext, signal) =>
      v2("GET /api/v2/admin/insights/bandwidth", {
        query: withRefreshFlag(query),
        profileContext,
        signal,
      }),
    enabled,
  );
}

export function useAdminInsightsTitles(query: AdminInsightsTitlesQuery, enabled = true) {
  return useAdminInsightQuery(
    adminKeys.insightsTitles(query as Record<string, unknown>),
    (profileContext, signal) =>
      v2("GET /api/v2/admin/insights/titles", {
        query: withRefreshFlag(query),
        profileContext,
        signal,
      }),
    enabled,
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
    enabled && Boolean(contentId),
  );
}
