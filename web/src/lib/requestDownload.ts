import type { RequestDownload } from "@/api/types";
import { formatHoursMinutes } from "@/lib/audiobooks/duration";

// Download progress labels live apart from mediaRequests: that module is on
// the launch path (search and status badges import it), and only the lazily
// loaded request pages show progress.

const DOWNLOAD_PHASES = new Set([
  "queued",
  "downloading",
  "paused",
  "stalled",
  "importing",
  "import_blocked",
]);

/** Figures the server has not refreshed for this long no longer support an estimate. */
const DOWNLOAD_ESTIMATE_MAX_AGE_MS = 10 * 60_000;

/**
 * How much has downloaded, for a progress bar; undefined while the size is
 * unknown, and for a phase this client does not know.
 */
export function requestDownloadPercent(download: RequestDownload): number | undefined {
  if (download.percent == null || !DOWNLOAD_PHASES.has(download.phase)) return undefined;
  return Math.min(100, Math.max(0, download.percent));
}

/**
 * "Downloading · 43% · about 12 min left", or the phase alone. The estimate
 * is left out once it has passed, or when the server last heard from the
 * download server more than ten minutes ago. A phase this client does not
 * know reads as Downloading. Admin views call a blocked import what it is;
 * a requester only needs to know it is waiting.
 */
export function formatRequestDownload(
  download: RequestDownload,
  { now = new Date(), admin = false }: { now?: Date; admin?: boolean } = {},
): string {
  switch (download.phase) {
    case "queued":
      return "Waiting to download";
    case "paused":
      return "Download paused";
    case "stalled":
      return "Download stalled";
    case "importing":
      return "Importing";
    case "import_blocked":
      return admin ? "Import blocked" : "Waiting for import";
    case "downloading":
      break;
    default:
      return "Downloading";
  }
  const parts = ["Downloading"];
  const percent = requestDownloadPercent(download);
  if (percent !== undefined) parts.push(`${percent}%`);
  const left = downloadTimeLeft(download, now);
  if (left) parts.push(`about ${left} left`);
  return parts.join(" · ");
}

function downloadTimeLeft(download: RequestDownload, now: Date): string | null {
  if (!download.estimated_completion_at) return null;
  const eta = Date.parse(download.estimated_completion_at);
  const heard = Date.parse(download.updated_at);
  const at = now.getTime();
  if (!Number.isFinite(eta) || eta <= at) return null;
  if (!Number.isFinite(heard) || at - heard > DOWNLOAD_ESTIMATE_MAX_AGE_MS) return null;
  return formatHoursMinutes((eta - at) / 1000);
}
