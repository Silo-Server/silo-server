import { useQuery } from "@tanstack/react-query";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { v2 } from "@/api/v2/request";
import type { AdminPlaybackHistoryItem, AdminUserProfile } from "@/api/types";
import {
  ADMIN_PLAYBACK_HISTORY_MAX_LIMIT,
  adminPlaybackHistoryScope,
  listAdminPlaybackHistory,
  type AdminPlaybackHistoryQuery,
} from "@/api/v2/adminPlaybackHistory";
import { adminKeys } from "../keys";

const ADMIN_HISTORY_STALE_TIME = 15_000;

export interface AdminPlaybackHistoryParams {
  userId?: number;
  profileId?: string;
  mediaItemId?: string;
  completed?: "all" | "true" | "false";
  limit?: number;
}

/**
 * Translates the view's filter state into the v2 query. An absent completion
 * filter and "all" both list every finalized attempt; an unset limit asks for
 * the v2 page ceiling so the views keep their single-page reading.
 */
export function buildAdminPlaybackHistoryQuery(
  params: AdminPlaybackHistoryParams,
): AdminPlaybackHistoryQuery {
  const query: AdminPlaybackHistoryQuery = {
    limit: Math.min(
      params.limit ?? ADMIN_PLAYBACK_HISTORY_MAX_LIMIT,
      ADMIN_PLAYBACK_HISTORY_MAX_LIMIT,
    ),
  };
  if (params.userId) query.user_id = String(params.userId);
  if (params.profileId) query.profile_id = params.profileId;
  if (params.mediaItemId) query.media_item_id = params.mediaItemId;
  if (params.completed === "true" || params.completed === "false") {
    query.completed = params.completed;
  }
  return query;
}

/**
 * The first page of finalized playback attempts matching the filters, read
 * under the authority captured when the query was created. Polled while
 * mounted, as before; the cache key carries the authority generation so a
 * profile or account switch never serves another authority's page.
 */
export function useAdminPlaybackHistory(params: AdminPlaybackHistoryParams) {
  const scope = adminPlaybackHistoryScope();
  const query = buildAdminPlaybackHistoryQuery(params);
  return useQuery({
    queryKey: [...adminKeys.playbackHistory(params), scope],
    queryFn: ({ signal }): Promise<AdminPlaybackHistoryItem[]> =>
      listAdminPlaybackHistory(query, scope, signal).then((page) => page.items),
    retry: false,
    staleTime: ADMIN_HISTORY_STALE_TIME,
    refetchInterval: ADMIN_HISTORY_STALE_TIME,
    refetchIntervalInBackground: true,
  });
}

export function useAdminUserProfiles(userId?: number) {
  const c = captureProfileRequestContext();
  const scope = c ? `${c.serverOrigin}:${c.authContextVersion}:${c.profileId}` : "unavailable";
  return useQuery({
    queryKey: [...adminKeys.userProfiles(userId), scope],
    queryFn: async () => {
      if (!c || !isCapturedProfileAuthorityActive(c)) throw new StaleApiRequestContextError();
      const result = await v2("GET /api/v2/admin/users/{id}/profiles", {
        path: { id: String(userId) },
        profileContext: c,
      });
      if (!isCapturedProfileAuthorityActive(c)) throw new StaleApiRequestContextError();
      if (!Array.isArray(result.items) || !result.page || result.page.has_more)
        throw new Error("Incomplete profile listing");
      return result.items as AdminUserProfile[];
    },
    enabled: Boolean(userId),
    retry: false,
    staleTime: ADMIN_HISTORY_STALE_TIME,
  });
}
