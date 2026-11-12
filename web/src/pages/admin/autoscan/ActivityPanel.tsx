import { useMemo, useState, type ReactNode } from "react";
import {
  AlertTriangle,
  CheckCircle2,
  Clock,
  History,
  ListChecks,
  RefreshCw,
  ScanLine,
  Search,
  Square,
  X,
} from "lucide-react";
import type {
  AutoscanChangeOutcome,
  AutoscanEvent,
  AutoscanEventChange,
  AutoscanEventScanRun,
  AutoscanEventStatus,
  AutoscanRunningPoll,
  AutoscanScan,
  AutoscanScanResult,
  AutoscanScanStatus,
  Library,
  ScanRun,
} from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { TablePagination } from "@/components/ui/pagination";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useEventChannel } from "@/components/realtimeEventsContext";
import { useDebounce } from "@/hooks/useDebounce";
import { numberText, useT, type MessageKey, type Translate } from "@/i18n";
import {
  useAutoscanConnections,
  useAutoscanEvents,
  useAutoscanScans,
  useAutoscanSources,
  useAutoscanStatus,
  useAvailableScanSources,
} from "@/hooks/queries/useAutoscan";
import { useActiveScans } from "@/hooks/queries/admin/scans";
import { useAdminLibraries, useCancelLibraryScans } from "@/hooks/queries/admin/libraries";
import { buildPluginDisplayNames, type SourceLabelLookups } from "@/lib/autoscanLabels";
import {
  compareActiveScans,
  formatActiveScanMode,
  formatActiveScanProgress,
  formatActiveScanStarted,
  formatActiveScanTrigger,
} from "@/lib/scanRuns";
import { cn } from "@/lib/utils";
import { formatTime as formatTimePreferred, preferredDateLocale } from "@/lib/datetime";

import { eventSourceName } from "./sourceDisplay";
import { formatLocale } from "@/lib/appLocale";
import { formatDecimal } from "@/lib/numberFormat";

const HISTORY_PAGE_SIZE_OPTIONS = [25, 50, 100];
const DEFAULT_HISTORY_PAGE_SIZE = 25;
const QUEUE_PAGE_SIZE = 8;
/** Shown where a time or result is not known yet. */
const NO_VALUE = "-";
const SEPARATOR = " · ";

type HistoryView = "scans" | "polls";

function formatTime(value: string | undefined): string {
  if (!value) return NO_VALUE;
  return formatTimePreferred(value, { hour: "2-digit" });
}

function formatTimestamp(value: string | undefined): string {
  if (!value) return NO_VALUE;
  const day = new Date(value).toLocaleDateString(preferredDateLocale(), {
    month: "short",
    day: "numeric",
  });
  return `${day}, ${formatTimePreferred(value, { hour: "2-digit" })}`;
}

function formatDuration(t: Translate, ms: number): string {
  if (ms < 1000) return t("web.admin.autoscan.activity.duration.milliseconds", { value: ms });
  if (ms < 60_000)
    return t("web.admin.autoscan.activity.duration.seconds", {
      value: formatDecimal(Math.round(ms / 100) / 10, 1, 0),
    });
  const minutes = Math.floor(ms / 60_000);
  const seconds = Math.round((ms % 60_000) / 1000);
  return t("web.admin.autoscan.activity.duration.minutesSeconds", {
    minutes: minutes,
    seconds: seconds,
  });
}

function pollEventDuration(t: Translate, event: AutoscanEvent): string {
  if (event.status !== "running") {
    return formatDuration(t, event.duration_ms);
  }
  const elapsed = Math.max(0, Date.now() - new Date(event.started_at).getTime());
  return formatDuration(t, elapsed);
}

function pollEventTimestamp(event: AutoscanEvent): string {
  return formatTimestamp(event.status === "running" ? event.started_at : event.completed_at);
}

function eventStatusTone(status: AutoscanEventStatus): {
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
        className: "border-destructive/30 bg-destructive/10 text-destructive",
      };
  }
}

function scanStatusClass(status: AutoscanEventScanRun["status"] | ScanRun["status"]): string {
  switch (status) {
    case "completed":
      return "border-emerald-500/30 bg-emerald-500/10 text-emerald-500";
    case "failed":
      return "border-destructive/30 bg-destructive/10 text-destructive";
    case "running":
      return "border-sky-500/30 bg-sky-500/10 text-sky-500";
    case "accepted":
      return "border-muted-foreground/25 bg-muted/60 text-muted-foreground";
    case "cancelled":
      return "border-amber-500/30 bg-amber-500/10 text-amber-500";
  }
}

// Lowercase: the badge capitalizes the first letter with CSS.
const SCAN_STATUS_LABELS: Record<AutoscanScanStatus | ScanRun["status"], MessageKey> = {
  accepted: "web.admin.autoscan.activity.scanStatus.queued",
  running: "web.admin.autoscan.activity.scanStatus.running",
  completed: "web.admin.autoscan.activity.scanStatus.completed",
  failed: "web.admin.autoscan.activity.scanStatus.failed",
  cancelled: "web.admin.autoscan.activity.scanStatus.cancelled",
};

// Name every Activity row the way the Sources list titles the source, so the
// two tabs agree on what a source is called.
type PollSourceRef = {
  source_id?: string | null;
  plugin_id?: string | null;
  capability_id?: string;
};

function pollSourceName(t: Translate, event: PollSourceRef, lookups: SourceLabelLookups): string {
  return (
    eventSourceName(event, lookups) ||
    event.plugin_id ||
    event.capability_id ||
    t("web.admin.autoscan.activity.sourceFallback")
  );
}

function scanSourceName(t: Translate, scan: AutoscanScan, lookups: SourceLabelLookups): string {
  return eventSourceName(scan, lookups) || t("web.admin.autoscan.activity.sourceFallback");
}

function libraryName(t: Translate, librariesByID: Map<number, Library>, libraryID: number): string {
  return (
    librariesByID.get(libraryID)?.name ??
    t("web.admin.autoscan.activity.libraryFallback", { id: libraryID })
  );
}

function PollStatusBadge({ status }: { status: AutoscanEventStatus }) {
  const t = useT();
  const tone = eventStatusTone(status);
  const Icon = tone.icon;
  return (
    <Badge variant="outline" className={tone.className}>
      <Icon className={cn("h-3.5 w-3.5", status === "running" && "animate-spin")} />
      {t(tone.label)}
    </Badge>
  );
}

function ScanStatusBadge({ status }: { status: AutoscanScanStatus | ScanRun["status"] }) {
  const t = useT();
  return (
    <Badge variant="outline" className={cn("capitalize tabular-nums", scanStatusClass(status))}>
      {Object.hasOwn(SCAN_STATUS_LABELS, status) ? t(SCAN_STATUS_LABELS[status]) : status}
    </Badge>
  );
}

/**
 * Marks webhook-delivered events (with the arr event type when known) so they
 * read distinctly from poll cycles. Poll events render nothing — poll is the
 * default and labeling every row would be noise.
 */
function DeliveryBadge({ event }: { event: AutoscanEvent }) {
  const t = useT();
  if (event.delivery_mode !== "webhook") return null;
  return (
    <Badge variant="secondary" className="text-xs">
      {event.provider_event_type
        ? t("web.admin.autoscan.activity.delivery.webhookEvent", {
            event_type: event.provider_event_type,
          })
        : t("web.admin.autoscan.activity.delivery.webhook")}
    </Badge>
  );
}

// Shared desktop table chrome so the queue, scan history, and poll log read as
// one family: clipped rounded border, muted sticky-feeling header band.
function DataTable({ head, children }: { head: ReactNode; children: ReactNode }) {
  return (
    <div className="hidden overflow-hidden rounded-lg border lg:block">
      <Table>
        <TableHeader className="bg-muted/40">
          <TableRow className="hover:bg-transparent">{head}</TableRow>
        </TableHeader>
        <TableBody>{children}</TableBody>
      </Table>
    </div>
  );
}

type ScanResultSummary = { text: string; tone: "skipped" | "errors" | "changes" | "none" };

/** Plural values for a count shown with the locale's digit grouping. */
function countValues(count: number) {
  return { count, count_text: count.toLocaleString(formatLocale()) };
}

/**
 * Summarizes a completed run's counters. A skipped run did no work because an
 * overlapping scan of the same scope was already running, which must not read
 * like a run that found nothing.
 */
function summarizeScanResult(
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

function ScanResultText({
  result,
  className,
}: {
  result: AutoscanScanResult | undefined;
  className?: string;
}) {
  const t = useT();
  const summary = summarizeScanResult(t, result);
  if (!summary) return null;
  return (
    <span
      className={cn(
        "text-xs tabular-nums",
        summary.tone === "skipped" && "text-amber-500",
        summary.tone === "errors" && "text-destructive",
        summary.tone === "changes" && "text-foreground",
        summary.tone === "none" && "text-muted-foreground",
        className,
      )}
    >
      {summary.text}
    </span>
  );
}

function RunList({ runs, joinedRuns }: { runs: AutoscanEventScanRun[]; joinedRuns: number }) {
  const t = useT();
  if (runs.length === 0) {
    return (
      <span className="text-muted-foreground text-xs">
        {joinedRuns > 0
          ? t("web.admin.autoscan.activity.runs.noneJoined")
          : t("web.admin.autoscan.activity.runs.none")}
      </span>
    );
  }
  return (
    <div className="space-y-2">
      {runs.map((run) => (
        <div
          key={run.id}
          className="border-border/70 bg-background/40 grid gap-1 rounded-md border px-3 py-2 text-xs sm:grid-cols-[auto_1fr_auto] sm:items-center"
        >
          <ScanStatusBadge status={run.status} />
          <div className="min-w-0">
            <div className="font-medium">{run.mode}</div>
            <div className="text-muted-foreground [overflow-wrap:anywhere]">
              {run.path || t("web.admin.autoscan.activity.entireLibrary")}
            </div>
            <ScanResultText result={run.result} className="mt-0.5 block" />
          </div>
          <div className="text-muted-foreground whitespace-nowrap tabular-nums">
            {formatTime(run.completed_at ?? run.started_at ?? run.requested_at)}
          </div>
        </div>
      ))}
    </div>
  );
}

const CHANGE_OUTCOME_TONES: Record<
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
    className: "border-destructive/30 bg-destructive/10 text-destructive",
  },
};

function isKnownChangeOutcome(outcome: string): outcome is AutoscanChangeOutcome {
  return Object.prototype.hasOwnProperty.call(CHANGE_OUTCOME_TONES, outcome);
}

// An outcome added by a newer server shows its raw value in a neutral badge.
function ChangeOutcomeBadge({ outcome }: { outcome: AutoscanEventChange["outcome"] }) {
  const t = useT();
  const tone = isKnownChangeOutcome(outcome)
    ? { ...CHANGE_OUTCOME_TONES[outcome], label: t(CHANGE_OUTCOME_TONES[outcome].label) }
    : { label: outcome, className: "border-muted-foreground/25 bg-muted/60 text-muted-foreground" };
  return (
    <Badge variant="outline" className={cn("whitespace-nowrap", tone.className)}>
      {tone.label}
    </Badge>
  );
}

// Plain-language explanations for the resolver's reason codes. Unknown codes
// fall back to the server's detail text so a new reason still reads sensibly.
const CHANGE_REASON_TEXT: Record<string, MessageKey> = {
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
  resolves_to_library: "web.admin.autoscan.activity.reason.resolvesToLibrary",
  enqueue_failed: "web.admin.autoscan.activity.reason.enqueueFailed",
};

function changeReasonText(t: Translate, reason: string | undefined): string | undefined {
  if (!reason || !Object.prototype.hasOwnProperty.call(CHANGE_REASON_TEXT, reason)) return;
  return t(CHANGE_REASON_TEXT[reason]!);
}

function changeScopeLabel(t: Translate, change: AutoscanEventChange): string {
  const mode = change.target_mode
    ? formatActiveScanMode({ mode: change.target_mode as ScanRun["mode"] })
    : t("web.admin.autoscan.activity.change.scanFallback");
  return t("web.admin.autoscan.activity.change.scope", {
    mode,
    path: change.target_path || t("web.admin.autoscan.activity.entireLibrary"),
  });
}

/** The explanation of a queued or joined change, without and with its library. */
function changeScanKeys(change: AutoscanEventChange): [MessageKey, MessageKey] {
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

function ChangeExplanation({
  change,
  librariesByID,
}: {
  change: AutoscanEventChange;
  librariesByID: Map<number, Library>;
}) {
  const t = useT();
  const library = change.library_id ? libraryName(t, librariesByID, change.library_id) : null;
  switch (change.outcome) {
    case "queued":
    case "joined": {
      const [plain, inLibrary] = changeScanKeys(change);
      const scope = changeScopeLabel(t, change);
      return (
        <>
          <div className="text-muted-foreground [overflow-wrap:anywhere]">
            {library ? t(inLibrary, { scope, library }) : t(plain, { scope })}
          </div>
          {change.scan_run_id ? (
            <div className="text-muted-foreground font-mono text-[11px] [overflow-wrap:anywhere]">
              {change.scan_run_id}
            </div>
          ) : null}
        </>
      );
    }
    case "suppressed":
      return (
        <div className="text-muted-foreground [overflow-wrap:anywhere]">
          {change.target_path
            ? t("web.admin.autoscan.activity.change.suppressedPath", { path: change.target_path })
            : t("web.admin.autoscan.activity.change.suppressed")}
        </div>
      );
    case "error":
      return (
        <div className="text-destructive [overflow-wrap:anywhere]">
          {change.reason === "resolve_failed"
            ? change.detail
              ? t("web.admin.autoscan.activity.change.resolveFailedDetail", {
                  detail: change.detail,
                })
              : t("web.admin.autoscan.activity.change.resolveFailed")
            : (changeReasonText(t, change.reason) ??
              change.detail ??
              t("web.admin.autoscan.activity.change.processingFailed"))}
        </div>
      );
    default: {
      const text =
        changeReasonText(t, change.reason) ??
        change.detail ??
        change.reason ??
        t("web.admin.autoscan.activity.change.notScanned");
      return (
        <div
          className={cn(
            "[overflow-wrap:anywhere]",
            change.outcome === "unresolved" ? "text-amber-500" : "text-muted-foreground",
          )}
        >
          {library ? t("web.admin.autoscan.activity.change.withLibrary", { text, library }) : text}
        </div>
      );
    }
  }
}

function ChangeList({
  event,
  librariesByID,
}: {
  event: AutoscanEvent;
  librariesByID: Map<number, Library>;
}) {
  const t = useT();
  if (event.changes.length === 0) {
    return (
      <span className="text-muted-foreground text-xs">
        {event.changes_returned > 0
          ? t("web.admin.autoscan.activity.changes.notRecorded")
          : t("web.admin.autoscan.activity.changes.none")}
      </span>
    );
  }
  return (
    <div className="space-y-2">
      {event.changes.map((change, index) => (
        <div
          key={`${index}:${change.source_path}`}
          className="border-border/70 bg-background/40 grid gap-1.5 rounded-md border px-3 py-2 text-xs sm:grid-cols-[auto_1fr] sm:items-start"
        >
          <ChangeOutcomeBadge outcome={change.outcome} />
          <div className="min-w-0 space-y-0.5">
            <div className="font-mono [overflow-wrap:anywhere]">{change.source_path}</div>
            {change.rewritten_path && change.rewritten_path !== change.source_path ? (
              <div className="text-muted-foreground font-mono [overflow-wrap:anywhere]">
                {t("web.admin.autoscan.activity.change.rewrittenTo", {
                  path: change.rewritten_path,
                  label: (chunks) => <span className="font-sans">{chunks}</span>,
                })}
              </div>
            ) : null}
            <ChangeExplanation change={change} librariesByID={librariesByID} />
          </div>
        </div>
      ))}
      {event.changes_truncated ? (
        <p className="text-muted-foreground text-xs">
          {t("web.admin.autoscan.activity.changes.truncated", {
            ...countValues(event.changes_returned),
            shown: event.changes.length.toLocaleString(formatLocale()),
          })}
        </p>
      ) : null}
    </div>
  );
}

function EventDetails({
  event,
  librariesByID,
}: {
  event: AutoscanEvent;
  librariesByID: Map<number, Library>;
}) {
  const t = useT();
  return (
    <div className="space-y-3">
      <div className="space-y-1.5">
        <div className="text-muted-foreground text-[11px] font-medium tracking-wide uppercase">
          {t("web.admin.autoscan.activity.details.paths")}
        </div>
        <ChangeList event={event} librariesByID={librariesByID} />
      </div>
      <div className="space-y-1.5">
        <div className="text-muted-foreground text-[11px] font-medium tracking-wide uppercase">
          {t("web.admin.autoscan.activity.details.scansCreated")}
        </div>
        <RunList runs={event.scan_runs} joinedRuns={joinedRunCount(event)} />
      </div>
    </div>
  );
}

/** Runs the event's changes joined that another event created. */
function joinedRunCount(event: AutoscanEvent): number {
  const created = new Set(event.scan_runs.map((run) => run.id));
  const joined = new Set<string>();
  for (const change of event.changes) {
    if (change.outcome === "joined" && change.scan_run_id && !created.has(change.scan_run_id))
      joined.add(change.scan_run_id);
  }
  return joined.size;
}

function eventDetailsSummary(t: Translate, event: AutoscanEvent): string {
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

/** Plural values for a count printed without digit grouping. */
function rawCountValues(count: number) {
  return { count, count_text: count };
}

function PollMetricStrip({ event }: { event: AutoscanEvent }) {
  const t = useT();
  return (
    <div className="text-muted-foreground flex flex-wrap gap-x-4 gap-y-1 text-xs tabular-nums">
      <span>
        {t("web.admin.autoscan.activity.metrics.changes", rawCountValues(event.changes_returned))}
      </span>
      <span>
        {t("web.admin.autoscan.activity.metrics.targets", rawCountValues(event.targets_claimed))}
      </span>
      <span>
        {t("web.admin.autoscan.activity.metrics.created", rawCountValues(event.scans_created))}
      </span>
      <span>
        {t("web.admin.autoscan.activity.metrics.reused", rawCountValues(event.scans_reused))}
      </span>
      <span>
        {t(
          "web.admin.autoscan.activity.metrics.suppressed",
          rawCountValues(event.scans_suppressed),
        )}
      </span>
    </div>
  );
}

function QueueCard({
  scan,
  librariesByID,
  cancellingLibraryID,
  onCancel,
}: {
  scan: ScanRun;
  librariesByID: Map<number, Library>;
  cancellingLibraryID: number | null;
  onCancel: (libraryID: number) => void;
}) {
  const t = useT();
  const progress = formatActiveScanProgress(scan);
  return (
    <div className="border-border rounded-lg border p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <ScanStatusBadge status={scan.status} />
            <span className="font-medium">{libraryName(t, librariesByID, scan.library_id)}</span>
          </div>
          <div className="text-muted-foreground flex flex-wrap gap-x-3 gap-y-1 text-xs">
            <span>{formatActiveScanMode(scan)}</span>
            <span>{formatActiveScanTrigger(scan.trigger)}</span>
            <span>
              {scan.status === "running"
                ? formatActiveScanStarted(scan.started_at)
                : t("web.admin.autoscan.activity.queue.waiting")}
            </span>
          </div>
        </div>
        <Button
          variant="ghost"
          size="icon"
          className="text-destructive h-8 w-8"
          disabled={cancellingLibraryID === scan.library_id}
          onClick={() => onCancel(scan.library_id)}
          aria-label={t("web.admin.autoscan.activity.queue.cancelLabel")}
        >
          <Square className="h-3.5 w-3.5" />
        </Button>
      </div>
      <div className="text-muted-foreground mt-3 font-mono text-xs [overflow-wrap:anywhere]">
        {scan.path || t("web.admin.autoscan.activity.entireLibrary")}
      </div>
      {progress ? (
        <div className="text-muted-foreground mt-2 text-xs [overflow-wrap:anywhere]">
          {progress}
        </div>
      ) : null}
    </div>
  );
}

function AutoscanQueue({
  scans,
  statusActiveCount,
  librariesByID,
  cancellingLibraryID,
  onCancel,
}: {
  scans: ScanRun[];
  statusActiveCount: number;
  librariesByID: Map<number, Library>;
  cancellingLibraryID: number | null;
  onCancel: (libraryID: number) => void;
}) {
  const t = useT();
  const [page, setPage] = useState(0);
  const pageCount = Math.max(1, Math.ceil(scans.length / QUEUE_PAGE_SIZE));
  // Live scans complete out from under us; the active set length is known
  // synchronously, so derive the in-range page during render (no effect needed)
  // and the view self-heals when the queue shrinks below the current offset.
  const safePage = Math.min(page, pageCount - 1);
  const rows = scans.slice(safePage * QUEUE_PAGE_SIZE, (safePage + 1) * QUEUE_PAGE_SIZE);

  if (scans.length === 0) {
    return (
      <div className="border-border rounded-lg border border-dashed p-6">
        <div className="flex items-center gap-2 text-sm font-medium">
          <ScanLine className="text-muted-foreground h-4 w-4" />
          {t("web.admin.autoscan.activity.queue.title")}
        </div>
        <p className="text-muted-foreground mt-2 text-sm">
          {statusActiveCount > 0
            ? t("web.admin.autoscan.activity.queue.pendingDetails")
            : t("web.admin.autoscan.activity.queue.empty")}
        </p>
      </div>
    );
  }

  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="flex items-center gap-2 text-sm font-semibold">
            <ScanLine className="text-primary h-4 w-4" />
            {t("web.admin.autoscan.activity.queue.title")}
          </div>
          <p className="text-muted-foreground mt-1 text-xs">
            {t("web.admin.autoscan.activity.queue.description")}
          </p>
        </div>
        <Badge variant="secondary" className="tabular-nums">
          {t("web.admin.autoscan.activity.activeCount", rawCountValues(scans.length))}
        </Badge>
      </div>

      <div className="space-y-3 lg:hidden">
        {rows.map((scan) => (
          <QueueCard
            key={scan.id}
            scan={scan}
            librariesByID={librariesByID}
            cancellingLibraryID={cancellingLibraryID}
            onCancel={onCancel}
          />
        ))}
      </div>

      <DataTable
        head={
          <>
            <TableHead>{t("web.admin.autoscan.activity.column.status")}</TableHead>
            <TableHead>{t("web.admin.autoscan.activity.column.library")}</TableHead>
            <TableHead>{t("web.admin.autoscan.activity.column.scope")}</TableHead>
            <TableHead>{t("web.admin.autoscan.activity.column.progress")}</TableHead>
            <TableHead className="w-20 text-end">
              {t("web.admin.autoscan.activity.column.action")}
            </TableHead>
          </>
        }
      >
        {rows.map((scan) => {
          const progress = formatActiveScanProgress(scan);
          return (
            <TableRow key={scan.id}>
              <TableCell>
                <ScanStatusBadge status={scan.status} />
              </TableCell>
              <TableCell className="font-medium">
                {libraryName(t, librariesByID, scan.library_id)}
                <div className="text-muted-foreground mt-1 text-xs">
                  {scan.status === "running"
                    ? formatActiveScanStarted(scan.started_at)
                    : t("web.admin.autoscan.activity.queue.waiting")}
                </div>
              </TableCell>
              <TableCell>
                <div className="text-sm">{formatActiveScanMode(scan)}</div>
                <div className="text-muted-foreground mt-1 max-w-xl font-mono text-xs [overflow-wrap:anywhere] whitespace-normal">
                  {scan.path || t("web.admin.autoscan.activity.entireLibrary")}
                </div>
              </TableCell>
              <TableCell className="text-muted-foreground text-xs [overflow-wrap:anywhere]">
                {progress || formatActiveScanTrigger(scan.trigger)}
              </TableCell>
              <TableCell className="text-end">
                <Button
                  variant="ghost"
                  size="icon"
                  className="text-destructive h-8 w-8"
                  disabled={cancellingLibraryID === scan.library_id}
                  onClick={() => onCancel(scan.library_id)}
                  aria-label={t("web.admin.autoscan.activity.queue.cancelLabel")}
                >
                  <Square className="h-3.5 w-3.5" />
                </Button>
              </TableCell>
            </TableRow>
          );
        })}
      </DataTable>

      {scans.length > QUEUE_PAGE_SIZE ? (
        <TablePagination
          page={safePage}
          pageSize={QUEUE_PAGE_SIZE}
          total={scans.length}
          onPageChange={setPage}
          summaryKey="web.admin.common.pagination.scans"
        />
      ) : null}
    </section>
  );
}

function RunningPolls({
  polls,
  lookups,
}: {
  polls: AutoscanRunningPoll[];
  lookups: SourceLabelLookups;
}) {
  const t = useT();
  if (polls.length === 0) return null;

  return (
    <section className="space-y-3" role="status" aria-live="polite">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="flex items-center gap-2 text-sm font-semibold">
            <RefreshCw className="text-primary h-4 w-4 animate-spin" />
            {t("web.admin.autoscan.activity.polls.title")}
          </div>
          <p className="text-muted-foreground mt-1 text-xs">
            {t("web.admin.autoscan.activity.polls.description")}
          </p>
        </div>
        <Badge variant="secondary" className="tabular-nums">
          {t("web.admin.autoscan.activity.activeCount", rawCountValues(polls.length))}
        </Badge>
      </div>

      <div className="grid gap-3 lg:grid-cols-2">
        {polls.map((poll) => (
          <div key={poll.id} className="border-border rounded-lg border p-4">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div className="min-w-0">
                <div className="font-medium">{pollSourceName(t, poll, lookups)}</div>
                <div className="text-muted-foreground mt-1 text-xs [overflow-wrap:anywhere]">
                  {[poll.plugin_id, poll.capability_id].join(SEPARATOR)}
                </div>
              </div>
              <PollStatusBadge status="running" />
            </div>
            <div className="text-muted-foreground mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs tabular-nums">
              <span>
                {t("web.admin.autoscan.activity.polls.started", {
                  time: formatTimestamp(poll.started_at),
                })}
              </span>
              <span>
                {t("web.admin.autoscan.activity.polls.elapsed", {
                  duration: formatDuration(t, poll.elapsed_ms),
                })}
              </span>
            </div>
          </div>
        ))}
      </div>
    </section>
  );
}

function ScanHistoryCard({
  scan,
  librariesByID,
  lookups,
}: {
  scan: AutoscanScan;
  librariesByID: Map<number, Library>;
  lookups: SourceLabelLookups;
}) {
  const t = useT();
  return (
    <div className="border-border rounded-lg border p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <ScanStatusBadge status={scan.status} />
            <span className="font-medium">{libraryName(t, librariesByID, scan.library_id)}</span>
          </div>
          <div className="text-muted-foreground flex flex-wrap gap-x-3 gap-y-1 text-xs">
            <span>{formatActiveScanMode(scan)}</span>
            <span>{scanSourceName(t, scan, lookups)}</span>
            <span>
              {formatTimestamp(scan.completed_at ?? scan.started_at ?? scan.requested_at)}
            </span>
          </div>
        </div>
        {scan.event_status ? <PollStatusBadge status={scan.event_status} /> : null}
      </div>
      <div className="text-muted-foreground mt-3 font-mono text-xs [overflow-wrap:anywhere]">
        {scan.path || t("web.admin.autoscan.activity.entireLibrary")}
      </div>
      <ScanResultText result={scan.result} className="mt-2 block" />
      {scan.error_message ? (
        <div className="text-destructive mt-2 text-xs [overflow-wrap:anywhere]">
          {scan.error_message}
        </div>
      ) : null}
      <div className="text-muted-foreground mt-3 font-mono text-[11px] [overflow-wrap:anywhere]">
        {scan.id}
      </div>
    </div>
  );
}

export function ScanHistoryTable({
  scans,
  librariesByID,
  lookups,
}: {
  scans: AutoscanScan[];
  librariesByID: Map<number, Library>;
  lookups: SourceLabelLookups;
}) {
  const t = useT();
  return (
    <>
      <div className="space-y-3 lg:hidden">
        {scans.map((scan) => (
          <ScanHistoryCard
            key={scan.id}
            scan={scan}
            librariesByID={librariesByID}
            lookups={lookups}
          />
        ))}
      </div>
      <DataTable
        head={
          <>
            <TableHead>{t("web.admin.autoscan.activity.column.status")}</TableHead>
            <TableHead>{t("web.admin.autoscan.activity.column.library")}</TableHead>
            <TableHead>{t("web.admin.autoscan.activity.column.source")}</TableHead>
            <TableHead>{t("web.admin.autoscan.activity.column.scope")}</TableHead>
            <TableHead>{t("web.admin.autoscan.activity.column.result")}</TableHead>
            <TableHead>{t("web.admin.autoscan.activity.column.time")}</TableHead>
            <TableHead>{t("web.admin.autoscan.activity.column.poll")}</TableHead>
          </>
        }
      >
        {scans.map((scan) => (
          <TableRow key={scan.id}>
            <TableCell>
              <ScanStatusBadge status={scan.status} />
            </TableCell>
            <TableCell className="font-medium">
              {libraryName(t, librariesByID, scan.library_id)}
              <div className="text-muted-foreground mt-1 font-mono text-[11px]">{scan.id}</div>
            </TableCell>
            <TableCell className="whitespace-nowrap">{scanSourceName(t, scan, lookups)}</TableCell>
            {/* Table cells default to nowrap. The path and error set their own
                width bounds so the text wraps within them, and the error keeps a
                minimum so a failed scan's column does not collapse to a sliver. */}
            <TableCell>
              <div className="text-sm">{formatActiveScanMode(scan)}</div>
              <div className="text-muted-foreground mt-1 max-w-xl font-mono text-xs [overflow-wrap:anywhere] whitespace-normal">
                {scan.path || t("web.admin.autoscan.activity.entireLibrary")}
              </div>
              {scan.error_message ? (
                <div className="text-destructive mt-1 max-w-xl min-w-[16rem] text-xs [overflow-wrap:anywhere] whitespace-normal">
                  {scan.error_message}
                </div>
              ) : null}
            </TableCell>
            <TableCell className="max-w-56">
              {scan.result ? (
                <ScanResultText result={scan.result} />
              ) : (
                <span className="text-muted-foreground">{NO_VALUE}</span>
              )}
            </TableCell>
            <TableCell className="text-muted-foreground whitespace-nowrap tabular-nums">
              {formatTimestamp(scan.completed_at ?? scan.started_at ?? scan.requested_at)}
            </TableCell>
            <TableCell>
              {scan.event_status ? <PollStatusBadge status={scan.event_status} /> : NO_VALUE}
            </TableCell>
          </TableRow>
        ))}
      </DataTable>
    </>
  );
}

function PollEventCard({
  event,
  lookups,
  librariesByID,
}: {
  event: AutoscanEvent;
  lookups: SourceLabelLookups;
  librariesByID: Map<number, Library>;
}) {
  const t = useT();
  return (
    <div className="border-border rounded-lg border p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <PollStatusBadge status={event.status} />
            <span className="font-medium">{pollSourceName(t, event, lookups)}</span>
            <DeliveryBadge event={event} />
          </div>
          <PollMetricStrip event={event} />
        </div>
        <div className="text-muted-foreground text-end text-xs tabular-nums">
          <div>{pollEventTimestamp(event)}</div>
          <div>{pollEventDuration(t, event)}</div>
        </div>
      </div>
      {event.error_message ? (
        <p className="text-destructive mt-3 text-xs [overflow-wrap:anywhere]">
          {event.error_message}
        </p>
      ) : null}
      <details className="mt-3">
        <summary className="text-muted-foreground cursor-pointer text-xs">
          {eventDetailsSummary(t, event)}
        </summary>
        <div className="mt-2">
          <EventDetails event={event} librariesByID={librariesByID} />
        </div>
      </details>
    </div>
  );
}

export function PollEventTable({
  events,
  lookups,
  librariesByID,
}: {
  events: AutoscanEvent[];
  lookups: SourceLabelLookups;
  librariesByID: Map<number, Library>;
}) {
  const t = useT();
  return (
    <>
      <div className="space-y-3 lg:hidden">
        {events.map((event) => (
          <PollEventCard
            key={event.id}
            event={event}
            lookups={lookups}
            librariesByID={librariesByID}
          />
        ))}
      </div>
      <DataTable
        head={
          <>
            <TableHead>{t("web.admin.autoscan.activity.column.status")}</TableHead>
            <TableHead>{t("web.admin.autoscan.activity.column.source")}</TableHead>
            <TableHead>{t("web.admin.autoscan.activity.column.counts")}</TableHead>
            <TableHead>{t("web.admin.autoscan.activity.column.details")}</TableHead>
            <TableHead>{t("web.admin.autoscan.activity.column.duration")}</TableHead>
            <TableHead>{t("web.admin.autoscan.activity.column.time")}</TableHead>
          </>
        }
      >
        {events.map((event) => (
          <TableRow key={event.id}>
            <TableCell>
              <PollStatusBadge status={event.status} />
            </TableCell>
            <TableCell className="font-medium">
              <div className="flex flex-wrap items-center gap-1.5">
                {pollSourceName(t, event, lookups)}
                <DeliveryBadge event={event} />
              </div>
            </TableCell>
            <TableCell>
              <PollMetricStrip event={event} />
              {/* Table cells default to nowrap; the error must wrap within its cap. */}
              {event.error_message ? (
                <div className="text-destructive mt-1 max-w-md text-xs [overflow-wrap:anywhere] whitespace-normal">
                  {event.error_message}
                </div>
              ) : null}
            </TableCell>
            <TableCell>
              <details>
                <summary className="text-muted-foreground cursor-pointer text-xs whitespace-nowrap">
                  {eventDetailsSummary(t, event)}
                </summary>
                {/* Table cells default to nowrap; paths and explanations must wrap. */}
                <div className="mt-2 max-w-[32rem] min-w-[16rem] whitespace-normal">
                  <EventDetails event={event} librariesByID={librariesByID} />
                </div>
              </details>
            </TableCell>
            <TableCell className="whitespace-nowrap tabular-nums">
              {pollEventDuration(t, event)}
            </TableCell>
            <TableCell className="text-muted-foreground whitespace-nowrap tabular-nums">
              {pollEventTimestamp(event)}
            </TableCell>
          </TableRow>
        ))}
      </DataTable>
    </>
  );
}

function HistorySkeleton() {
  return (
    <div className="space-y-3" aria-hidden="true">
      {Array.from({ length: 6 }).map((_, i) => (
        <Skeleton key={i} className="h-16 w-full rounded-lg" />
      ))}
    </div>
  );
}

function StatTile({
  label,
  value,
  icon: Icon,
}: {
  label: string;
  value: ReactNode;
  icon?: typeof ScanLine;
}) {
  return (
    <div>
      <div className="text-muted-foreground flex items-center gap-2 text-xs">
        {Icon ? <Icon className="h-3.5 w-3.5" /> : null}
        {label}
      </div>
      <div className="mt-1 text-2xl font-semibold tabular-nums">{value}</div>
    </div>
  );
}

export default function ActivityPanel() {
  const t = useT();
  useEventChannel("scans");
  const [historyView, setHistoryView] = useState<HistoryView>("scans");
  const [historyQuery, setHistoryQuery] = useState("");
  const [scanStatus, setScanStatus] = useState<AutoscanScanStatus | "all">("all");
  const [pollStatus, setPollStatus] = useState<AutoscanEventStatus | "all">("all");
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(DEFAULT_HISTORY_PAGE_SIZE);
  const debouncedHistoryQuery = useDebounce(historyQuery.trim(), 300);

  const scanHistory = useAutoscanScans({
    limit: pageSize,
    offset: page * pageSize,
    query: debouncedHistoryQuery || undefined,
    status: scanStatus === "all" ? undefined : scanStatus,
    enabled: historyView === "scans",
  });
  const pollEvents = useAutoscanEvents({
    limit: pageSize,
    offset: page * pageSize,
    query: debouncedHistoryQuery || undefined,
    status: pollStatus === "all" ? undefined : pollStatus,
    enabled: historyView === "polls",
  });
  const status = useAutoscanStatus();
  const { data: activeScans = [] } = useActiveScans();
  const { data: libraries = [] } = useAdminLibraries();
  const { data: autoscanSources = [] } = useAutoscanSources();
  const { data: autoscanConnections = [] } = useAutoscanConnections();
  const available = useAvailableScanSources();
  const cancelScans = useCancelLibraryScans();

  const queue = status.data;
  const librariesByID = useMemo(
    () => new Map(libraries.map((library) => [library.id, library])),
    [libraries],
  );
  const labelLookups: SourceLabelLookups = useMemo(
    () => ({
      sourceByID: new Map(autoscanSources.map((s) => [s.id, s])),
      connectionByID: new Map(autoscanConnections.map((c) => [c.id, c.name])),
      displayNames: buildPluginDisplayNames(available.data ?? []),
    }),
    [autoscanSources, autoscanConnections, available.data],
  );
  const autoscanQueue = useMemo(
    () =>
      activeScans
        .filter((scan) => scan.trigger === "autoscan")
        .slice()
        .sort(compareActiveScans),
    [activeScans],
  );

  const activeQuery = historyView === "scans" ? scanHistory : pollEvents;
  const activeRows = activeQuery.data?.rows ?? [];
  const activeTotal = activeQuery.data?.total ?? 0;
  const activeStatus = historyView === "scans" ? scanStatus : pollStatus;
  const hasHistoryFilters = debouncedHistoryQuery !== "" || activeStatus !== "all";
  const isRefreshing = status.isFetching || activeQuery.isFetching;
  const summaryKey =
    historyView === "scans"
      ? "web.admin.common.pagination.scans"
      : "web.admin.common.pagination.polls";

  // If a background refresh shrinks the result set below the current page,
  // pull the viewer back to the last page that still has rows. Adjusting state
  // during render (React's escape hatch) converges in one guarded step and
  // avoids an extra effect pass; the offset query then refetches the valid page.
  const historyPageCount = Math.max(1, Math.ceil(activeTotal / pageSize));
  if (page > 0 && page >= historyPageCount && !activeQuery.isFetching) {
    setPage(historyPageCount - 1);
  }

  function refresh() {
    void status.refetch();
    void activeQuery.refetch();
  }

  function resetHistoryFilters() {
    setHistoryQuery("");
    setScanStatus("all");
    setPollStatus("all");
    setPage(0);
  }

  function switchHistoryView(nextView: HistoryView) {
    setHistoryView(nextView);
    setPage(0);
  }

  return (
    <div className="space-y-6">
      <div className="border-border grid gap-4 rounded-lg border p-4 sm:grid-cols-5">
        <StatTile
          label={t("web.admin.autoscan.activity.stats.active")}
          value={queue?.active_scans ?? 0}
          icon={ScanLine}
        />
        <StatTile
          label={t("web.admin.autoscan.activity.stats.queued")}
          value={queue?.accepted_scans ?? 0}
        />
        <StatTile
          label={t("web.admin.autoscan.activity.stats.runningScans")}
          value={queue?.running_scans ?? 0}
        />
        <StatTile
          label={t("web.admin.autoscan.activity.stats.polling")}
          value={queue?.running_polls?.length ?? 0}
          icon={RefreshCw}
        />
        <div className="flex items-start justify-between gap-3 sm:block">
          <StatTile
            label={t("web.admin.autoscan.activity.stats.latestPoll")}
            value={formatTime(queue?.latest_event_at)}
            icon={Clock}
          />
          <Button
            variant="outline"
            size="icon"
            className="sm:mt-2"
            onClick={refresh}
            disabled={isRefreshing}
            aria-label={t("web.admin.autoscan.activity.refreshLabel")}
          >
            <RefreshCw className={cn(isRefreshing && "animate-spin")} />
          </Button>
        </div>
      </div>

      <RunningPolls polls={queue?.running_polls ?? []} lookups={labelLookups} />

      <AutoscanQueue
        scans={autoscanQueue}
        statusActiveCount={queue?.active_scans ?? 0}
        librariesByID={librariesByID}
        cancellingLibraryID={cancelScans.variables ?? null}
        onCancel={(libraryID) => cancelScans.mutate(libraryID)}
      />

      <section className="space-y-3">
        <div className="flex flex-col gap-3 lg:flex-row lg:items-end lg:justify-between">
          <div>
            <div className="flex items-center gap-2 text-sm font-semibold">
              {historyView === "scans" ? (
                <ListChecks className="text-primary h-4 w-4" />
              ) : (
                <History className="text-primary h-4 w-4" />
              )}
              {historyView === "scans"
                ? t("web.admin.autoscan.activity.history.scansTitle")
                : t("web.admin.autoscan.activity.history.pollsTitle")}
            </div>
            <p className="text-muted-foreground mt-1 text-xs">
              {historyView === "scans"
                ? t("web.admin.autoscan.activity.history.scansDescription")
                : t("web.admin.autoscan.activity.history.pollsDescription")}
            </p>
          </div>
          <div className="grid gap-2 sm:grid-cols-[auto_minmax(0,1fr)_10rem_auto] lg:w-[50rem]">
            <div className="border-border bg-background flex rounded-md border p-1">
              <Button
                type="button"
                variant={historyView === "scans" ? "secondary" : "ghost"}
                size="sm"
                className="h-7"
                onClick={() => switchHistoryView("scans")}
              >
                {t("web.admin.autoscan.activity.history.scans")}
              </Button>
              <Button
                type="button"
                variant={historyView === "polls" ? "secondary" : "ghost"}
                size="sm"
                className="h-7"
                onClick={() => switchHistoryView("polls")}
              >
                {t("web.admin.autoscan.activity.history.polls")}
              </Button>
            </div>
            <div className="relative min-w-0">
              <Search className="text-muted-foreground pointer-events-none absolute start-3 top-1/2 h-4 w-4 -translate-y-1/2" />
              <Input
                value={historyQuery}
                onChange={(event) => {
                  setHistoryQuery(event.target.value);
                  setPage(0);
                }}
                placeholder={
                  historyView === "scans"
                    ? t("web.admin.autoscan.activity.history.searchScans")
                    : t("web.admin.autoscan.activity.history.searchPolls")
                }
                className="ps-9 pe-9"
              />
              {historyQuery ? (
                <button
                  type="button"
                  className="text-muted-foreground hover:text-foreground absolute end-2 top-1/2 rounded p-1 transition-colors"
                  onClick={() => setHistoryQuery("")}
                  aria-label={t("web.admin.autoscan.activity.history.clearSearch")}
                >
                  <X className="h-3.5 w-3.5" />
                </button>
              ) : null}
            </div>
            {historyView === "scans" ? (
              <Select
                value={scanStatus}
                onValueChange={(value) => {
                  setScanStatus(value as AutoscanScanStatus | "all");
                  setPage(0);
                }}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">
                    {t("web.admin.autoscan.activity.filter.allStatuses")}
                  </SelectItem>
                  <SelectItem value="accepted">
                    {t("web.admin.autoscan.activity.scanFilter.queued")}
                  </SelectItem>
                  <SelectItem value="running">
                    {t("web.admin.autoscan.activity.scanFilter.running")}
                  </SelectItem>
                  <SelectItem value="completed">
                    {t("web.admin.autoscan.activity.scanFilter.completed")}
                  </SelectItem>
                  <SelectItem value="failed">
                    {t("web.admin.autoscan.activity.scanFilter.failed")}
                  </SelectItem>
                  <SelectItem value="cancelled">
                    {t("web.admin.autoscan.activity.scanFilter.cancelled")}
                  </SelectItem>
                </SelectContent>
              </Select>
            ) : (
              <Select
                value={pollStatus}
                onValueChange={(value) => {
                  setPollStatus(value as AutoscanEventStatus | "all");
                  setPage(0);
                }}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">
                    {t("web.admin.autoscan.activity.filter.allStatuses")}
                  </SelectItem>
                  <SelectItem value="running">
                    {t("web.admin.autoscan.activity.pollStatus.running")}
                  </SelectItem>
                  <SelectItem value="success">
                    {t("web.admin.autoscan.activity.pollStatus.success")}
                  </SelectItem>
                  <SelectItem value="unresolved">
                    {t("web.admin.autoscan.activity.pollStatus.unresolved")}
                  </SelectItem>
                  <SelectItem value="error">
                    {t("web.admin.autoscan.activity.pollStatus.error")}
                  </SelectItem>
                </SelectContent>
              </Select>
            )}
            <Button variant="outline" onClick={resetHistoryFilters} disabled={!hasHistoryFilters}>
              {t("web.admin.autoscan.activity.history.reset")}
            </Button>
          </div>
        </div>

        {activeQuery.isLoading ? (
          <HistorySkeleton />
        ) : activeQuery.isError ? (
          <div className="border-destructive/30 bg-destructive/5 rounded-lg border p-8 text-center">
            <p className="text-destructive text-sm">
              {t("web.admin.autoscan.activity.history.loadFailed")}
            </p>
            <Button variant="outline" size="sm" className="mt-3" onClick={refresh}>
              {t("common.tryAgain")}
            </Button>
          </div>
        ) : activeRows.length === 0 ? (
          <div className="rounded-lg border border-dashed p-8 text-center">
            <p className="text-muted-foreground text-sm">
              {hasHistoryFilters
                ? t("web.admin.autoscan.activity.history.emptyFiltered")
                : historyView === "scans"
                  ? t("web.admin.autoscan.activity.history.emptyScans")
                  : t("web.admin.autoscan.activity.history.emptyPolls")}
            </p>
          </div>
        ) : (
          <div className="space-y-4">
            {historyView === "scans" ? (
              <ScanHistoryTable
                scans={activeRows as AutoscanScan[]}
                librariesByID={librariesByID}
                lookups={labelLookups}
              />
            ) : (
              <PollEventTable
                events={activeRows as AutoscanEvent[]}
                lookups={labelLookups}
                librariesByID={librariesByID}
              />
            )}

            <TablePagination
              page={page}
              pageSize={pageSize}
              total={activeTotal}
              onPageChange={setPage}
              onPageSizeChange={(size) => {
                setPageSize(size);
                setPage(0);
              }}
              pageSizeOptions={HISTORY_PAGE_SIZE_OPTIONS}
              summaryKey={summaryKey}
              isFetching={activeQuery.isFetching}
            />
          </div>
        )}
      </section>
    </div>
  );
}
