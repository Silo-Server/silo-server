import type { StreamNode } from "@/api/types";
import type { HWAccelInfo } from "@/hooks/queries/admin/system";

// Helpers for the playback.hw_device GPU picker. The setting stores a
// comma-separated render-device list; the UI presents it as per-device
// toggles, with no selection meaning "auto" (server picks the first
// available device). The setting is cluster-wide, so rows carry per-node
// presence info when transcode nodes report their inventories.
//
// Parsing and toggling that list is the same problem as editing one node's
// hw_device_override, so both live in @/lib/hwDevices and are re-exported here
// for the callers (and tests) that already know them by these names.

import { parseHWDeviceList, toggleHWDevice } from "@/lib/hwDevices";

export { parseHWDeviceList, toggleHWDevice };

export interface HWDeviceRow {
  path: string;
  description: string;
  /** Present in the primary detection result. */
  detected: boolean;
  /** Names/URLs of responding nodes whose inventory lacks this device. */
  missingOnNodes: string[];
}

/**
 * Builds the picker rows: the union of detected devices and configured
 * entries, so configured-but-missing devices stay visible (and deselectable)
 * even when detection returns nothing or an older node omits
 * render_device_details.
 */
export function buildHWDeviceRows(
  detection: HWAccelInfo | undefined,
  configured: string | undefined,
): HWDeviceRow[] {
  const detected = detectedDevices(detection);
  const respondingNodes = (detection?.nodes ?? []).filter((node) => !node.error);
  const missingOn = (path: string) =>
    respondingNodes
      .filter((node) => !(node.render_devices ?? []).includes(path))
      .map((node) => node.node_name || node.node_url);

  const rows: HWDeviceRow[] = detected.map((device) => ({
    path: device.path,
    description: device.description,
    detected: true,
    missingOnNodes: missingOn(device.path),
  }));
  for (const path of parseHWDeviceList(configured)) {
    if (rows.some((row) => row.path === path)) continue;
    rows.push({
      path,
      description: "Configured device not detected",
      detected: false,
      missingOnNodes: missingOn(path),
    });
  }
  return rows;
}

/**
 * True when more than one transcode node responded and their render-device
 * inventories differ — the cluster-wide hw_device value is only safe for
 * paths present on every node, so the UI shows a warning.
 */
export function nodeInventoriesDiverge(detection: HWAccelInfo | undefined): boolean {
  const inventories = (detection?.nodes ?? [])
    .filter((node) => !node.error)
    .map((node) => [...(node.render_devices ?? [])].sort().join(","));
  return inventories.length > 1 && new Set(inventories).size > 1;
}

// Helpers for the "Generate chapter thumbnails on" select. Two of its three
// modes need a transcode node to run on: `transcode_nodes_only` fails every
// extraction without one, and `prefer_transcode_nodes` silently degrades to
// local — so both are offered only when the node pool can actually serve them.

export const CHAPTER_THUMBNAIL_EXECUTION_DEFAULT = "local";

const NODE_BACKED_CHAPTER_THUMBNAIL_MODES = ["prefer_transcode_nodes", "transcode_nodes_only"];

/**
 * True when at least one transcode node could take an extraction. Mirrors the
 * server's reservation rule (internal/chapterthumbs reserveRemoteNode): a node
 * counts only while it is both enabled and healthy.
 */
export function hasUsableTranscodeNode(nodes: StreamNode[] | undefined): boolean {
  return (nodes ?? []).some((node) => node.type === "transcode" && node.enabled && node.healthy);
}

export interface ChapterThumbnailExecutionOption {
  value: string;
  label: string;
  disabled: boolean;
}

/**
 * Builds the execution options, disabling the node-backed modes when no
 * transcode node is available. The saved mode is never disabled: pre-configuring
 * nodes that have not joined yet is legitimate, and a persisted node-only value
 * has to stay visible and editable so the admin can switch back off it.
 */
export function chapterThumbnailExecutionOptions(
  current: string,
  transcodeNodeAvailable: boolean,
): ChapterThumbnailExecutionOption[] {
  return [
    { value: CHAPTER_THUMBNAIL_EXECUTION_DEFAULT, label: "This server" },
    { value: "prefer_transcode_nodes", label: "Transcode nodes when available" },
    { value: "transcode_nodes_only", label: "Transcode nodes only" },
  ].map((option) => ({
    ...option,
    disabled:
      !transcodeNodeAvailable &&
      option.value !== current &&
      NODE_BACKED_CHAPTER_THUMBNAIL_MODES.includes(option.value),
  }));
}

function detectedDevices(
  detection: HWAccelInfo | undefined,
): { path: string; description: string }[] {
  if (!detection) return [];
  if (detection.render_device_details && detection.render_device_details.length > 0) {
    return detection.render_device_details;
  }
  // Older nodes report only render_devices paths.
  return (detection.render_devices ?? []).map((path) => ({ path, description: "GPU" }));
}

/** The `playback.hw_accel` choices, in the order the select shows them. */
export const HW_ACCEL_OPTIONS = [
  { value: "auto", label: "Auto" },
  { value: "qsv", label: "Intel Quick Sync (QSV)" },
  { value: "vaapi", label: "VA-API" },
  { value: "nvenc", label: "NVIDIA NVENC" },
  { value: "videotoolbox", label: "VideoToolbox (macOS)" },
  { value: "none", label: "Software" },
];

/** Human name for a resolved hardware-acceleration backend. */
export function formatResolved(resolved: string): string {
  return HW_ACCEL_OPTIONS.find((option) => option.value === resolved)?.label ?? resolved;
}

/**
 * One-line detection result, e.g. "Detected VA-API on renderD128". Returns
 * undefined while nothing has been probed yet so the caller can show its own
 * "detecting" state instead of an empty phrase.
 */
export function describeDetection(detection: HWAccelInfo | undefined): string | undefined {
  if (!detection) return undefined;
  if (detection.resolved === "none") return "No supported graphics hardware found";
  const device = detection.render_devices?.[0];
  const onNode = detection.source === "transcode_node" ? " (transcode node)" : "";
  return `Detected ${formatResolved(detection.resolved)}${device ? ` on ${device}` : ""}${onNode}`;
}

/**
 * Whether the probed executor inventory contains a validated hardware tone
 * mapper. The server only lists capabilities it has actually verified, so an
 * empty or missing list means hardware HDR-to-SDR is not available here.
 */
export function hasHardwareToneMapCapability(detection: HWAccelInfo | undefined): boolean {
  return (detection?.tone_map_capabilities ?? []).some((cap) => cap.mode === "hardware");
}
