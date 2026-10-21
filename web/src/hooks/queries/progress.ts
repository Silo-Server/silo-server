import { useCallback } from "react";
import { useMutation, useQuery, useQueries, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type { ItemDetail } from "@/api/types";
import { v2, type V2Result } from "@/api/v2/request";
import { bumpHomeRefreshSignal } from "@/pages/homeSurfaceRefresh";
import { catalogKeys, progressKeys } from "./keys";
import { fetchCatalogItemDetail } from "./catalogRead";
import {
  cancelItemDetailQueries,
  dropInactiveWatchDetail,
  invalidateMediaSurfaceQueries,
  removeItemFromHomeSectionCaches,
  updateCatalogItemDetail,
} from "./mediaSurfaceRefresh";

interface ContinueWatchingOptions {
  enabled?: boolean;
}

/** The first page of the profile's progress list as the v2 contract returns it. */
export type ProgressList = V2Result<"GET /api/v2/progress">;
export type ProgressListEntry = ProgressList["items"][number];

export function useProgressList(libraryId?: number, options?: ContinueWatchingOptions) {
  return useQuery({
    queryKey: progressKeys.list("in_progress", libraryId),
    queryFn: () =>
      v2("GET /api/v2/progress", {
        query: {
          status: "in_progress",
          limit: 20,
          library_id: libraryId ? String(libraryId) : undefined,
        },
      }),
    enabled: options?.enabled ?? true,
  });
}

export interface ContinueWatchingItem {
  progress: ProgressListEntry;
  detail: ItemDetail | undefined;
  isLoading: boolean;
}

export function useContinueWatching(
  libraryId?: number,
  options?: ContinueWatchingOptions,
): {
  items: ContinueWatchingItem[];
  isLoading: boolean;
} {
  const enabled = options?.enabled ?? true;
  const { data: progressData, isLoading: progressLoading } = useProgressList(libraryId, {
    enabled,
  });

  const entries = progressData?.items ?? [];

  const detailQueries = useQueries({
    queries: entries.map((entry) => ({
      queryKey: catalogKeys.itemDetail(entry.media_item_id),
      queryFn: ({ signal }) => fetchCatalogItemDetail(entry.media_item_id, undefined, { signal }),
      enabled: enabled && !!entry.media_item_id,
      staleTime: 2 * 60 * 1000,
    })),
  });

  const items: ContinueWatchingItem[] = entries.map((entry, i) => ({
    progress: entry,
    detail: detailQueries[i]?.data,
    isLoading: detailQueries[i]?.isLoading ?? true,
  }));

  return {
    items,
    isLoading: enabled && progressLoading,
  };
}

interface ReportMediaProgressVars {
  contentId: string;
  positionSeconds: number;
  durationSeconds: number;
  forceOverwrite?: boolean;
}

export function useReportMediaProgress() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({
      contentId,
      positionSeconds,
      durationSeconds,
      forceOverwrite = true,
    }: ReportMediaProgressVars) => {
      const result = await v2("POST /api/v2/sync/progress", {
        body: {
          items: [
            {
              media_item_id: contentId,
              position_ms: Math.round(positionSeconds * 1000),
              duration_ms: Math.round(durationSeconds * 1000),
              force_overwrite: forceOverwrite,
            },
          ],
        },
      });
      const failed = result.items.find((item) => item.status === "failure");
      if (failed?.status === "failure") throw new Error(failed.failure.detail);
      return result;
    },
    onSuccess: (_data, variables) => {
      // Progress genuinely changed → refresh progress-derived surfaces
      // (continue-watching etc.). Scope the catalog invalidation to THIS item's
      // detail rather than all of `catalog`: a progress report fires every ~10s
      // during playback, and invalidating catalogKeys.all refetched every active
      // browse/detail query — including the large audiobook author/narrator
      // group lists — on every tick.
      queryClient.invalidateQueries({ queryKey: progressKeys.all });
      queryClient.invalidateQueries({
        queryKey: catalogKeys.itemDetail(variables.contentId),
      });
    },
  });
}

interface ClearResumePositionVars {
  contentId: string;
}

/**
 * Clears the acting profile's resume position on a movie or episode, keeping
 * its watched state and watch history (issue #2134). The item leaves Continue
 * Watching at once, and its cached watch detail is dropped so playing it next
 * starts from the beginning instead of a stale resume point.
 */
export function useClearResumePosition() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ contentId }: ClearResumePositionVars) =>
      v2("DELETE /api/v2/progress/{media_item_id}/position", {
        path: { media_item_id: contentId },
      }),
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to reset progress");
    },
    onSuccess: async (_data, { contentId }) => {
      await cancelItemDetailQueries(queryClient, contentId);
      updateCatalogItemDetail(queryClient, contentId, (detail) =>
        detail.user_data
          ? {
              ...detail,
              user_data: { ...detail.user_data, position_seconds: 0, is_in_progress: false },
            }
          : detail,
      );
      removeItemFromHomeSectionCaches(queryClient, contentId, "continue_watching");
      bumpHomeRefreshSignal(queryClient);
      toast.success("Progress reset");
      // The next play must start from the beginning, not the cached position.
      const knownRelatedIds = dropInactiveWatchDetail(queryClient, contentId);
      await invalidateMediaSurfaceQueries(queryClient, {
        itemId: contentId,
        skipSimilarItems: true,
        knownRelatedIds,
      });
      bumpHomeRefreshSignal(queryClient);
    },
  });
}

/**
 * The detail page's Reset Progress action: offered for a movie or episode with
 * a resume position, otherwise undefined.
 */
export function useResetProgressAction(
  item: Pick<ItemDetail, "content_id" | "type" | "user_data">,
): (() => void) | undefined {
  const { mutate } = useClearResumePosition();
  const contentId = item.content_id;
  const reset = useCallback(() => mutate({ contentId }), [contentId, mutate]);
  // Leaf detail also carries the season counters, so read the position itself.
  const userData = item.user_data;
  const position =
    userData && "position_seconds" in userData ? (userData.position_seconds ?? 0) : 0;
  const hasResumePosition = (item.type === "movie" || item.type === "episode") && position > 0;
  return hasResumePosition ? reset : undefined;
}
