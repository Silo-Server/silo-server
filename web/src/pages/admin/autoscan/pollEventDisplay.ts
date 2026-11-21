import type {
  AutoscanChangeOutcome,
  AutoscanEvent,
  AutoscanEventChange,
  AutoscanEventScanRun,
  AutoscanEventStatus,
  AutoscanScan,
  AutoscanScanResult,
  AutoscanScanStatus,
  Library,
  ScanRun,
} from "@/api/types";
import { AlertTriangle, CheckCircle2, RefreshCw } from "lucide-react";
import { numberText, type MessageKey, type Translate } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";
import { formatTime as formatTimePreferred, preferredDateLocale } from "@/lib/datetime";
import { type SourceLabelLookups } from "@/lib/autoscanLabels";
import { formatDecimal } from "@/lib/numberFormat";
import { formatActiveScanMode } from "@/lib/scanRuns";

import { eventSourceName } from "./sourceDisplay";

/** Shown where a time or result is not known yet. */
export const NO_VALUE = "-";
export const SEPARATOR = " · ";

export function formatTime(value: string | undefined): string {
  if (!value) return NO_VALUE;
  return formatTimePreferred(value, { hour: "2-digit" });
}

export function formatTimestamp(value: string | undefined): string {
  if (!value) return NO_VALUE;
  const day = new Date(value).toLocaleDateString(preferredDateLocale(), {
    month: "short",
    day: "numeric",
  });
  return `${day}, ${formatTimePreferred(value, { hour: "2-digit" })}`;
}

export function formatDuration(t: Translate, ms: number): string {
  if (ms < 1000) return t("web.admin.autoscan.activity.duration.milliseconds", { value: ms });
  if (ms < 60_000)
    return t("web.admin.autoscan.activity.duration.seconds", {
      value: formatDecimal(Math.round(ms / 100) / 10, 1, 0),
    });
  const minutes = Math.floor(ms / 60_000);
  const seconds = Math.round((ms % 60_000) / 1000);
  return t("web.admin.autoscan.activity.duration.minutesSeconds", {
    minutes,
    seconds,
  });
}

export function pollEventDuration(t: Translate, event: AutoscanEvent): string {
  if (event.status !== "running") {
    return formatDuration(t, event.duration_ms);
  }
  const elapsed = Math.max(0, Date.now() - new Date(event.started_at).getTime());
  return formatDuration(t, elapsed);
}

export function pollEventTimestamp(event: AutoscanEvent): string {
  return formatTimestamp(event.status === "running" ? event.started_at : event.completed_at);
}

export function eventStatusTone(status: AutoscanEventStatus): {
  label: MessageKey;
  icon: typeof CheckCircle2;
  className: string;
} {
  switch (status) {
    case "success":
      return {
        label: "web.admin.autoscan.activity.pollStatus.success",
        icon: CheckCircle2,
        className: "border-emerald-500/30 bg-emerald-500/10 text-emerald-500",
      };
    case "running":
      return {
        label: "web.admin.autoscan.activity.pollStatus.running",
        icon: RefreshCw,
        className: "border-primary/30 bg-primary/10 text-primary",
      };
    case "unresolved":
      return {
        label: "web.admin.autoscan.activity.pollStatus.unresolved",
        icon: AlertTriangle,
        className: "border-amber-500/30 bg-amber-500/10 text-amber-500",
      };
    case "error":
      return {
        label: "web.admin.autoscan.activity.pollStatus.error",
        icon: AlertTriangle,
        className: "border-destructive/30 bg-destructive/10 text-destructive-label",
      };
  }
}

export function scanStatusClass(
  status: AutoscanEventScanRun["status"] | ScanRun["status"],
): string {
  switch (status) {
    case "completed":
      return "border-emerald-500/30 bg-emerald-500/10 text-emerald-500";
    case "failed":
      return "border-destructive/30 bg-destructive/10 text-destructive-label";
    case "running":
      return "border-sky-500/30 bg-sky-500/10 text-sky-500";
    case "accepted":
      return "border-muted-foreground/25 bg-muted/60 text-muted-foreground";
    case "cancelled":
      return "border-amber-500/30 bg-amber-500/10 text-amber-500";
  }
}

// Lowercase: the badge capitalizes the first letter with CSS.
export const SCAN_STATUS_LABELS: Record<AutoscanScanStatus | ScanRun["status"], MessageKey> = {
  accepted: "web.admin.autoscan.activity.scanStatus.queued",
  running: "web.admin.autoscan.activity.scanStatus.running",
  completed: "web.admin.autoscan.activity.scanStatus.completed",
  failed: "web.admin.autoscan.activity.scanStatus.failed",
  cancelled: "web.admin.autoscan.activity.scanStatus.cancelled",
};

export const SCAN_STATUS_FILTER_LABELS: Record<AutoscanScanStatus, MessageKey> = {
  accepted: "web.admin.autoscan.activity.scanFilter.queued",
  running: "web.admin.autoscan.activity.scanFilter.running",
  completed: "web.admin.autoscan.activity.scanFilter.completed",
  failed: "web.admin.autoscan.activity.scanFilter.failed",
  cancelled: "web.admin.autoscan.activity.scanFilter.cancelled",
};

export const SCAN_STATUS_ORDER: readonly AutoscanScanStatus[] = [
  "running",
  "accepted",
  "completed",
  "failed",
  "cancelled",
];

// Name every Activity row the way the Sources list titles the source, so the
// two tabs agree on what a source is called.
export type PollSourceRef = {
  source_id?: string | null;
  plugin_id?: string | null;
  capability_id?: string;
};

export function pollSourceName(
  t: Translate,
  event: PollSourceRef,
  lookups: SourceLabelLookups,
): string {
  return (
    eventSourceName(event, lookups) ||
    event.plugin_id ||
    event.capability_id ||
    t("web.admin.autoscan.activity.sourceFallback")
  );
}

export function scanSourceName(
  t: Translate,
  scan: AutoscanScan,
  lookups: SourceLabelLookups,
): string {
  return eventSourceName(scan, lookups) || t("web.admin.autoscan.activity.sourceFallback");
}

export function libraryName(
  t: Translate,
  librariesByID: Map<number, Library>,
  libraryID: number,
): string {
  return (
    librariesByID.get(libraryID)?.name ??
    t("web.admin.autoscan.activity.libraryFallback", { id: libraryID })
  );
}

export type ScanResultSummary = { text: string; tone: "skipped" | "errors" | "changes" | "none" };

/** Plural values for a count shown with the locale's digit grouping. */
export function countValues(count: number) {
  return { count, count_text: count.toLocaleString(formatLocale()) };
}

/** Plural values for a count printed without digit grouping. */
export function rawCountValues(count: number) {
  return { count, count_text: count };
}

/**
 * Summarizes a completed run's counters. A skipped run did no work because an
 * overlapping scan of the same scope was already running, which must not read
 * like a run that found nothing.
 */
export function summarizeScanResult(
  t: Translate,
  result: AutoscanScanResult | undefined,
): ScanResultSummary | null {
  if (!result) return null;
  if (result.skipped > 0) {
    return { text: t("web.admin.autoscan.activity.result.skipped"), tone: "skipped" };
  }
  const parts: string[] = [];
  if (result.new > 0)
    parts.push(t("web.admin.autoscan.activity.result.new", countValues(result.new)));
  if (result.updated > 0)
    parts.push(t("web.admin.autoscan.activity.result.updated", countValues(result.updated)));
  if (result.missing > 0)
    parts.push(t("web.admin.autoscan.activity.result.missing", countValues(result.missing)));
  if (result.files_deleted > 0)
    parts.push(
      t("web.admin.autoscan.activity.result.filesRemoved", countValues(result.files_deleted)),
    );
  // A scan can drop catalog items whose files were already gone.
  if (result.items_deleted > 0)
    parts.push(
      t("web.admin.autoscan.activity.result.itemsRemoved", countValues(result.items_deleted)),
    );
  // A scoped scan can drop a title from this library without deleting it.
  if (result.memberships_removed > 0)
    parts.push(
      t(
        "web.admin.autoscan.activity.result.titlesRemoved",
        countValues(result.memberships_removed),
      ),
    );
  if (result.missing_skipped_protected > 0)
    parts.push(
      t(
        "web.admin.autoscan.activity.result.keptOffline",
        countValues(result.missing_skipped_protected),
      ),
    );
  if (result.errors > 0)
    parts.push(t("web.admin.autoscan.activity.result.errors", countValues(result.errors)));
  if (parts.length === 0) {
    return {
      text:
        result.unchanged > 0
          ? t(
              "web.admin.autoscan.activity.result.noChangesUnchanged",
              countValues(result.unchanged),
            )
          : t("web.admin.autoscan.activity.result.noChanges"),
      tone: "none",
    };
  }
  return { text: parts.join(SEPARATOR), tone: result.errors > 0 ? "errors" : "changes" };
}

export const CHANGE_OUTCOME_TONES: Record<
  AutoscanChangeOutcome,
  { label: MessageKey; className: string }
> = {
  queued: {
    label: "web.admin.autoscan.activity.outcome.queued",
    className: "border-emerald-500/30 bg-emerald-500/10 text-emerald-500",
  },
  joined: {
    label: "web.admin.autoscan.activity.outcome.joined",
    className: "border-sky-500/30 bg-sky-500/10 text-sky-500",
  },
  suppressed: {
    label: "web.admin.autoscan.activity.outcome.suppressed",
    className: "border-muted-foreground/25 bg-muted/60 text-muted-foreground",
  },
  unresolved: {
    label: "web.admin.autoscan.activity.outcome.unresolved",
    className: "border-amber-500/30 bg-amber-500/10 text-amber-500",
  },
  ignored: {
    label: "web.admin.autoscan.activity.outcome.ignored",
    className: "border-muted-foreground/25 bg-muted/60 text-muted-foreground",
  },
  error: {
    label: "web.admin.autoscan.activity.outcome.error",
    className: "border-destructive/30 bg-destructive/10 text-destructive-label",
  },
};

export const CHANGE_OUTCOME_ORDER: readonly AutoscanChangeOutcome[] = [
  "queued",
  "joined",
  "unresolved",
  "error",
  "suppressed",
  "ignored",
];

export function isKnownChangeOutcome(outcome: string): outcome is AutoscanChangeOutcome {
  return Object.prototype.hasOwnProperty.call(CHANGE_OUTCOME_TONES, outcome);
}

// Plain-language explanations for the resolver's reason codes. Unknown codes
// fall back to the server's detail text so a new reason still reads sensibly.
export const CHANGE_REASON_TEXT: Record<string, MessageKey> = {
  no_library_match: "web.admin.autoscan.activity.reason.noLibraryMatch",
  subtree_outside_library: "web.admin.autoscan.activity.reason.subtreeOutsideLibrary",
  path_outside_library: "web.admin.autoscan.activity.reason.pathOutsideLibrary",
  path_ambiguous: "web.admin.autoscan.activity.reason.pathAmbiguous",
  library_disabled: "web.admin.autoscan.activity.reason.libraryDisabled",
  library_root_offline: "web.admin.autoscan.activity.reason.libraryRootOffline",
  unsupported_extension: "web.admin.autoscan.activity.reason.unsupportedExtension",
  path_permission_denied: "web.admin.autoscan.activity.reason.pathPermissionDenied",
  path_not_inspectable: "web.admin.autoscan.activity.reason.pathNotInspectable",
  path_not_file_or_dir: "web.admin.autoscan.activity.reason.pathNotFileOrDir",
  path_required: "web.admin.autoscan.activity.reason.pathRequired",
  path_ignored: "web.admin.autoscan.activity.reason.pathIgnored",
  resolves_to_library: "web.admin.autoscan.activity.reason.resolvesToLibrary",
  enqueue_failed: "web.admin.autoscan.activity.reason.enqueueFailed",
};

export function changeReasonText(t: Translate, reason: string | undefined): string | undefined {
  if (!reason || !Object.prototype.hasOwnProperty.call(CHANGE_REASON_TEXT, reason)) return;
  return t(CHANGE_REASON_TEXT[reason]!);
}

export function changeScopeLabel(t: Translate, change: AutoscanEventChange): string {
  const mode = change.target_mode
    ? formatActiveScanMode({ mode: change.target_mode as ScanRun["mode"] })
    : t("web.admin.autoscan.activity.change.scanFallback");
  return t("web.admin.autoscan.activity.change.scope", {
    mode,
    path: change.target_path || t("web.admin.autoscan.activity.entireLibrary"),
  });
}

/** The explanation of a queued or joined change, without and with its library. */
export function changeScanKeys(change: AutoscanEventChange): [MessageKey, MessageKey] {
  if (change.outcome === "queued")
    return [
      "web.admin.autoscan.activity.change.queued",
      "web.admin.autoscan.activity.change.queuedInLibrary",
    ];
  if (change.reason === "follow_up_scan")
    return [
      "web.admin.autoscan.activity.change.followUp",
      "web.admin.autoscan.activity.change.followUpInLibrary",
    ];
  return [
    "web.admin.autoscan.activity.change.joined",
    "web.admin.autoscan.activity.change.joinedInLibrary",
  ];
}

/** Runs the event's changes joined that another event created. */
export function joinedRunCount(event: AutoscanEvent): number {
  const created = new Set(event.scan_runs.map((run) => run.id));
  const joined = new Set<string>();
  for (const change of event.changes) {
    if (change.outcome === "joined" && change.scan_run_id && !created.has(change.scan_run_id))
      joined.add(change.scan_run_id);
  }
  return joined.size;
}

export function eventDetailsSummary(t: Translate, event: AutoscanEvent): string {
  const linked = event.scan_runs.length + joinedRunCount(event);
  // Joined runs are only known from recorded changes, so a truncated log can
  // omit some; the count is then a lower bound.
  const paths = t(
    "web.admin.autoscan.activity.details.pathCount",
    countValues(event.changes_returned),
  );
  const linkedText = t("web.admin.autoscan.activity.details.linkedCount", {
    count: linked,
    count_text: `${numberText(linked)}${event.changes_truncated ? "+" : ""}`,
  });
  return [paths, linkedText].join(SEPARATOR);
}

/**
 * Splits a filesystem path into its parent directory prefix and emphasized
 * final segment so long media paths remain scannable without modifying the
 * underlying selectable text.
 */
export function splitDisplayPath(path: string): { dir: string; leaf: string } {
  if (!path) return { dir: "", leaf: "" };
  const trimmedEnd = path.replace(/[\\/]+$/, "");
  if (!trimmedEnd) return { dir: "", leaf: path };
  const lastSlash = Math.max(trimmedEnd.lastIndexOf("/"), trimmedEnd.lastIndexOf("\\"));
  if (lastSlash < 0) return { dir: "", leaf: path };
  return {
    dir: path.slice(0, lastSlash + 1),
    leaf: path.slice(lastSlash + 1),
  };
}
