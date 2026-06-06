import type { ScanRun } from "@/api/types";

export function compareActiveScans(left: ScanRun, right: ScanRun) {
  const leftRank = left.status === "running" ? 0 : 1;
  const rightRank = right.status === "running" ? 0 : 1;
  if (leftRank !== rightRank) {
    return leftRank - rightRank;
  }
  const modeCompare = formatActiveScanMode(left).localeCompare(formatActiveScanMode(right));
  if (modeCompare !== 0) {
    return modeCompare;
  }
  return (left.path ?? "").localeCompare(right.path ?? "");
}

export function formatActiveScanMode(scan: Pick<ScanRun, "mode">) {
  switch (scan.mode) {
    case "library":
      return "Full library scan";
    case "subtree":
      return "Subtree scan";
    case "file":
      return "Single file scan";
    default:
      return scan.mode;
  }
}

export function formatActiveScanTrigger(trigger: string) {
  switch (trigger) {
    case "autoscan":
      return "Autoscan";
    case "library_created":
      return "Created";
    case "library_paths_changed":
    case "library_updated":
      return "Updated";
    case "library_id":
    case "manual":
      return "Manual";
    case "task:scan_libraries":
    case "task_scan_libraries":
      return "Scheduled";
    default:
      return trigger.replace(/_/g, " ");
  }
}

export function formatActiveScanTime(iso: string | undefined, prefix: string) {
  if (!iso) {
    return prefix;
  }

  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return prefix;
  }

  const seconds = Math.max(0, Math.floor((Date.now() - date.getTime()) / 1000));
  if (seconds < 60) {
    return `${prefix} just now`;
  }
  if (seconds < 3600) {
    const minutes = Math.floor(seconds / 60);
    return `${prefix} ${minutes}m ago`;
  }
  if (seconds < 86400) {
    const hours = Math.floor(seconds / 3600);
    return `${prefix} ${hours}h ago`;
  }
  const days = Math.floor(seconds / 86400);
  return `${prefix} ${days}d ago`;
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
    return `${result.message ?? "Processing files"} · ${result.files_processed.toLocaleString()} / ${result.total_files.toLocaleString()} (${percent}%)`;
  }
  return result.message ?? "";
}
