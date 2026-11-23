import type { AdminJob, Library, ScanRun } from "@/api/types";
import type { Translate } from "@/i18n";

export interface LibraryScanGroup {
  libraryID: number;
  library: Library | null;
  scans: ScanRun[];
  runningCount: number;
  queuedCount: number;
}

export interface LibraryRefreshEntry {
  libraryID: number;
  library: Library | null;
  job: AdminJob;
}

export function getLibraryScanGroupName(
  t: Translate,
  library: Library | null,
  libraryID: number,
): string {
  return library?.name ?? t("web.admin.libraries.scanQueue.libraryFallback", { id: libraryID });
}

export function scanProgressPercent(scan: ScanRun): number | null {
  const total = scan.result?.total_files;
  const processed = scan.result?.files_processed;
  if (!total || total <= 0 || processed == null) return null;
  return Math.max(0, Math.min(100, Math.round((processed / total) * 100)));
}

/** Best available progress percent (0–100) across a library's running scans, or null. */
export function libraryScanProgressPercent(scans: ScanRun[]): number | null {
  const percents = scans
    .filter((scan) => scan.status === "running")
    .map(scanProgressPercent)
    .filter((pct): pct is number => pct !== null);
  if (percents.length === 0) return null;
  return Math.round(percents.reduce((sum, pct) => sum + pct, 0) / percents.length);
}
