import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, getAccessToken } from "@/api/client";
import { downloadKeys } from "./keys";
import { toast } from "sonner";

// DownloadFormat mirrors the server's download delivery formats. Phase 0 only
// fulfills `original`; `remux`/`transcode` are advertised by the capability
// endpoint once the prepare-to-file pipeline ships.
export type DownloadFormat = "original" | "remux" | "transcode";

interface DownloadResponse {
  id: string;
  content_id: string;
  episode_id?: string;
  batch_id?: string;
  device_id?: string;
  media_file_id: number;
  file_size: number;
  bytes_sent: number;
  kind: string;
  status: string;
  format: DownloadFormat;
  created_at: string;
  completed_at?: string;
}

interface CreateDownloadRequest {
  content_id: string;
  episode_id?: string;
  file_id?: number;
  format?: DownloadFormat;
  series?: boolean;
}

// DownloadCapability is the GET /downloads/capability payload used for feature
// detection (which formats are available and transcode gating).
export interface DownloadCapability {
  enabled: boolean;
  download_allowed: boolean;
  formats: DownloadFormat[];
  transcode_enabled: boolean;
  transcode_user_allowed: boolean;
}

export function useDownloadCapability(enabled = true) {
  return useQuery({
    queryKey: downloadKeys.capability(),
    queryFn: () => api<DownloadCapability>("/downloads/capability"),
    enabled,
  });
}

export function useCreateDownload() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: CreateDownloadRequest) =>
      api<DownloadResponse | { downloads: DownloadResponse[] }>("/downloads", {
        method: "POST",
        body: JSON.stringify(req),
      }),
    onSuccess: (_data, req) => {
      toast.success(req.series ? "Series download queued" : "Download queued");
      qc.invalidateQueries({ queryKey: downloadKeys.all });
    },
    onError: () => {
      toast.error("Failed to start download");
    },
  });
}

export function useDeleteDownload() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api(`/downloads/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: downloadKeys.all });
    },
  });
}

/**
 * Build a direct download URL for a specific file.
 * Uses a token query param since browsers can't set auth headers on navigations.
 * `format` is optional and defaults to the original file server-side.
 */
export function buildDirectDownloadUrl(fileId: number, format?: DownloadFormat): string {
  const token = getAccessToken();
  const params = new URLSearchParams({ file_id: String(fileId) });
  if (format) params.set("format", format);
  if (token) params.set("token", token);
  return `/api/v1/direct-download?${params.toString()}`;
}
