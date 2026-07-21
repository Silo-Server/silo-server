import type { HWAccelInfo } from "@/hooks/queries/admin/system";

// Helpers for the playback.hw_device GPU picker. The setting stores a
// comma-separated render-device list; the UI presents it as per-device
// toggles, with no selection meaning "auto" (server picks the first
// available device). The setting is cluster-wide, so rows carry per-node
// presence info when transcode nodes report their inventories.

export function parseHWDeviceList(value: string | undefined): string[] {
  if (!value) return [];
  return value
    .split(",")
    .map((part) => part.trim())
    .filter((part) => part.length > 0);
}

/**
 * Toggles one device in the stored list, preserving the order devices are
 * detected in so the stored value stays stable regardless of click order.
 */
export function toggleHWDevice(
  value: string | undefined,
  device: string,
  detectedOrder: string[],
): string {
  const selected = new Set(parseHWDeviceList(value));
  if (selected.has(device)) {
    selected.delete(device);
  } else {
    selected.add(device);
  }
  const ordered = detectedOrder.filter((path) => selected.has(path));
  // Preserve selected devices the current detection pass doesn't list (e.g.
  // a temporarily unplugged GPU) rather than silently dropping them.
  for (const path of selected) {
    if (!detectedOrder.includes(path)) ordered.push(path);
  }
  return ordered.join(",");
}

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
