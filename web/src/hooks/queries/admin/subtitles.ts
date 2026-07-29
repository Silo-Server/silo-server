import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api, apiDownload } from "@/api/client";
import type {
  AdminDownloadedSubtitle,
  AdminDownloadedSubtitlesFilters,
  AdminDownloadedSubtitlesResponse,
  AdminUpdateDownloadedSubtitleRequest,
  SubtitleProviderConfig,
  SubtitleProviderUpdateRequest,
  SubtitleProviderTestRequest,
  SubtitleProviderTestResponse,
} from "@/api/types";
import { adminKeys } from "../keys";
import { toast } from "sonner";

const ADMIN_STALE_TIME = 30_000;

function buildDownloadedSubtitlesQuery(filters: AdminDownloadedSubtitlesFilters): string {
  const params = new URLSearchParams();
  if (filters.provider) params.set("provider", filters.provider);
  if (filters.language) params.set("language", filters.language);
  if (filters.userId != null) params.set("user_id", String(filters.userId));
  if (filters.mediaFileId != null) params.set("media_file_id", String(filters.mediaFileId));
  if (filters.q) params.set("q", filters.q);
  params.set("limit", String(filters.limit ?? 50));
  params.set("offset", String(filters.offset ?? 0));
  const query = params.toString();
  return query ? `/admin/subtitles?${query}` : "/admin/subtitles";
}

export function useAdminDownloadedSubtitles(filters: AdminDownloadedSubtitlesFilters) {
  return useQuery({
    queryKey: adminKeys.downloadedSubtitles(filters),
    queryFn: () =>
      api<AdminDownloadedSubtitlesResponse>(buildDownloadedSubtitlesQuery(filters)).then(
        (data) => data ?? { subtitles: [], total: 0, uploads: 0, provider_downloads: 0 },
      ),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useAdminUpdateDownloadedSubtitle() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, patch }: { id: number; patch: AdminUpdateDownloadedSubtitleRequest }) =>
      api<{ subtitle: AdminDownloadedSubtitle }>(`/admin/subtitles/${id}`, {
        method: "PATCH",
        body: JSON.stringify(patch),
      }),
    onSuccess: () => {
      toast.success("Subtitle updated");
      queryClient.invalidateQueries({ queryKey: ["admin", "downloadedSubtitles"] });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to update subtitle");
    },
  });
}

export function useAdminDeleteDownloadedSubtitle() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) =>
      api<void>(`/admin/subtitles/${id}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      toast.success("Subtitle deleted");
      queryClient.invalidateQueries({ queryKey: ["admin", "downloadedSubtitles"] });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to delete subtitle");
    },
  });
}

export async function downloadAdminSubtitle(subtitle: AdminDownloadedSubtitle): Promise<void> {
  const base = subtitle.release_name?.trim() || `subtitle-${subtitle.id}`;
  const filename = base.includes(".") ? base : `${base}.${subtitle.format}`;
  await apiDownload(`/admin/subtitles/${subtitle.id}/download`, filename);
}

export function useSubtitleProviders() {
  return useQuery({
    queryKey: adminKeys.subtitleProviders(),
    queryFn: () => api<{ providers: SubtitleProviderConfig[] }>("/admin/subtitle-providers"),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useUpdateSubtitleProvider() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      provider,
      config,
    }: {
      provider: string;
      config: SubtitleProviderUpdateRequest;
    }) =>
      api<{ status: string }>(`/admin/subtitle-providers/${provider}`, {
        method: "PUT",
        body: JSON.stringify(config),
      }),
    onSuccess: async () => {
      toast.success("Provider settings saved");
      await queryClient.invalidateQueries({
        queryKey: adminKeys.subtitleProviders(),
      });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save provider settings");
    },
  });
}

export function useTestSubtitleProvider() {
  return useMutation({
    mutationFn: ({ provider, config }: { provider: string; config: SubtitleProviderTestRequest }) =>
      api<SubtitleProviderTestResponse>(`/admin/subtitle-providers/${provider}/test`, {
        method: "POST",
        body: JSON.stringify(config),
      }),
  });
}
