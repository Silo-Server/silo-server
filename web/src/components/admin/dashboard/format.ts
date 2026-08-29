import type { ScanRun } from "@/api/types";
import { formatActiveScanMode, formatActiveScanProgress } from "@/lib/scanRuns";

export function formatFileCount(count: number | null | undefined) {
  if (count == null) {
    return "—";
  }
  return count === 1 ? "1 file" : `${count.toLocaleString()} files`;
}

/**
 * Compact watch time for the top-activity lists. Sub-hour totals stay in
 * minutes so a short session does not collapse to "0.0h".
 */
export function formatWatchTime(totalSeconds: number | null | undefined) {
  if (totalSeconds == null || !Number.isFinite(totalSeconds) || totalSeconds <= 0) {
    return "0m";
  }
  if (totalSeconds < 3600) {
    return `${Math.max(1, Math.round(totalSeconds / 60))}m`;
  }
  const hours = totalSeconds / 3600;
  return `${hours.toLocaleString(undefined, { maximumFractionDigits: 1 })}h`;
}

/**
 * Egress rate for charts and tiles. Small rates keep one decimal so a trickle
 * of traffic does not round to a flat "0 Mbps" and read as idle.
 */
export function formatMbpsValue(mbps: number) {
  if (!Number.isFinite(mbps)) {
    return "—";
  }
  const decimals = mbps > 0 && mbps < 10 ? 1 : 0;
  return mbps.toLocaleString(undefined, {
    minimumFractionDigits: decimals,
    maximumFractionDigits: decimals,
  });
}

export function formatMbps(mbps: number) {
  if (!Number.isFinite(mbps)) {
    return "—";
  }
  return `${formatMbpsValue(mbps)} Mbps`;
}

export function formatDashboardLibraryScanProgress(scan: ScanRun, activeScanCount: number) {
  const status = scan.status === "running" ? "Scanning" : "Queued";
  const progress = formatActiveScanProgress(scan);
  const detail =
    progress || (scan.status === "running" ? formatActiveScanMode(scan) : "Waiting for capacity");
  const extraScans = activeScanCount > 1 ? ` + ${activeScanCount - 1} more` : "";
  return `${status}: ${detail}${extraScans}`;
}

/**
 * Elapsed time for a scan row. A finished scan reports the span it took; a
 * running one reports how long it has been going so far, which is the number
 * an operator watching a slow scan actually wants. The scans endpoint reports
 * the two timestamps and no duration, so it is derived here.
 */
export function formatScanDuration(
  scan: { started_at?: string; completed_at?: string },
  now: number = Date.now(),
): string {
  if (!scan.started_at) {
    return "—";
  }
  const started = Date.parse(scan.started_at);
  if (!Number.isFinite(started)) {
    return "—";
  }
  const ended = scan.completed_at ? Date.parse(scan.completed_at) : now;
  if (!Number.isFinite(ended) || ended < started) {
    return "—";
  }
  return formatDurationSeconds(Math.round((ended - started) / 1000));
}

function formatDurationSeconds(totalSeconds: number): string {
  if (totalSeconds < 60) {
    return `${totalSeconds}s`;
  }
  if (totalSeconds < 3600) {
    const minutes = Math.floor(totalSeconds / 60);
    const seconds = totalSeconds % 60;
    return seconds === 0 ? `${minutes}m` : `${minutes}m ${seconds}s`;
  }
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  return minutes === 0 ? `${hours}h` : `${hours}h ${minutes}m`;
}

/**
 * Uptime at the coarsest unit that still says something useful: an operator
 * scanning a health strip wants "3d 4h", not a seconds counter.
 */
export function formatUptime(startedAt: string | null | undefined, now: number = Date.now()) {
  if (!startedAt) {
    return "—";
  }
  const started = Date.parse(startedAt);
  if (!Number.isFinite(started)) {
    return "—";
  }
  const seconds = Math.max(0, Math.floor((now - started) / 1000));
  if (seconds < 86_400) {
    return formatDurationSeconds(seconds);
  }
  const days = Math.floor(seconds / 86_400);
  const hours = Math.floor((seconds % 86_400) / 3600);
  return hours === 0 ? `${days}d` : `${days}d ${hours}h`;
}

/** Sub-10ms round trips keep their decimals; anything slower does not need them. */
export function formatLatency(latencyMs: number) {
  if (!Number.isFinite(latencyMs)) {
    return "—";
  }
  return `${latencyMs.toLocaleString(undefined, {
    maximumFractionDigits: latencyMs < 10 ? 2 : 0,
  })} ms`;
}
