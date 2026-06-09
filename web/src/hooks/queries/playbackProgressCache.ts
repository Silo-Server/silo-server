import type { QueryClient } from "@tanstack/react-query";
import type { ItemDetail, LeafItemUserData, ProgressListResponse, WatchDetail } from "@/api/types";
import { progressKeys } from "./keys";

export interface PlaybackProgressSnapshot {
  contentId: string;
  positionSeconds: number;
  durationSeconds?: number;
  lastFileId?: number | null;
  lastResolution?: string;
  lastHDR?: boolean;
  lastCodecVideo?: string;
  lastEditionKey?: string;
  updatedAt?: string;
}

function mergeLeafProgress(
  existing: LeafItemUserData | undefined,
  snapshot: PlaybackProgressSnapshot,
): LeafItemUserData {
  const rawPosition = Math.max(0, snapshot.positionSeconds);
  const durationSeconds = snapshot.durationSeconds ?? existing?.duration_seconds;
  // Mirror the server model: played is a one-way latch (a rewatch keeps the
  // watched badge), completion clears the resume point, and any nonzero
  // position is an active resume point.
  const completedNow =
    durationSeconds != null && durationSeconds > 0 && rawPosition >= durationSeconds;
  const played = existing?.played === true || completedNow;
  const positionSeconds = completedNow ? 0 : rawPosition;

  return {
    played,
    is_in_progress: positionSeconds > 0,
    position_seconds: positionSeconds,
    duration_seconds: durationSeconds,
    last_file_id: snapshot.lastFileId ?? existing?.last_file_id,
    last_resolution: snapshot.lastResolution ?? existing?.last_resolution,
    last_hdr: snapshot.lastHDR ?? existing?.last_hdr,
    last_codec_video: snapshot.lastCodecVideo ?? existing?.last_codec_video,
    last_edition_key: snapshot.lastEditionKey ?? existing?.last_edition_key,
  };
}

function applySnapshotToItemDetail(
  existing: ItemDetail | undefined,
  snapshot: PlaybackProgressSnapshot,
): ItemDetail | undefined {
  if (!existing) return existing;

  const currentUserData =
    existing.user_data && "position_seconds" in existing.user_data ? existing.user_data : undefined;

  return {
    ...existing,
    user_data: mergeLeafProgress(currentUserData, snapshot),
  };
}

function applySnapshotToWatchDetail(
  existing: WatchDetail | undefined,
  snapshot: PlaybackProgressSnapshot,
): WatchDetail | undefined {
  if (!existing) return existing;

  return {
    ...existing,
    user_data: mergeLeafProgress(existing.user_data, snapshot),
  };
}

export function applyPlaybackProgressToCache(
  queryClient: QueryClient,
  snapshot: PlaybackProgressSnapshot,
) {
  for (const [queryKey, existing] of queryClient.getQueriesData<ItemDetail>({
    queryKey: ["catalog", "items", snapshot.contentId, "detail"],
  })) {
    queryClient.setQueryData<ItemDetail>(queryKey, applySnapshotToItemDetail(existing, snapshot));
  }
  for (const [queryKey, existing] of queryClient.getQueriesData<ItemDetail>({
    queryKey: ["items", "detail", snapshot.contentId],
  })) {
    queryClient.setQueryData<ItemDetail>(queryKey, applySnapshotToItemDetail(existing, snapshot));
  }
  for (const [queryKey, existing] of queryClient.getQueriesData<WatchDetail>({
    queryKey: ["items", "watchDetail", snapshot.contentId],
  })) {
    queryClient.setQueryData<WatchDetail>(queryKey, applySnapshotToWatchDetail(existing, snapshot));
  }

  const updatedAt = snapshot.updatedAt ?? new Date().toISOString();
  for (const [queryKey, existing] of queryClient.getQueriesData<ProgressListResponse>({
    queryKey: progressKeys.all,
  })) {
    if (!existing) continue;

    const nextProgress = existing.progress.map((entry) => {
      if (entry.media_item_id !== snapshot.contentId) return entry;
      const rawPosition = Math.max(0, snapshot.positionSeconds);
      const duration = snapshot.durationSeconds ?? entry.duration_seconds;
      // Mirror the server model: completion latches and clears the resume point.
      const completedNow = duration > 0 && rawPosition >= duration;
      return {
        ...entry,
        position_seconds: completedNow ? 0 : rawPosition,
        duration_seconds: duration,
        completed: entry.completed || completedNow,
        updated_at: updatedAt,
      };
    });

    queryClient.setQueryData<ProgressListResponse>(queryKey, {
      ...existing,
      progress: nextProgress,
    });
  }
}
