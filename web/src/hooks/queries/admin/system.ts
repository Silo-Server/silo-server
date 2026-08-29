import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { SystemResources } from "@/api/types";
import { adminKeys } from "../keys";

/**
 * How often the API host's own sample is re-read. The sampler publishes every
 * few seconds and the read costs nothing (it returns an already-published
 * snapshot), so this is set by how live an operator expects a resource panel to
 * feel, not by what the server can afford.
 */
const SYSTEM_RESOURCES_REFRESH_MS = 15_000;

export interface BuildInfo {
  display: string;
  revision: string;
  dirty: boolean;
  vcs_time: string;
  build_number?: number;
  built_at?: string;
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

/**
 * The API host's own resource sample. `retry: false` because a server predating
 * the endpoint 404s and there is nothing to retry into — the caller renders the
 * same "not being sampled" state it uses for a non-Linux host.
 */
export function useSystemResources(enabled = true) {
  return useQuery({
    queryKey: adminKeys.systemResources(),
    queryFn: () => api<SystemResources>("/admin/system/resources"),
    refetchInterval: SYSTEM_RESOURCES_REFRESH_MS,
    staleTime: SYSTEM_RESOURCES_REFRESH_MS,
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
