import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { adminKeys } from "../keys";

export interface BuildInfo {
  display: string;
  revision: string;
  dirty: boolean;
  vcs_time: string;
  available: boolean;
}

export interface HWAccelInfo {
  resolved: string;
  render_devices: string[];
  intel_detected: boolean;
  source: "local" | "transcode_node";
  node_url?: string;
}

export function useBuildInfo(enabled = true) {
  return useQuery({
    queryKey: adminKeys.buildInfo(),
    queryFn: () => api<BuildInfo>("/admin/system/build"),
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
    enabled,
  });
}

export function useHWAccelDetection(enabled = true) {
  return useQuery({
    queryKey: adminKeys.hwAccel(),
    queryFn: () => api<HWAccelInfo>("/admin/system/hw-accel"),
    staleTime: 60_000,
    retry: false,
    enabled,
  });
}
