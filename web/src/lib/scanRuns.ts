import type { ScanRun } from "@/api/types";
import { t, type MessageKey } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";

export function compareActiveScans(left: ScanRun, right: ScanRun) {
  const leftRank = left.status === "running" ? 0 : 1;
  const rightRank = right.status === "running" ? 0 : 1;
  if (leftRank !== rightRank) {
    return leftRank - rightRank;
  }
  const modeCompare =
    activeScanModeRank(left.mode) - activeScanModeRank(right.mode) ||
    left.mode.localeCompare(right.mode);
  if (modeCompare !== 0) {
    return modeCompare;
  }
  return (left.path ?? "").localeCompare(right.path ?? "");
}

function activeScanModeKey(mode: ScanRun["mode"]): MessageKey | undefined {
  switch (mode) {
    case "library":
      return "web.admin.common.scans.mode.library";
    case "subtree":
      return "web.admin.common.scans.mode.subtree";
    case "file":
      return "web.admin.common.scans.mode.file";
    default:
      return undefined;
  }
}

/**
 * The modes' order in a list: their English labels' alphabetical order, kept
 * fixed so the list does not reorder with the interface language.
 */
function activeScanModeRank(mode: ScanRun["mode"]) {
  const rank = ["library", "file", "subtree"].indexOf(mode);
  return rank === -1 ? 3 : rank;
}

export function formatActiveScanMode(scan: Pick<ScanRun, "mode">) {
  const key = activeScanModeKey(scan.mode);
  return key ? t(key) : scan.mode;
}

export function formatActiveScanTarget(scan: Pick<ScanRun, "path">) {
  if (!scan.path) {
    return t("web.admin.common.scans.entireLibrary");
  }
  const segments = scan.path.replace(/\/+$/, "").split("/");
  return segments[segments.length - 1] || scan.path;
}

export function formatActiveScanTrigger(trigger: string) {
  switch (trigger) {
    case "autoscan":
      return t("web.admin.common.scans.trigger.autoscan");
    case "library_created":
      return t("web.admin.common.scans.trigger.created");
    case "library_paths_changed":
    case "library_updated":
      return t("web.admin.common.scans.trigger.updated");
    case "library_id":
    case "manual":
      return t("web.admin.common.scans.trigger.manual");
    case "task:scan_libraries":
    case "task_scan_libraries":
      return t("web.admin.common.scans.trigger.scheduled");
    case "realtime_monitor":
      return t("web.admin.common.scans.trigger.fileChange");
    default:
      return trigger.replace(/_/g, " ");
  }
}

/** When a running scan started: "Started", "Started just now", "Started 5m ago", … */
export function formatActiveScanStarted(iso: string | undefined) {
  if (!iso) {
    return t("web.admin.common.scans.started");
  }

  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return t("web.admin.common.scans.started");
  }

  const seconds = Math.max(0, Math.floor((Date.now() - date.getTime()) / 1000));
  if (seconds < 60) {
    return t("web.admin.common.scans.startedJustNow");
  }
  if (seconds < 3600) {
    const minutes = Math.floor(seconds / 60);
    return t("web.admin.common.scans.startedMinutesAgo", { count: minutes });
  }
  if (seconds < 86400) {
    const hours = Math.floor(seconds / 3600);
    return t("web.admin.common.scans.startedHoursAgo", { count: hours });
  }
  const days = Math.floor(seconds / 86400);
  return t("web.admin.common.scans.startedDaysAgo", { count: days, count_text: days });
}

export function formatActiveScanProgress(scan: ScanRun) {
  const result = scan.result;
  if (!result) {
    return "";
  }
  if (result.total_files && result.files_processed) {
    const percent = Math.max(
      0,
      Math.min(100, Math.round((result.files_processed / result.total_files) * 100)),
    );
    return t("web.admin.common.scans.progress", {
      message: result.message ?? t("web.admin.common.scans.processingFiles"),
      processed: result.files_processed.toLocaleString(formatLocale()),
      total: result.total_files.toLocaleString(formatLocale()),
      percent: percent,
    });
  }
  return result.message ?? "";
}
