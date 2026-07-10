import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { api } from "@/api/client";
import type { PrePlaySubtitleSelection } from "@/player/types";
import { derivePersistedSubtitleMode } from "@/player/utils/subtitleMode";
import type {
  DownloadedSubtitle,
  SubtitleDownloadRequest,
  SubtitleLanguageDetection,
  SubtitleSearchRequest,
  SubtitleSearchResponse,
  SubtitleUploadRequest,
} from "@/api/types";

import { itemKeys, subtitleKeys } from "./keys";

interface DownloadSubtitleResponse {
  subtitle: DownloadedSubtitle;
}

function buildSubtitleUploadFormData(request: SubtitleUploadRequest): FormData {
  const form = new FormData();
  form.set("media_file_id", String(request.media_file_id));
  if (request.language) {
    form.set("language", request.language);
  }
  if (request.language_override) {
    form.set("language_override", "true");
  }
  form.set("file", request.file);
  if (request.release_name) {
    form.set("release_name", request.release_name);
  }
  if (request.hearing_impaired) {
    form.set("hearing_impaired", "true");
  }
  return form;
}

function buildSubtitleDetectFormData(file: File, language?: string): FormData {
  const form = new FormData();
  form.set("file", file);
  if (language) {
    form.set("language", language);
  }
  return form;
}

export async function fetchDownloadedSubtitles(
  mediaFileId: number,
  options?: RequestInit,
): Promise<DownloadedSubtitle[]> {
  const response = await api<{ subtitles: DownloadedSubtitle[] }>(
    `/subtitles/${mediaFileId}`,
    options,
  );
  return response.subtitles ?? [];
}

export async function searchSubtitles(
  request: SubtitleSearchRequest,
  options?: RequestInit,
): Promise<SubtitleSearchResponse> {
  return api<SubtitleSearchResponse>("/subtitles/search", {
    ...options,
    method: "POST",
    body: JSON.stringify(request),
  });
}

export async function downloadSubtitle(
  request: SubtitleDownloadRequest,
  options?: RequestInit,
): Promise<DownloadSubtitleResponse> {
  return api<DownloadSubtitleResponse>("/subtitles/download", {
    ...options,
    method: "POST",
    body: JSON.stringify(request),
  });
}

export async function uploadSubtitle(
  request: SubtitleUploadRequest,
  options?: RequestInit,
): Promise<DownloadSubtitleResponse> {
  return api<DownloadSubtitleResponse>("/subtitles/upload", {
    ...options,
    method: "POST",
    body: buildSubtitleUploadFormData(request),
  });
}

export async function detectSubtitleLanguage(
  file: File,
  language?: string,
  options?: RequestInit,
): Promise<SubtitleLanguageDetection> {
  return api<SubtitleLanguageDetection>("/subtitles/detect-language", {
    ...options,
    method: "POST",
    body: buildSubtitleDetectFormData(file, language),
  });
}

export function useDownloadedSubtitles(mediaFileId: number | undefined) {
  return useQuery({
    queryKey: mediaFileId != null ? subtitleKeys.downloaded(mediaFileId) : subtitleKeys.all,
    queryFn: () => fetchDownloadedSubtitles(mediaFileId!),
    enabled: mediaFileId != null,
  });
}

// Subtitle preferences feed the effective defaults on item details; a
// series-keyed preference feeds every episode's detail, so invalidate broadly.
function invalidateItemDetails(queryClient: ReturnType<typeof useQueryClient>): Promise<unknown> {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: itemKeys.details() }),
    queryClient.invalidateQueries({ queryKey: ["catalog", "items"] }),
  ]);
}

/**
 * Clears the persisted subtitle override (saved when a track is manually
 * selected during playback) so profile-level auto selection applies again.
 * Keyed by the movie's content ID or the episode's series ID.
 */
export function useDeleteSubtitlePreference() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (prefId: string) => api<void>(`/subtitle-prefs/${prefId}`, { method: "DELETE" }),
    onSuccess: () => invalidateItemDetails(queryClient),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to reset subtitle preference");
    },
  });
}

interface SetSubtitlePreferenceInput {
  /** Movie content ID or episode series ID (preferences are series-scoped). */
  prefId: string;
  /** The chosen track, or null to persist "subtitles off". */
  selection: PrePlaySubtitleSelection | null;
  /** Preserve the effective forced-subtitle behavior in the replaced row. */
  showForcedSubtitles?: boolean;
}

/**
 * Persists a pre-play subtitle choice as the item's override — the same
 * "always play this track" (or "off") preference a manual in-player
 * selection saves — so the choice sticks across visits and sessions.
 */
export function useSetSubtitlePreference() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ prefId, selection, showForcedSubtitles }: SetSubtitlePreferenceInput) =>
      api<void>(`/subtitle-prefs/${prefId}`, {
        method: "PUT",
        body: JSON.stringify({
          subtitle_language: selection?.language ?? "",
          subtitle_track_index: selection?.track_index ?? -1,
          subtitle_mode: derivePersistedSubtitleMode(
            selection ? (selection.track_index ?? -1) : null,
          ),
          track_signature: selection
            ? {
                source: selection.source,
                language: selection.language,
                codec: selection.codec,
                label: selection.label,
                forced: selection.forced,
                hearing_impaired: selection.hearing_impaired,
              }
            : null,
          show_forced_subtitles: showForcedSubtitles,
        }),
      }),
    onSuccess: () => invalidateItemDetails(queryClient),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save subtitle preference");
    },
  });
}

export function useDownloadSubtitle() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: SubtitleDownloadRequest) => downloadSubtitle(request),
    onSuccess: async (_response, request) => {
      toast.success("Subtitle downloaded");
      await queryClient.invalidateQueries({
        queryKey: subtitleKeys.downloaded(request.media_file_id),
      });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to download subtitle");
    },
  });
}

export function useUploadSubtitle() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: SubtitleUploadRequest) => uploadSubtitle(request),
    onSuccess: async (_response, request) => {
      toast.success("Subtitle uploaded");
      await queryClient.invalidateQueries({
        queryKey: subtitleKeys.downloaded(request.media_file_id),
      });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to upload subtitle");
    },
  });
}
