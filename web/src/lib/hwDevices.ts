// Shared parsing and toggling for the comma-separated render-device lists the
// server stores: the cluster-wide `playback.hw_device` setting and a node's
// `hw_device_override`. Both are edited as a per-device picker, and both must
// round-trip a path the current inventory does not list, so the rules live here
// rather than once per page. Row building stays with each page: the cluster
// picker rows carry per-node presence, a node's rows carry its own inventory.

/** Split a stored comma-separated device list into its paths. */
export function parseHWDeviceList(value: string | null | undefined): string[] {
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
  value: string | null | undefined,
  device: string,
  detectedOrder: readonly string[],
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
