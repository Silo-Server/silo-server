import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { useRealtimeEvents } from "@/components/realtimeEventsContext";
import type {
  AdminJob,
  ApplyItemImageRequest,
  ApplyItemImageResponse,
  ItemDetail,
  ItemImagesResponse,
  ItemMatchSearchRequest,
  ItemMatchSearchResponse,
  WatchDetail,
} from "@/api/types";
import { adminKeys, catalogKeys, episodeKeys, itemKeys, sectionKeys } from "./keys";
import { toast } from "sonner";
import { getCachedWatchedInvalidationKeys } from "@/pages/ItemDetail/watchedState";
import { invalidateMediaSurfaceQueries } from "./mediaSurfaceRefresh";
import { bumpHomeRefreshSignal } from "@/pages/homeSurfaceRefresh";

function itemPathID(id: string): string {
  return encodeURIComponent(id);
}

export async function fetchWatchDetail(
  id: string,
  fileId?: number,
  libraryId?: number,
  options?: RequestInit,
): Promise<WatchDetail> {
  const searchParams = new URLSearchParams();
  if (fileId != null) searchParams.set("fileId", String(fileId));
  if (libraryId != null) searchParams.set("library_id", String(libraryId));
  const query = searchParams.toString();
  return api<WatchDetail>(`/watch/${itemPathID(id)}${query ? `?${query}` : ""}`, options);
}

export function useWatchDetail(id: string | undefined, fileId?: number, libraryId?: number) {
  return useQuery({
    queryKey: itemKeys.watchDetail(id!, fileId, libraryId),
    queryFn: () => fetchWatchDetail(id!, fileId, libraryId),
    enabled: !!id,
    staleTime: 0,
  });
}

type RefreshMutationItem = Pick<ItemDetail, "content_id" | "type" | "series_id" | "season_number">;
export type RefreshItemMetadataMode = "quick" | "complete";

interface RefreshItemMetadataVariables {
  item: RefreshMutationItem;
  mode: RefreshItemMetadataMode;
  onReplaced?: (contentID: string) => void | Promise<void>;
}

interface ItemRefreshJobResult {
  requested_content_id?: string;
  refresh_content_id?: string;
  detail_content_id?: string;
  scan_path?: string;
  matched_files?: number;
  scan_result?: {
    new?: number;
  };
}

export function useRefreshItemMetadata() {
  const queryClient = useQueryClient();
  const { awaitAdminJob } = useRealtimeEvents();
  return useMutation({
    mutationFn: async ({ item, mode }: RefreshItemMetadataVariables) => {
      const job = await api<AdminJob>(
        `/admin/items/${itemPathID(item.content_id)}/refresh-metadata`,
        {
          method: "POST",
          body: JSON.stringify({ mode }),
        },
      );
      const completed = await awaitAdminJob(job.id);
      return { job: completed };
    },
    onSuccess: async ({ job }, { item, mode, onReplaced }) => {
      const result = (job.result_payload ?? {}) as ItemRefreshJobResult;
      const refreshContentID = result.refresh_content_id;
      const detailContentID = result.detail_content_id;
      const newFiles = result.scan_result?.new ?? 0;

      if (mode === "complete") {
        toast.success("Complete refresh finished");
      } else if (newFiles > 0) {
        toast.success(
          `Metadata refreshed. Found ${newFiles} new file version${newFiles === 1 ? "" : "s"}`,
        );
      } else {
        toast.success("Metadata refreshed");
      }

      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["items", "detail", item.content_id] }),
        queryClient.invalidateQueries({
          queryKey: ["catalog", "items", item.content_id, "detail"],
        }),
        queryClient.invalidateQueries({ queryKey: ["items", "watchDetail", item.content_id] }),
      ]);

      if (refreshContentID && refreshContentID !== item.content_id) {
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: ["items", "detail", refreshContentID] }),
          queryClient.invalidateQueries({
            queryKey: ["catalog", "items", refreshContentID, "detail"],
          }),
        ]);
      }
      if (
        detailContentID &&
        detailContentID !== item.content_id &&
        detailContentID !== refreshContentID
      ) {
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: ["items", "detail", detailContentID] }),
          queryClient.invalidateQueries({
            queryKey: ["catalog", "items", detailContentID, "detail"],
          }),
        ]);
      }

      if (item.type === "series") {
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: episodeKeys.seasons(item.content_id) }),
          queryClient.invalidateQueries({
            queryKey: ["catalog", "series", item.content_id, "seasons"],
          }),
          queryClient.invalidateQueries({ queryKey: episodeKeys.all }),
        ]);
      } else if ((item.type === "season" || item.type === "episode") && item.series_id) {
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: ["items", "detail", item.series_id] }),
          queryClient.invalidateQueries({
            queryKey: ["catalog", "items", item.series_id, "detail"],
          }),
          queryClient.invalidateQueries({ queryKey: episodeKeys.seasons(item.series_id) }),
          queryClient.invalidateQueries({
            queryKey: ["catalog", "series", item.series_id, "seasons"],
          }),
          queryClient.invalidateQueries({ queryKey: episodeKeys.all }),
        ]);
        if (item.season_number != null) {
          await queryClient.invalidateQueries({
            queryKey: episodeKeys.bySeason(item.series_id, item.season_number),
          });
          await queryClient.invalidateQueries({
            queryKey: episodeKeys.seasonDetail(item.series_id, item.season_number),
          });
          await queryClient.invalidateQueries({
            queryKey: catalogKeys.seasonEpisodes(item.series_id, item.season_number),
          });
          await queryClient.invalidateQueries({
            queryKey: catalogKeys.seasonDetail(item.series_id, item.season_number),
          });
        }
      }

      if (detailContentID && detailContentID !== item.content_id && onReplaced) {
        await onReplaced(detailContentID);
      }
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Refresh failed");
    },
  });
}

export interface RedetectEpisodeIntroResponse {
  status: string;
}

export async function redetectEpisodeIntro(
  episodeId: string,
): Promise<RedetectEpisodeIntroResponse> {
  return api<RedetectEpisodeIntroResponse>(
    `/admin/items/${itemPathID(episodeId)}/redetect-intro`,
    {
      method: "POST",
    },
  );
}

export function useRedetectEpisodeIntro() {
  return useMutation({
    mutationFn: redetectEpisodeIntro,
    onSuccess: (response) => {
      toast.success(
        response.status === "already_running"
          ? "Re-detection already running"
          : "Re-detection started",
      );
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to start re-detection");
    },
  });
}

export interface UpdateItemMetadataRequest {
  title?: string;
  sort_title?: string;
  original_title?: string;
  overview?: string;
  tagline?: string;
  content_rating?: string;
  year?: number;
  runtime?: number;
  genres?: string[];
  studios?: string[];
  networks?: string[];
  countries?: string[];
  release_date?: string | null;
  first_air_date?: string | null;
  last_air_date?: string | null;
  air_time?: string | null;
  air_timezone?: string | null;
  air_date?: string | null;
  status?: string;
  rating_imdb?: number | null;
  rating_tmdb?: number | null;
  rating_rt_critic?: number | null;
  rating_rt_audience?: number | null;
  imdb_id?: string;
  tmdb_id?: string;
  tvdb_id?: string;
  season_number?: number;
  episode_number?: number;
  locked_fields?: number[];
}

export function useUpdateItemMetadata(contentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: UpdateItemMetadataRequest) =>
      api<ItemDetail>(`/admin/items/${itemPathID(contentId)}/metadata`, {
        method: "PATCH",
        body: JSON.stringify(data),
      }),
    onSuccess: () => {
      void invalidateMediaSurfaceQueries(queryClient, { itemId: contentId }).then(() => {
        bumpHomeRefreshSignal(queryClient);
      });
      toast.success("Metadata saved");
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save metadata");
    },
  });
}

type WatchedMutationItem = Pick<
  ItemDetail,
  "content_id" | "type" | "series_id" | "season_number" | "user_data"
>;

export function useWatchedStateMutation(item: WatchedMutationItem) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (nextPlayed: boolean) =>
      api(`/watched/${itemPathID(item.content_id)}`, {
        method: nextPlayed ? "POST" : "DELETE",
      }),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to update watched state");
    },
    onSuccess: (_data, nextPlayed) => {
      toast.success(nextPlayed ? "Marked as watched" : "Marked as unwatched");
    },
    onSettled: async () => {
      await invalidateMediaSurfaceQueries(queryClient, {
        itemId: item.content_id,
        watchedKeys: getCachedWatchedInvalidationKeys(queryClient, item),
      });
      bumpHomeRefreshSignal(queryClient);
    },
  });
}

export function useSearchItemMatchCandidates(contentId: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (params: ItemMatchSearchRequest) =>
      api<ItemMatchSearchResponse>(`/admin/items/${itemPathID(contentId)}/match/search`, {
        method: "POST",
        body: JSON.stringify(params),
      }),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Match search failed");
    },
    meta: { queryClient },
  });
}

type ApplyMatchItem = Pick<ItemDetail, "content_id" | "type" | "series_id" | "season_number">;

export function useApplyItemMatch() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async ({
      item,
      providerIds,
    }: {
      item: ApplyMatchItem;
      providerIds: Record<string, string>;
    }) => {
      return api(`/admin/items/${itemPathID(item.content_id)}/match/apply`, {
        method: "POST",
        body: JSON.stringify({ provider_ids: providerIds }),
      });
    },
    onSuccess: async (_, { item }) => {
      toast.success("Match applied successfully");

      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["items", "detail", item.content_id] }),
        queryClient.invalidateQueries({
          queryKey: ["catalog", "items", item.content_id, "detail"],
        }),
        queryClient.invalidateQueries({ queryKey: ["items", "watchDetail", item.content_id] }),
        queryClient.invalidateQueries({ queryKey: adminKeys.staleMediaIDs() }),
        queryClient.invalidateQueries({ queryKey: adminKeys.unmatchedItems() }),
      ]);

      if (item.type === "series") {
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: episodeKeys.seasons(item.content_id) }),
          queryClient.invalidateQueries({
            queryKey: ["catalog", "series", item.content_id, "seasons"],
          }),
          queryClient.invalidateQueries({ queryKey: episodeKeys.all }),
        ]);
      } else if ((item.type === "season" || item.type === "episode") && item.series_id) {
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: ["items", "detail", item.series_id] }),
          queryClient.invalidateQueries({
            queryKey: ["catalog", "items", item.series_id, "detail"],
          }),
          queryClient.invalidateQueries({ queryKey: episodeKeys.seasons(item.series_id) }),
          queryClient.invalidateQueries({
            queryKey: ["catalog", "series", item.series_id, "seasons"],
          }),
          queryClient.invalidateQueries({ queryKey: episodeKeys.all }),
        ]);
        if (item.season_number != null) {
          await queryClient.invalidateQueries({
            queryKey: episodeKeys.bySeason(item.series_id, item.season_number),
          });
          await queryClient.invalidateQueries({
            queryKey: episodeKeys.seasonDetail(item.series_id, item.season_number),
          });
          await queryClient.invalidateQueries({
            queryKey: catalogKeys.seasonEpisodes(item.series_id, item.season_number),
          });
          await queryClient.invalidateQueries({
            queryKey: catalogKeys.seasonDetail(item.series_id, item.season_number),
          });
        }
      }
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to apply match");
    },
  });
}

// --- Image selector hooks ---

export function useItemImages(contentId: string | undefined, enabled = true) {
  return useQuery({
    queryKey: adminKeys.itemImages(contentId!),
    queryFn: () => api<ItemImagesResponse>(`/admin/items/${itemPathID(contentId!)}/images`),
    enabled: !!contentId && enabled,
    staleTime: 5 * 60_000,
  });
}

type ApplyImageItem = Pick<ItemDetail, "content_id" | "type" | "series_id" | "season_number">;

export function useApplyItemImage() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async ({
      item,
      request,
    }: {
      item: ApplyImageItem;
      request: ApplyItemImageRequest;
    }) =>
      api<ApplyItemImageResponse>(
        `/admin/items/${itemPathID(item.content_id)}/images/apply`,
        {
          method: "POST",
          body: JSON.stringify(request),
        },
      ),
    onSuccess: async (_, { item }) => {
      toast.success("Image applied successfully");

      await Promise.all([
        queryClient.invalidateQueries({ queryKey: adminKeys.itemImages(item.content_id) }),
        queryClient.invalidateQueries({ queryKey: ["items", "detail", item.content_id] }),
        queryClient.invalidateQueries({
          queryKey: ["catalog", "items", item.content_id, "detail"],
        }),
        queryClient.invalidateQueries({ queryKey: ["items", "watchDetail", item.content_id] }),
        queryClient.invalidateQueries({ queryKey: catalogKeys.all }),
        queryClient.invalidateQueries({ queryKey: sectionKeys.all }),
      ]);

      // Cascade for seasons/episodes to parent series.
      if ((item.type === "season" || item.type === "episode") && item.series_id) {
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: ["items", "detail", item.series_id] }),
          queryClient.invalidateQueries({
            queryKey: ["catalog", "items", item.series_id, "detail"],
          }),
          queryClient.invalidateQueries({ queryKey: episodeKeys.seasons(item.series_id) }),
          queryClient.invalidateQueries({
            queryKey: ["catalog", "series", item.series_id, "seasons"],
          }),
        ]);
      }
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to apply image");
    },
  });
}
