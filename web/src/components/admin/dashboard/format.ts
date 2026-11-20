import type { ScanRun } from "@/api/types";
import { formatActiveScanMode, formatActiveScanProgress } from "@/lib/scanRuns";
import { formatLocale } from "@/lib/appLocale";
import { t, type MessageKey } from "@/i18n";

export function formatFileCount(count: number | null | undefined) {
  if (count == null) {
    return "—";
  }
  return t("web.admin.dashboard.format.fileCount", {
    count,
    count_text: count.toLocaleString(formatLocale()),
  });
}

/**
 * Compact watch time for the top-activity lists. Sub-hour totals stay in
 * minutes so a short session does not collapse to "0.0h".
 */
export function formatWatchTime(totalSeconds: number | null | undefined) {
  if (totalSeconds == null || !Number.isFinite(totalSeconds) || totalSeconds <= 0) {
    return t("web.admin.dashboard.format.duration.minutes", { minutes: 0 });
  }
  if (totalSeconds < 3600) {
    return t("web.admin.dashboard.format.duration.minutes", {
      minutes: Math.max(1, Math.round(totalSeconds / 60)),
    });
  }
  const hours = totalSeconds / 3600;
  return t("web.admin.dashboard.format.watchTimeHours", {
    hours: hours.toLocaleString(formatLocale(), { maximumFractionDigits: 1 }),
  });
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
  return mbps.toLocaleString(formatLocale(), {
    minimumFractionDigits: decimals,
    maximumFractionDigits: decimals,
  });
}

export function formatMbps(mbps: number) {
  if (!Number.isFinite(mbps)) {
    return "—";
  }
  return t("web.common.format.bitrate.mbps", { rate: formatMbpsValue(mbps) });
}

const DATA_TOTAL_UNIT_KEYS: readonly MessageKey[] = [
  "web.common.format.size.bytes",
  "web.common.format.size.kb",
  "web.common.format.size.mb",
  "web.common.format.size.gb",
  "web.common.format.size.tb",
  "web.common.format.size.pb",
];

/**
 * Data transferred over a window. Decimal (1000-based) units, matching the
 * Mbps rates beside it, with one decimal below 100 so a quiet window does not
 * round to a misleading whole number.
 *
 * Unit and precision are chosen on the value as it will be displayed, so
 * rounding never produces "1,000 KB" (it reads "1.0 MB") or "100.0 GB" (it
 * reads "100 GB").
 */
export function formatDataTotal(bytes: number | null | undefined) {
  if (bytes == null || !Number.isFinite(bytes) || bytes < 0) {
    return "—";
  }
  let value = bytes;
  let unit = 0;
  for (;;) {
    const decimals = unit > 0 && value < 100 ? 1 : 0;
    const rounded = Number(value.toFixed(decimals));
    if (rounded >= 1000 && unit < DATA_TOTAL_UNIT_KEYS.length - 1) {
      value /= 1000;
      unit += 1;
      continue;
    }
    const shown = unit > 0 && rounded < 100 ? 1 : 0;
    return t(DATA_TOTAL_UNIT_KEYS[unit] ?? "web.common.format.size.pb", {
      size: rounded.toLocaleString(formatLocale(), {
        minimumFractionDigits: shown,
        maximumFractionDigits: shown,
      }),
    });
  }
}

export function formatDashboardLibraryScanProgress(scan: ScanRun, activeScanCount: number) {
  const running = scan.status === "running";
  const progress = formatActiveScanProgress(scan);
  const detail =
    progress ||
    (running ? formatActiveScanMode(scan) : t("web.admin.dashboard.format.scanWaitingForCapacity"));
  if (activeScanCount > 1) {
    return t(
      running
        ? "web.admin.dashboard.format.scanRunningWithMore"
        : "web.admin.dashboard.format.scanQueuedWithMore",
      { detail, more: activeScanCount - 1, more_text: activeScanCount - 1 },
    );
  }
  return t(
    running ? "web.admin.dashboard.format.scanRunning" : "web.admin.dashboard.format.scanQueued",
    { detail },
  );
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
    return t("web.admin.dashboard.format.duration.seconds", { seconds: totalSeconds });
  }
  if (totalSeconds < 3600) {
    const minutes = Math.floor(totalSeconds / 60);
    const seconds = totalSeconds % 60;
    return seconds === 0
      ? t("web.admin.dashboard.format.duration.minutes", { minutes })
      : t("web.admin.dashboard.format.duration.minutesSeconds", { minutes, seconds });
  }
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  return minutes === 0
    ? t("web.admin.dashboard.format.duration.hours", { hours })
    : t("web.admin.dashboard.format.duration.hoursMinutes", { hours, minutes });
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
  return hours === 0
    ? t("web.admin.dashboard.format.duration.days", { days })
    : t("web.admin.dashboard.format.duration.daysHours", { days, hours });
}

/** Sub-10ms round trips keep their decimals; anything slower does not need them. */
export function formatLatency(latencyMs: number) {
  if (!Number.isFinite(latencyMs)) {
    return "—";
  }
  return t("web.admin.dashboard.format.latencyMs", {
    value: latencyMs.toLocaleString(formatLocale(), {
      maximumFractionDigits: latencyMs < 10 ? 2 : 0,
    }),
  });
}

/** A whole or one-decimal percentage, already formatted, as a label. */
export function formatPercentLabel(percent: string): string {
  return t("web.common.format.percent", { percent });
}
