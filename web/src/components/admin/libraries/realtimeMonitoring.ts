import type { LibraryRealtimeMonitoringEntry, LibraryRealtimeMonitoringState } from "@/api/types";
import { t, type MessageKey } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";

/** Where the server-wide real-time monitoring switch lives. */
export const REALTIME_MONITORING_SETTINGS_PATH = "/admin/settings/library";

const STATE_LABELS: Record<LibraryRealtimeMonitoringState, MessageKey> = {
  server_disabled: "web.admin.common.libraryEditor.monitoringState.serverDisabled",
  library_disabled: "web.admin.common.libraryEditor.monitoringState.libraryDisabled",
  monitoring_off: "web.admin.common.libraryEditor.monitoringState.monitoringOff",
  not_reporting: "web.admin.common.libraryEditor.monitoringState.notReporting",
  starting: "web.admin.common.libraryEditor.monitoringState.starting",
  monitoring: "web.admin.common.libraryEditor.monitoringState.monitoring",
  unsupported_filesystem: "web.admin.common.libraryEditor.monitoringState.unsupportedFilesystem",
  unsupported_platform: "web.admin.common.libraryEditor.monitoringState.unsupportedPlatform",
  limit_reached: "web.admin.common.libraryEditor.monitoringState.limitReached",
  root_unavailable: "web.admin.common.libraryEditor.monitoringState.rootUnavailable",
  error: "web.admin.common.libraryEditor.monitoringState.error",
};

/** A short label for a monitoring state; unknown states read as an error. */
export function realtimeMonitoringStateLabel(state: string): string {
  return t(STATE_LABELS[state as LibraryRealtimeMonitoringState] ?? STATE_LABELS.error);
}

/**
 * The status line under the library editor's switch: the folder count and
 * backend while monitoring, otherwise the state's label, each followed by the
 * server's detail when it has one.
 */
export function realtimeMonitoringStatusText(entry: LibraryRealtimeMonitoringEntry): string {
  const summary = statusSummary(entry);
  const detail = entry.detail.trim();
  return detail
    ? t("web.admin.common.libraryEditor.monitoringStatus.withDetail", { summary, detail })
    : summary;
}

function statusSummary(entry: LibraryRealtimeMonitoringEntry): string {
  if (entry.state !== "monitoring") return realtimeMonitoringStateLabel(entry.state);
  const count = {
    count: entry.directories,
    count_text: entry.directories.toLocaleString(formatLocale()),
  };
  return entry.backend
    ? t("web.admin.common.libraryEditor.monitoringStatus.foldersWithBackend", {
        ...count,
        backend: entry.backend,
      })
    : t("web.admin.common.libraryEditor.monitoringStatus.folders", count);
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
