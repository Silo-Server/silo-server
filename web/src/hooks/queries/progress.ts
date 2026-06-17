import { useMutation, useQuery, useQueries, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { ProgressListResponse, ProgressEntry, ItemDetail } from "@/api/types";
import { catalogKeys, progressKeys } from "./keys";
import { fetchCatalogItemDetail } from "./catalogRead";

interface ContinueWatchingOptions {
  enabled?: boolean;
}

export function useProgressList(libraryId?: number, options?: ContinueWatchingOptions) {
  return useQuery({
    queryKey: progressKeys.list("in_progress", libraryId),
    queryFn: () => {
      const searchParams = new URLSearchParams({
        status: "in_progress",
        limit: "20",
      });
      if (libraryId) {
        searchParams.set("library_id", String(libraryId));
      }
      return api<ProgressListResponse>(`/progress?${searchParams.toString()}`);
    },
    enabled: options?.enabled ?? true,
  });
}

export interface ContinueWatchingItem {
  progress: ProgressEntry;
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

  const entries = progressData?.progress ?? [];

  const detailQueries = useQueries({
    queries: entries.map((entry) => ({
      queryKey: catalogKeys.itemDetail(entry.media_item_id),
      queryFn: () => fetchCatalogItemDetail(entry.media_item_id),
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
    mutationFn: ({
      contentId,
      positionSeconds,
      durationSeconds,
      forceOverwrite = true,
    }: ReportMediaProgressVars) =>
      api("/sync/progress", {
        method: "POST",
        body: JSON.stringify({
          items: [
            {
              media_item_id: contentId,
              position: positionSeconds,
              duration: durationSeconds,
              force_overwrite: forceOverwrite,
            },
          ],
        }),
      }),
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
