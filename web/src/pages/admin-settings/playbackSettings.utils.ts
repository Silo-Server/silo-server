import type { NodeDetectedBackend, StreamNode } from "@/api/types";
import { t } from "@/i18n";
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
      description: t("web.admin.settings.playback.devices.notDetected"),
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

// Helpers for the "Generate chapter thumbnails on" and "Generate seek previews
// on" selects, which share three modes. Two of them need a transcode node to
// run on: `transcode_nodes_only` makes nothing without one, and
// `prefer_transcode_nodes` silently degrades to local — so both are offered
// only when the node pool can actually serve them.

export const IMAGE_EXECUTION_DEFAULT = "local";

export const TRICKPLAY_NODE_CAPACITY_KEY = "playback.trickplay_node_capacity";

/**
 * Whether a Preview images advanced key is on screen. A node's seek preview
 * capacity matters only once previews may run on one; an unsaved edit keeps it
 * shown so a hidden field never holds back the save.
 */
export function showsPreviewKey(
  key: string,
  execution: string,
  isDirty: (key: string) => boolean,
): boolean {
  return (
    key !== TRICKPLAY_NODE_CAPACITY_KEY ||
    execution !== IMAGE_EXECUTION_DEFAULT ||
    isDirty(TRICKPLAY_NODE_CAPACITY_KEY)
  );
}

const NODE_BACKED_IMAGE_MODES = ["prefer_transcode_nodes", "transcode_nodes_only"];

/**
 * True when at least one transcode node could take an extraction. Mirrors the
 * server's reservation rule (internal/chapterthumbs reserveRemoteNode): a node
 * counts only while it is both enabled and healthy.
 */
export function hasUsableTranscodeNode(nodes: StreamNode[] | undefined): boolean {
  return (nodes ?? []).some((node) => node.type === "transcode" && node.enabled && node.healthy);
}

/**
 * True when a node can extract seek previews with its current capability
 * snapshot. Mirrors the server's rule (internal/trickplay makesTrickplay): only
 * a node advertising trickplay_extract_v2, which reads the tone-map source
 * signal, is sent work.
 */
export function hasUsableTrickplayNode(nodes: StreamNode[] | undefined): boolean {
  return (nodes ?? []).some(
    (node) =>
      node.type === "transcode" &&
      node.enabled &&
      node.healthy &&
      node.capabilities?.transport_features?.includes("trickplay_extract_v2") &&
      (node.advertised_capabilities_hash === undefined ||
        (node.advertised_capabilities_hash !== "" &&
          node.advertised_capabilities_hash === node.capabilities_hash)),
  );
}

export interface ImageExecutionOption {
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
export function imageExecutionOptions(
  current: string,
  transcodeNodeAvailable: boolean,
): ImageExecutionOption[] {
  return gateNodeBackedModes(
    [
      {
        value: IMAGE_EXECUTION_DEFAULT,
        label: t("web.admin.settings.playback.execution.thisServer"),
      },
      {
        value: "prefer_transcode_nodes",
        label: t("web.admin.settings.playback.execution.nodesWhenAvailable"),
      },
      {
        value: "transcode_nodes_only",
        label: t("web.admin.settings.playback.execution.nodesOnly"),
      },
    ],
    current,
    transcodeNodeAvailable,
  );
}

/** Mirrors `subtitles.sync_execution` in `adminSettingDefaults`. */
export const SUBTITLE_SYNC_EXECUTION_DEFAULT = "prefer_transcode_nodes";

/**
 * Where subtitle sync analyzes audio. It shares the chapter thumbnail modes
 * and the same rule for disabling node-backed modes without a node.
 */
export function subtitleSyncExecutionOptions(
  current: string,
  transcodeNodeAvailable: boolean,
): ImageExecutionOption[] {
  return gateNodeBackedModes(
    [
      { value: "local", label: t("web.admin.settings.playback.execution.localServer") },
      {
        value: "prefer_transcode_nodes",
        label: t("web.admin.settings.playback.execution.preferNodes"),
      },
      {
        value: "transcode_nodes_only",
        label: t("web.admin.settings.playback.execution.nodesOnly"),
      },
    ],
    current,
    transcodeNodeAvailable,
  );
}

export function isNodeBackedExecution(mode: string): boolean {
  return NODE_BACKED_IMAGE_MODES.includes(mode);
}

function gateNodeBackedModes(
  options: { value: string; label: string }[],
  current: string,
  transcodeNodeAvailable: boolean,
): ImageExecutionOption[] {
  return options.map((option) => ({
    ...option,
    disabled:
      !transcodeNodeAvailable &&
      option.value !== current &&
      NODE_BACKED_IMAGE_MODES.includes(option.value),
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
  return (detection.render_devices ?? []).map((path) => ({
    path,
    description: t("web.admin.settings.playback.devices.gpu"),
  }));
}

/**
 * The `playback.hw_accel` choices, in the order the select shows them. The
 * backends are product names; "Auto" and "Software" are read when the label
 * is, so they follow the interface language.
 */
export const HW_ACCEL_OPTIONS = [
  {
    value: "auto",
    get label() {
      return t("web.admin.settings.playback.hwAccel.auto");
    },
  },
  { value: "qsv", label: "Intel Quick Sync (QSV)" },
  { value: "vaapi", label: "VA-API" },
  { value: "nvenc", label: "NVIDIA NVENC" },
  { value: "videotoolbox", label: "VideoToolbox (macOS)" },
  {
    value: "none",
    get label() {
      return t("web.admin.settings.playback.hwAccel.software");
    },
  },
];

/** Human name for a resolved hardware-acceleration backend. */
export function formatResolved(resolved: string): string {
  return HW_ACCEL_OPTIONS.find((option) => option.value === resolved)?.label ?? resolved;
}

/**
 * The probe entry for the backend detection resolved to, if one ran. The
 * server reports an explicitly configured backend as resolved even when it was
 * never verified (VideoToolbox in a Linux container, NVENC with no NVIDIA
 * device), so `resolved` alone proves nothing.
 */
function resolvedProbe(detection: HWAccelInfo): NodeDetectedBackend | undefined {
  return detection.detected_backends?.find(
    (entry) => entry.backend.trim().toLowerCase() === detection.resolved,
  );
}

/** Whether detection verified working hardware for the backend it resolved to. */
export function hardwareVerified(detection: HWAccelInfo | undefined): detection is HWAccelInfo {
  return Boolean(detection && detection.resolved !== "none" && resolvedProbe(detection)?.verified);
}

/**
 * One-line detection result, e.g. "Detected VA-API on renderD128". Returns
 * undefined while nothing has been probed yet so the caller can show its own
 * "detecting" state instead of an empty phrase. Pair it with hardwareVerified
 * for the tone: every result other than "Detected …" is a warning.
 */
export function describeDetection(detection: HWAccelInfo | undefined): string | undefined {
  if (!detection) return undefined;
  if (detection.resolved === "none") return t("web.admin.settings.playback.hwAccel.noneFound");
  const backend = formatResolved(detection.resolved);
  const node = detection.source === "transcode_node";
  const probe = resolvedProbe(detection);
  if (probe?.verified) {
    const device = probe.device || detection.render_devices?.[0];
    if (node) {
      return device
        ? t("web.admin.settings.playback.hwAccel.detectedOnDeviceNode", { backend, device })
        : t("web.admin.settings.playback.hwAccel.detectedNode", { backend });
    }
    return device
      ? t("web.admin.settings.playback.hwAccel.detectedOnDevice", { backend, device })
      : t("web.admin.settings.playback.hwAccel.detected", { backend });
  }
  // Skipped: none of the configured devices is accessible, so nothing ran.
  if (probe?.skipped) {
    return node
      ? t("web.admin.settings.playback.hwAccel.notAccessibleNode", { backend })
      : t("web.admin.settings.playback.hwAccel.notAccessible", { backend });
  }
  if (probe) {
    return node
      ? t("web.admin.settings.playback.hwAccel.checkFailedNode", { backend })
      : t("web.admin.settings.playback.hwAccel.checkFailed", { backend });
  }
  // macOS always reports a VideoToolbox probe, so no entry means the server
  // is not running natively on a Mac (Docker on macOS runs a Linux VM).
  if (detection.resolved === "videotoolbox") {
    return node
      ? t("web.admin.settings.playback.hwAccel.needsNativeMacNode", { backend })
      : t("web.admin.settings.playback.hwAccel.needsNativeMac", { backend });
  }
  return node
    ? t("web.admin.settings.playback.hwAccel.notFoundNode", { backend })
    : t("web.admin.settings.playback.hwAccel.notFound", { backend });
}

/**
 * Whether the probed executor inventory contains a validated hardware tone
 * mapper. The server only lists capabilities it has actually verified, so an
 * empty or missing list means hardware HDR-to-SDR is not available here.
 */
export function hasHardwareToneMapCapability(detection: HWAccelInfo | undefined): boolean {
  return (detection?.tone_map_capabilities ?? []).some((cap) => cap.mode === "hardware");
}
