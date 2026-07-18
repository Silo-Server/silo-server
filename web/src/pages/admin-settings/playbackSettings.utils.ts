// Helpers for the playback.hw_device GPU picker. The setting stores a
// comma-separated render-device list; the UI presents it as per-device
// toggles, with no selection meaning "auto" (server picks the first
// available device).

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
export function toggleHWDevice(value: string | undefined, device: string, detectedOrder: string[]): string {
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
