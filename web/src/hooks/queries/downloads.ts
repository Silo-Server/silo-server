import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { fetchDownloadCapability, deleteDownloadEntry } from "@/api/v2/downloadRegistry";
import { downloadKeys } from "./keys";

export type DownloadQuality = "original" | "20mbps" | "10mbps" | "5mbps" | "2mbps" | "1mbps";
export type DownloadDeliveryFormat = "original" | "remux" | "transcode";

/** One quality preset: its video bitrate cap and the tallest output it can produce. */
export interface DownloadQualityOption {
  preset: DownloadQuality;
  bitrate_kbps?: number;
  max_height?: number;
}

export interface DownloadCapability {
  enabled: boolean;
  download_allowed: boolean;
  quality_presets: DownloadQuality[];
  quality_options: DownloadQualityOption[];
  transcode_enabled: boolean;
  transcode_user_allowed: boolean;
  season_download: boolean;
  series_monitoring: boolean;
  monitoring_modes?: string[];
  proxy_delivery: boolean;
}

export function useDownloadCapability(enabled = true) {
  return useQuery({
    queryKey: downloadKeys.capability(),
    queryFn: async () => (await fetchDownloadCapability()) as DownloadCapability,
    enabled,
  });
}

export function useDeleteDownload() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: deleteDownloadEntry,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: downloadKeys.all });
    },
  });
}
