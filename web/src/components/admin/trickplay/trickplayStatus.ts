import type { AdminTrickplayFile, AdminTrickplayLibrary } from "@/hooks/queries/admin/trickplay";
import { formatFileSize } from "@/lib/mediaFormat";

/** Files a library has, and how many of them have seek previews. */
export function libraryTrickplayProgress(library: AdminTrickplayLibrary) {
  const total = library.pending + library.running + library.ready + library.unusable;
  return { total, ready: library.ready, done: library.pending === 0 && library.running === 0 };
}

/** The badge's short text: "Previews 84%" while working, "Previews ready" once done. */
export function libraryTrickplayLabel(library: AdminTrickplayLibrary): string {
  const { total, ready, done } = libraryTrickplayProgress(library);
  if (total > 0 && done && library.unusable === 0) return "Previews ready";
  return `Previews ${total === 0 ? 0 : Math.floor((ready * 100) / total)}%`;
}

/** The badge's tooltip: every count, then the storage the sheets take. */
export function libraryTrickplayDetail(library: AdminTrickplayLibrary): string {
  const parts = [`${library.ready} ready`];
  if (library.pending > 0) parts.push(`${library.pending} waiting`);
  if (library.running > 0) parts.push(`${library.running} in progress`);
  if (library.unusable > 0) parts.push(`${library.unusable} failed`);
  parts.push(formatFileSize(library.sheet_bytes, { fallback: "0 B" }));
  return `Seek previews: ${parts.join(" · ")}`;
}

const STATE_LABELS: Record<AdminTrickplayFile["state"], string> = {
  off: "Off for this library",
  pending: "Waiting",
  running: "In progress",
  ready: "Ready",
  unusable: "Failed",
};

export function trickplayStateLabel(file: AdminTrickplayFile): string {
  // A file waiting after a failure is retried later; say so rather than "Waiting".
  if (file.state === "pending" && file.failures > 0) {
    return `Retrying after ${file.failures} ${file.failures === 1 ? "failure" : "failures"}`;
  }
  return STATE_LABELS[file.state];
}

/** "720 previews · 300 px every 10 s · 2.1 MB", or "" before any are published. */
export function trickplayFileSummary(file: AdminTrickplayFile): string {
  if (!file.thumbnail_count) return "";
  const parts = [`${file.thumbnail_count} previews`];
  if (file.thumbnail_width && file.interval_ms) {
    parts.push(`${file.thumbnail_width} px every ${file.interval_ms / 1000} s`);
  }
  if (file.sheet_bytes) parts.push(formatFileSize(file.sheet_bytes));
  return parts.join(" · ");
}
