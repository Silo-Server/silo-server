import { useQuery } from "@tanstack/react-query";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { v2 } from "@/api/v2/request";
import type { AdminUserProfile } from "@/api/types";
import {
  ADMIN_PLAYBACK_HISTORY_MAX_LIMIT,
  adminPlaybackHistoryScope,
  getAdminPlaybackHistoryEntry,
  listAdminPlaybackHistory,
  type AdminPlaybackHistoryPage,
  type AdminPlaybackHistoryQuery,
} from "@/api/v2/adminPlaybackHistory";
import { adminKeys } from "../keys";

const ADMIN_HISTORY_STALE_TIME = 15_000;

export interface AdminPlaybackHistoryParams {
  userId?: number;
  profileId?: string;
  mediaItemId?: string;
  /** Attempts of any episode of this series. */
  seriesId?: string;
  completed?: "all" | "true" | "false";
  search?: string;
  playMethod?: string;
  devicePlatform?: string;
  clientName?: string;
  sourceVideoCodec?: string;
  subtitleBurnIn?: "all" | "true" | "false";
  streamLocation?: "all" | "local" | "remote";
  adaptationReason?: string;
  libraryId?: number;
  startedAfter?: string;
  startedBefore?: string;
  cursor?: string;
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
  if (params.seriesId) query.series_id = params.seriesId;
  if (params.completed === "true" || params.completed === "false") {
    query.completed = params.completed;
  }
  if (params.search?.trim()) query.search = params.search.trim();
  if (
    params.playMethod === "direct" ||
    params.playMethod === "remux" ||
    params.playMethod === "transcode"
  ) {
    query.play_method = params.playMethod;
  }
  if (params.devicePlatform?.trim()) query.device_platform = params.devicePlatform.trim();
  if (params.clientName?.trim()) query.client_name = params.clientName.trim();
  if (params.sourceVideoCodec?.trim()) query.source_video_codec = params.sourceVideoCodec.trim();
  if (params.subtitleBurnIn === "true" || params.subtitleBurnIn === "false") {
    query.subtitle_burn_in = params.subtitleBurnIn;
  }
  if (params.streamLocation === "local" || params.streamLocation === "remote") {
    query.stream_location = params.streamLocation;
  }
  if (params.adaptationReason?.trim()) query.adaptation_reason = params.adaptationReason.trim();
  if (params.libraryId) query.library_id = String(params.libraryId);
  if (params.startedAfter?.trim()) query.started_after = params.startedAfter.trim();
  if (params.startedBefore?.trim()) query.started_before = params.startedBefore.trim();
  if (params.cursor) query.cursor = params.cursor;
  return query;
}

/**
 * One keyset-paginated page of finalized playback attempts matching the
 * filters, carrying `hasMore` and `nextCursor` for server-side pagination.
 */
export function useAdminPlaybackHistoryPage(params: AdminPlaybackHistoryParams) {
  const scope = adminPlaybackHistoryScope();
  const query = buildAdminPlaybackHistoryQuery(params);
  return useQuery({
    queryKey: [...adminKeys.playbackHistory(params as Record<string, unknown>), scope],
    queryFn: ({ signal }): Promise<AdminPlaybackHistoryPage> =>
      listAdminPlaybackHistory(query, scope, signal),
    retry: false,
    staleTime: ADMIN_HISTORY_STALE_TIME,
    refetchInterval: params.cursor ? false : ADMIN_HISTORY_STALE_TIME,
    refetchIntervalInBackground: false,
  });
}

/** Reads a single finalized playback session by ID (used for `?session=` deep links). */
export function useAdminPlaybackHistoryEntry(sessionId: string | null, enabled = true) {
  const scope = adminPlaybackHistoryScope();
  return useQuery({
    queryKey: [...adminKeys.playbackHistoryDetail(sessionId ?? ""), scope],
    queryFn: ({ signal }) => getAdminPlaybackHistoryEntry(sessionId ?? "", scope, signal),
    enabled: enabled && Boolean(sessionId),
    retry: false,
    staleTime: 60_000,
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
