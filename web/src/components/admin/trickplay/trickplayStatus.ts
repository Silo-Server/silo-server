import type { AdminTrickplayFile, AdminTrickplayLibrary } from "@/hooks/queries/admin/trickplay";
import { t, type MessageKey } from "@/i18n";
import { formatFileSize } from "@/lib/mediaFormat";

/** Files a library has, and how many of them have seek previews. */
export function libraryTrickplayProgress(library: AdminTrickplayLibrary) {
  const total = library.pending + library.running + library.ready + library.unusable;
  return { total, ready: library.ready, done: library.pending === 0 && library.running === 0 };
}

/** The badge's short text: "Previews 84%" while working, "Previews ready" once done. */
export function libraryTrickplayLabel(library: AdminTrickplayLibrary): string {
  const { total, ready, done } = libraryTrickplayProgress(library);
  if (total > 0 && done && library.unusable === 0)
    return t("web.admin.common.trickplay.badge.ready");
  return t("web.admin.common.trickplay.badge.percent", {
    percent: String(total === 0 ? 0 : Math.floor((ready * 100) / total)),
  });
}

function countValues(count: number) {
  return { count, count_text: String(count) };
}

/** The badge's tooltip: every count, then the storage the sheets take. */
export function libraryTrickplayDetail(library: AdminTrickplayLibrary): string {
  const parts = [t("web.admin.common.trickplay.badge.readyCount", countValues(library.ready))];
  if (library.pending > 0)
    parts.push(t("web.admin.common.trickplay.badge.waitingCount", countValues(library.pending)));
  if (library.running > 0)
    parts.push(t("web.admin.common.trickplay.badge.runningCount", countValues(library.running)));
  if (library.unusable > 0)
    parts.push(t("web.admin.common.trickplay.badge.failedCount", countValues(library.unusable)));
  parts.push(formatFileSize(library.sheet_bytes, { fallback: "0 B" }));
  return t("web.admin.common.trickplay.badge.detail", { details: parts.join(" · ") });
}

const STATE_LABELS: Record<AdminTrickplayFile["state"], MessageKey> = {
  off: "web.admin.common.trickplay.state.off",
  pending: "web.admin.common.trickplay.state.pending",
  running: "web.admin.common.trickplay.state.running",
  ready: "web.admin.common.trickplay.state.ready",
  unusable: "web.admin.common.trickplay.state.unusable",
};

export function trickplayStateLabel(file: AdminTrickplayFile): string {
  // A file waiting after a failure is retried later; say so rather than "Waiting".
  if (file.state === "pending" && file.failures > 0) {
    return t("web.admin.common.trickplay.state.retrying", countValues(file.failures));
  }
  return t(STATE_LABELS[file.state]);
}

/** "720 previews · 300 px every 10 s · 2.1 MB", or "" before any are published. */
export function trickplayFileSummary(file: AdminTrickplayFile): string {
  if (!file.thumbnail_count) return "";
  const parts = [t("web.admin.common.trickplay.file.previews", countValues(file.thumbnail_count))];
  if (file.thumbnail_width && file.interval_ms) {
    parts.push(
      t("web.admin.common.trickplay.file.sampling", {
        width: String(file.thumbnail_width),
        seconds: String(file.interval_ms / 1000),
      }),
    );
  }
  if (file.sheet_bytes) parts.push(formatFileSize(file.sheet_bytes));
  return parts.join(" · ");
}
