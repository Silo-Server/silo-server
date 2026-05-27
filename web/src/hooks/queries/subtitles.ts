import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { api } from "@/api/client";
import type {
  DownloadedSubtitle,
  SubtitleDownloadRequest,
  SubtitleLanguageDetection,
  SubtitleSearchRequest,
  SubtitleSearchResponse,
  SubtitleUploadRequest,
} from "@/api/types";

import { subtitleKeys } from "./keys";

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
