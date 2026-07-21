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

export interface RenderDeviceInfo {
  path: string;
  description: string;
}

export interface NodeHWAccel {
  node_url: string;
  node_name?: string;
  resolved?: string;
  render_devices?: string[];
  render_device_details?: RenderDeviceInfo[];
  error?: string;
}

export interface HWAccelInfo {
  resolved: string;
  render_devices: string[];
  render_device_details?: RenderDeviceInfo[];
  intel_detected: boolean;
  source: "local" | "transcode_node";
  node_url?: string;
  /** Per-node inventories when transcode nodes are registered. */
  nodes?: NodeHWAccel[];
}

export function useBuildInfo() {
  return useQuery({
    queryKey: adminKeys.buildInfo(),
    queryFn: () => api<BuildInfo>("/admin/system/build"),
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
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
