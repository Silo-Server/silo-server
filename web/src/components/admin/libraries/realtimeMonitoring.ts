import type { LibraryRealtimeMonitoringEntry, LibraryRealtimeMonitoringState } from "@/api/types";

/** Where the server-wide real-time monitoring switch lives. */
export const REALTIME_MONITORING_SETTINGS_PATH = "/admin/settings/library";

const STATE_LABELS: Record<LibraryRealtimeMonitoringState, string> = {
  server_disabled: "Turned off server-wide",
  library_disabled: "Library is disabled",
  monitoring_off: "Off",
  not_reporting: "Not reporting",
  starting: "Starting",
  monitoring: "Monitoring",
  unsupported_filesystem: "Unsupported filesystem",
  unsupported_platform: "Unsupported platform",
  limit_reached: "Watch limit reached",
  root_unavailable: "Folder unavailable",
  error: "Error",
};

/** A short label for a monitoring state; unknown states read as an error. */
export function realtimeMonitoringStateLabel(state: string): string {
  return STATE_LABELS[state as LibraryRealtimeMonitoringState] ?? STATE_LABELS.error;
}

/**
 * The status line under the library editor's switch: the folder count and
 * backend while monitoring, otherwise the state's label, each followed by the
 * server's detail when it has one.
 */
export function realtimeMonitoringStatusText(entry: LibraryRealtimeMonitoringEntry): string {
  const summary = statusSummary(entry);
  const detail = entry.detail.trim();
  return detail ? `${summary} · ${detail}` : summary;
}

function statusSummary(entry: LibraryRealtimeMonitoringEntry): string {
  if (entry.state !== "monitoring") return realtimeMonitoringStateLabel(entry.state);
  const folderNoun = entry.directories === 1 ? "folder" : "folders";
  const summary = `Monitoring ${entry.directories.toLocaleString()} ${folderNoun}`;
  return entry.backend ? `${summary} (${entry.backend})` : summary;
}

// Monitoring is switched on for these libraries but is not working on a node
// that can see their folders. Starting resolves on its own; the
// settings-derived states are deliberate choices; not_reporting and
// unsupported_platform describe the deployment (no node sees the folders, or
// no node runs Linux) rather than a fault in one library, so they would flag
// every library at once. The editor's status line still shows them.
const NEEDS_ATTENTION = new Set<LibraryRealtimeMonitoringState>([
  "limit_reached",
  "root_unavailable",
  "unsupported_filesystem",
  "error",
]);

/** Whether the libraries list should flag this library's monitoring. */
export function realtimeMonitoringNeedsAttention(
  entry: LibraryRealtimeMonitoringEntry | undefined,
): entry is LibraryRealtimeMonitoringEntry {
  return entry !== undefined && NEEDS_ATTENTION.has(entry.state);
}
