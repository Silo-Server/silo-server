import { useState } from "react";
import {
  ArrowRight,
  CheckCircle2,
  ChevronDown,
  ChevronUp,
  DatabaseBackup,
  FolderOpen,
  Square,
} from "lucide-react";

import {
  getLibraryScanGroupName,
  scanProgressPercent,
  type LibraryRefreshEntry,
  type LibraryScanGroup,
} from "@/components/admin/libraries/libraryActivityHelpers";
import { findLibraryType } from "@/components/admin/libraries/libraryTypes";
import { formatJobProgress } from "@/components/adminCatalogMaintenanceFormatters";
import { PathText } from "@/components/PathText";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { numberText, useT } from "@/i18n";
import {
  formatActiveScanMode,
  formatActiveScanProgress,
  formatActiveScanStarted,
  formatActiveScanTarget,
  formatActiveScanTrigger,
} from "@/lib/scanRuns";
import { cn } from "@/lib/utils";

// Not translated: separator glyphs.
const SEPARATOR = "·";
const SPACED_SEPARATOR = ` ${SEPARATOR} `;
const EM_DASH = "—";
const COMMA_SEPARATOR = ", ";
const OPEN_PAREN = " (";
const CLOSE_PAREN = ")";
const SPACE = " ";
const QUEUED_PREVIEW_LIMIT = 2;

/** Calm single-line summary banner shown above the Libraries list while scans or refreshes run. */
export function LiveActivitySummaryStrip({
  groups,
  refreshEntries,
  onViewQueue,
}: {
  groups: LibraryScanGroup[];
  refreshEntries: LibraryRefreshEntry[];
  onViewQueue: () => void;
}) {
  const t = useT();
  const totalRunning = groups.reduce((sum, group) => sum + group.runningCount, 0);
  const totalQueued = groups.reduce((sum, group) => sum + group.queuedCount, 0);
  const runningRefreshes = refreshEntries.filter((entry) => entry.job.status === "running").length;
  const totalRefreshes = refreshEntries.length;

  if (totalRunning === 0 && totalQueued === 0 && totalRefreshes === 0) {
    return null;
  }

  const headlineParts: string[] = [];
  if (totalRunning > 0) {
    headlineParts.push(
      t("web.admin.libraries.scans.running", {
        count: totalRunning,
        count_text: numberText(totalRunning),
      }),
    );
  }
  if (totalQueued > 0) {
    headlineParts.push(
      t("web.admin.libraries.scans.queued", {
        count: totalQueued,
        count_text: numberText(totalQueued),
      }),
    );
  }
  if (totalRefreshes > 0 && headlineParts.length === 0) {
    headlineParts.push(
      runningRefreshes > 0
        ? t("web.admin.libraries.activity.refreshingMetadata")
        : t("web.admin.libraries.activeWork.refreshQueued"),
    );
  }

  const primaryGroup = groups[0];
  const primaryRefresh = !primaryGroup ? refreshEntries[0] : undefined;
  let detailSummary: React.ReactNode = null;
  if (primaryGroup) {
    const name = getLibraryScanGroupName(t, primaryGroup.library, primaryGroup.libraryID);
    const sampleTargets = primaryGroup.scans
      .slice(0, 2)
      .map((scan) => formatActiveScanTarget(scan));
    const otherCount = groups.length - 1;
    detailSummary = (
      <>
        <span dir="auto" className="bidi-text">
          {name}
        </span>
        {sampleTargets.length > 0 ? (
          <>
            {OPEN_PAREN}
            {sampleTargets.map((target, index) => (
              <span key={`${target}-${index}`}>
                {index > 0 ? COMMA_SEPARATOR : null}
                <span dir="ltr">
                  <PathText path={target} />
                </span>
              </span>
            ))}
            {CLOSE_PAREN}
          </>
        ) : null}
        {otherCount > 0 ? (
          <>
            {SPACE}
            {t("web.admin.libraries.activity.moreLibraries", { count: otherCount })}
          </>
        ) : null}
      </>
    );
  } else if (primaryRefresh) {
    const name = getLibraryScanGroupName(t, primaryRefresh.library, primaryRefresh.libraryID);
    detailSummary = (
      <>
        <span dir="auto" className="bidi-text">
          {name}
        </span>
        {primaryRefresh.job.message ? (
          <>
            {SPACED_SEPARATOR}
            <span dir="auto" className="bidi-text">
              {primaryRefresh.job.message}
            </span>
          </>
        ) : null}
      </>
    );
  }

  const isRunning = totalRunning > 0 || runningRefreshes > 0;

  return (
    <div
      className={cn(
        "surface-panel flex flex-wrap items-center justify-between gap-3 rounded-2xl border px-4 py-2.5",
        isRunning ? "border-success/30" : "border-border/60",
      )}
    >
      <div className="flex min-w-0 flex-1 items-center gap-2.5">
        <span className="relative flex h-2 w-2 shrink-0">
          {isRunning ? (
            <>
              <span className="bg-success absolute inline-flex h-full w-full animate-ping rounded-full opacity-75" />
              <span className="bg-success relative inline-flex h-2 w-2 rounded-full" />
            </>
          ) : (
            <span className="bg-muted-foreground/50 relative inline-flex h-2 w-2 rounded-full" />
          )}
        </span>
        <div className="flex min-w-0 flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
          <span className="text-foreground text-xs font-semibold tabular-nums">
            {headlineParts.join(SPACED_SEPARATOR)}
          </span>
          {detailSummary ? (
            <span className="text-muted-foreground truncate text-xs">{detailSummary}</span>
          ) : null}
        </div>
      </div>
      <Button
        variant="outline"
        size="sm"
        className="h-7 shrink-0 gap-1 text-xs max-lg:h-9"
        onClick={onViewQueue}
      >
        {t("web.admin.libraries.activity.viewQueue")}
        <ArrowRight className="h-3 w-3 rtl:rotate-180" aria-hidden="true" />
      </Button>
    </div>
  );
}

/** Dedicated Scan Activity tab: filterable per-library scan progress, collapsed queues, and metadata refreshes. */
export function LibraryActivityPanel({
  groups,
  refreshEntries,
  refreshJobsLoading = false,
  refreshJobsError = false,
  selectedLibraryID,
  onSelectLibraryID,
  cancellingLibraryID,
  cancellingJobID,
  onCancelScans,
  onCancelJob,
  footer,
}: {
  groups: LibraryScanGroup[];
  refreshEntries: LibraryRefreshEntry[];
  refreshJobsLoading?: boolean;
  refreshJobsError?: boolean;
  selectedLibraryID: number | null;
  onSelectLibraryID: (libraryID: number | null) => void;
  cancellingLibraryID: number | null;
  cancellingJobID: string | undefined;
  onCancelScans: (libraryID: number) => void;
  onCancelJob: (jobID: string) => void;
  footer?: React.ReactNode;
}) {
  const t = useT();

  const totalScans = groups.reduce((sum, group) => sum + group.scans.length, 0);
  const totalActive = totalScans + refreshEntries.length;

  // Build unique library filters covering both active scans and active metadata refreshes.
  const filterEntries = (() => {
    const map = new Map<number, { libraryID: number; name: string; count: number }>();
    for (const group of groups) {
      map.set(group.libraryID, {
        libraryID: group.libraryID,
        name: getLibraryScanGroupName(t, group.library, group.libraryID),
        count: group.scans.length,
      });
    }
    for (const entry of refreshEntries) {
      const existing = map.get(entry.libraryID);
      if (existing) {
        existing.count += 1;
      } else {
        map.set(entry.libraryID, {
          libraryID: entry.libraryID,
          name: getLibraryScanGroupName(t, entry.library, entry.libraryID),
          count: 1,
        });
      }
    }
    return Array.from(map.values());
  })();

  const effectiveFilter =
    selectedLibraryID !== null && filterEntries.some((f) => f.libraryID === selectedLibraryID)
      ? selectedLibraryID
      : null;

  const visibleGroups =
    effectiveFilter === null
      ? groups
      : groups.filter((group) => group.libraryID === effectiveFilter);
  const visibleRefreshes =
    effectiveFilter === null
      ? refreshEntries
      : refreshEntries.filter((entry) => entry.libraryID === effectiveFilter);

  if (totalActive === 0) {
    if (refreshJobsError) {
      return <div className="space-y-4">{footer}</div>;
    }
    if (refreshJobsLoading) {
      return (
        <div className="surface-panel text-muted-foreground rounded-2xl border-0 px-6 py-12 text-center text-sm">
          {t("common.loading")}
        </div>
      );
    }
    return (
      <div className="space-y-4">
        <div className="surface-panel flex flex-col items-center justify-center gap-2 rounded-2xl border-0 px-6 py-12 text-center">
          <div className="bg-muted/60 flex h-10 w-10 items-center justify-center rounded-xl">
            <CheckCircle2 className="text-muted-foreground h-5 w-5" aria-hidden="true" />
          </div>
          <h2 className="text-sm font-semibold">{t("web.admin.libraries.activity.emptyTitle")}</h2>
          <p className="text-muted-foreground max-w-md text-xs">
            {t("web.admin.libraries.activity.emptyDescription")}
          </p>
        </div>
        {footer}
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {filterEntries.length > 1 ? (
        <div className="flex flex-wrap items-center gap-1.5">
          <button
            type="button"
            aria-pressed={effectiveFilter === null}
            className={cn(
              "rounded-full border px-3 py-1 text-xs font-medium transition-colors max-lg:min-h-9",
              effectiveFilter === null
                ? "bg-foreground text-background border-transparent"
                : "border-border bg-card text-muted-foreground hover:text-foreground",
            )}
            onClick={() => onSelectLibraryID(null)}
          >
            {t("web.admin.libraries.activity.filterAll", { count: totalActive })}
          </button>
          {filterEntries.map((entry) => (
            <button
              key={entry.libraryID}
              type="button"
              aria-pressed={effectiveFilter === entry.libraryID}
              className={cn(
                "inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs font-medium tabular-nums transition-colors max-lg:min-h-9",
                effectiveFilter === entry.libraryID
                  ? "bg-foreground text-background border-transparent"
                  : "border-border bg-card text-muted-foreground hover:text-foreground",
              )}
              onClick={() => onSelectLibraryID(entry.libraryID)}
            >
              <span dir="auto" className="bidi-text">
                {entry.name}
              </span>
              <span
                className={cn(
                  "rounded-full px-1.5 py-px text-[10px]",
                  effectiveFilter === entry.libraryID
                    ? "bg-background/20 text-background"
                    : "bg-secondary text-secondary-foreground",
                )}
              >
                {numberText(entry.count)}
              </span>
            </button>
          ))}
        </div>
      ) : null}

      {visibleRefreshes.length > 0 ? (
        <div className="space-y-2.5">
          <h2 className="text-muted-foreground text-xs font-semibold tracking-wider uppercase">
            {t("web.admin.libraries.activity.refreshSectionTitle")}
          </h2>
          {visibleRefreshes.map((entry) => {
            const TypeIcon = findLibraryType(entry.library?.type ?? "")?.icon ?? FolderOpen;
            const name = getLibraryScanGroupName(t, entry.library, entry.libraryID);
            const cancelling = cancellingJobID === entry.job.id;
            return (
              <section
                key={entry.job.id}
                className="surface-panel flex flex-wrap items-center justify-between gap-3 rounded-2xl border-0 p-4"
              >
                <div className="flex min-w-0 flex-1 items-start gap-3">
                  <div className="bg-secondary flex h-8 w-8 shrink-0 items-center justify-center rounded-lg">
                    <TypeIcon className="text-muted-foreground h-4 w-4" aria-hidden="true" />
                  </div>
                  <div className="min-w-0 flex-1 space-y-0.5">
                    <div className="flex flex-wrap items-center gap-2">
                      <span dir="auto" className="bidi-text text-sm font-semibold">
                        {name}
                      </span>
                      <Badge variant="secondary" className="gap-1 text-[11px]">
                        <DatabaseBackup className="h-3 w-3" aria-hidden="true" />
                        {t("web.admin.libraries.activeWork.metadata")}
                      </Badge>
                    </div>
                    <p className="text-muted-foreground truncate text-xs">
                      {entry.job.message || t("web.admin.libraries.activeWork.refreshQueued")}
                    </p>
                    <p className="text-muted-foreground/80 truncate text-[11px] tabular-nums">
                      {formatJobProgress(entry.job)}
                    </p>
                  </div>
                </div>
                <Button
                  variant="outline"
                  size="sm"
                  className="border-destructive/30 bg-destructive/10 text-destructive hover:bg-destructive/20 hover:text-destructive h-7 gap-1.5 text-xs max-lg:h-10"
                  disabled={cancelling}
                  aria-label={t("web.admin.libraries.activeWork.cancelRefresh")}
                  onClick={() => onCancelJob(entry.job.id)}
                >
                  <Square className="h-2.5 w-2.5 fill-current" aria-hidden="true" />
                  {t("web.admin.libraries.list.stopAction")}
                </Button>
              </section>
            );
          })}
        </div>
      ) : null}

      <div className="space-y-3">
        {visibleGroups.map((group) => (
          <ActivityLibraryCard
            key={group.libraryID}
            group={group}
            cancelling={cancellingLibraryID === group.libraryID}
            onCancel={onCancelScans}
          />
        ))}
      </div>

      {footer}
    </div>
  );
}

function ActivityLibraryCard({
  group,
  cancelling,
  onCancel,
}: {
  group: LibraryScanGroup;
  cancelling: boolean;
  onCancel: (libraryID: number) => void;
}) {
  const t = useT();
  const [queueExpanded, setQueueExpanded] = useState(false);
  const TypeIcon = findLibraryType(group.library?.type ?? "")?.icon ?? FolderOpen;
  const name = getLibraryScanGroupName(t, group.library, group.libraryID);
  const runningScans = group.scans.filter((scan) => scan.status === "running");
  const queuedScans = group.scans.filter((scan) => scan.status !== "running");

  const countParts: string[] = [];
  if (group.runningCount > 0) {
    countParts.push(
      t("web.admin.libraries.scans.running", {
        count: group.runningCount,
        count_text: numberText(group.runningCount),
      }),
    );
  }
  if (group.queuedCount > 0) {
    countParts.push(
      t("web.admin.libraries.scans.queued", {
        count: group.queuedCount,
        count_text: numberText(group.queuedCount),
      }),
    );
  }

  const queuedPreview = queuedScans.slice(0, QUEUED_PREVIEW_LIMIT).map(formatActiveScanTarget);
  const remainingQueued = Math.max(0, queuedScans.length - QUEUED_PREVIEW_LIMIT);

  return (
    <section className="surface-panel space-y-3 rounded-2xl border-0 p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2.5">
          <div className="bg-secondary flex h-8 w-8 shrink-0 items-center justify-center rounded-lg">
            <TypeIcon className="text-muted-foreground h-4 w-4" aria-hidden="true" />
          </div>
          <h3 dir="auto" className="bidi-text truncate text-sm font-semibold">
            {name}
          </h3>
          <Badge variant="secondary" className="text-[11px] tabular-nums">
            {countParts.join(SPACED_SEPARATOR)}
          </Badge>
        </div>
        <Button
          variant="outline"
          size="sm"
          className="border-destructive/30 bg-destructive/10 text-destructive hover:bg-destructive/20 hover:text-destructive h-7 gap-1.5 text-xs max-lg:h-10"
          disabled={cancelling}
          aria-label={t("web.admin.libraries.activeWork.cancelScans")}
          onClick={() => onCancel(group.libraryID)}
        >
          <Square className="h-2.5 w-2.5 fill-current" aria-hidden="true" />
          {t("web.admin.libraries.list.stopAction")}
        </Button>
      </div>

      {runningScans.length > 0 ? (
        <div className="space-y-2">
          {runningScans.map((scan) => {
            const pct = scanProgressPercent(scan);
            const progressText = formatActiveScanProgress(scan);
            const scanLabelId = `scan-progress-label-${scan.id}`;
            return (
              <div
                key={scan.id}
                className="border-border/50 bg-secondary/40 space-y-2 rounded-xl border px-3 py-2.5"
              >
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div className="flex min-w-0 flex-1 items-center gap-2">
                    <span className="relative flex h-2 w-2 shrink-0">
                      <span className="bg-success absolute inline-flex h-full w-full animate-ping rounded-full opacity-75" />
                      <span className="bg-success relative inline-flex h-2 w-2 rounded-full" />
                    </span>
                    <span id={scanLabelId} className="flex min-w-0 items-center gap-2">
                      <span className="shrink-0 text-xs font-medium">
                        {formatActiveScanMode(scan)}
                      </span>
                      <code
                        dir="ltr"
                        className="text-foreground/90 truncate font-mono text-xs"
                        title={scan.path}
                      >
                        <PathText path={formatActiveScanTarget(scan)} />
                      </code>
                    </span>
                    {scan.trigger ? (
                      <Badge variant="secondary" className="px-1.5 py-0 text-[10px]">
                        {formatActiveScanTrigger(scan.trigger)}
                      </Badge>
                    ) : null}
                  </div>
                  <span className="text-muted-foreground shrink-0 text-[11px] tabular-nums">
                    {progressText || formatActiveScanStarted(scan.started_at)}
                  </span>
                </div>
                {pct !== null ? (
                  <div
                    role="progressbar"
                    aria-labelledby={scanLabelId}
                    aria-valuemin={0}
                    aria-valuemax={100}
                    aria-valuenow={pct}
                    className="bg-border/70 h-1.5 w-full overflow-hidden rounded-full forced-colors:bg-transparent forced-colors:outline forced-colors:outline-[CanvasText]"
                  >
                    <div
                      className="bg-success h-full rounded-full transition-[width] duration-300 forced-colors:bg-[Highlight]"
                      style={{ width: `${pct}%` }}
                    />
                  </div>
                ) : null}
              </div>
            );
          })}
        </div>
      ) : null}

      {queuedScans.length > 0 ? (
        <div className="border-border/60 rounded-xl border border-dashed px-3 py-2">
          <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
            <div className="text-muted-foreground flex min-w-0 flex-1 flex-wrap items-center gap-x-1.5 gap-y-0.5">
              <span className="text-foreground/85 font-medium tabular-nums">
                {t("web.admin.libraries.scans.queued", {
                  count: queuedScans.length,
                  count_text: numberText(queuedScans.length),
                })}
              </span>
              <span aria-hidden="true">{EM_DASH}</span>
              <span dir="ltr" className="truncate font-mono text-[11px]">
                {queuedPreview.map((target, index) => (
                  <span key={`${target}-${index}`}>
                    {index > 0 ? COMMA_SEPARATOR : null}
                    <PathText path={target} />
                  </span>
                ))}
              </span>
              {remainingQueued > 0 ? (
                <span className="shrink-0 tabular-nums">
                  {t("web.admin.libraries.scanQueue.more", {
                    count: remainingQueued,
                    count_text: numberText(remainingQueued),
                  })}
                </span>
              ) : null}
            </div>
            {queuedScans.length > QUEUED_PREVIEW_LIMIT || queueExpanded ? (
              <Button
                variant="ghost"
                size="sm"
                className="text-muted-foreground hover:text-foreground h-6 gap-1 px-2 text-[11px] max-lg:h-9"
                aria-expanded={queueExpanded}
                onClick={() => setQueueExpanded((open) => !open)}
              >
                {queueExpanded ? (
                  <>
                    {t("web.admin.libraries.activity.hideQueue")}
                    <ChevronUp className="h-3 w-3" aria-hidden="true" />
                  </>
                ) : (
                  <>
                    {t("web.admin.libraries.activeWork.showAll", {
                      count: queuedScans.length,
                      count_text: numberText(queuedScans.length),
                    })}
                    <ChevronDown className="h-3 w-3" aria-hidden="true" />
                  </>
                )}
              </Button>
            ) : null}
          </div>

          {queueExpanded ? (
            <ul className="border-border/40 mt-2 max-h-56 space-y-1 overflow-y-auto border-t pt-2">
              {queuedScans.map((scan) => (
                <li
                  key={scan.id}
                  className="text-muted-foreground flex items-center gap-2 text-[11px]"
                  title={scan.path}
                >
                  <span className="bg-muted-foreground/40 h-1.5 w-1.5 shrink-0 rounded-full" />
                  <span className="shrink-0">{formatActiveScanMode(scan)}</span>
                  <code dir="ltr" className="text-foreground/80 min-w-0 flex-1 truncate font-mono">
                    <PathText path={formatActiveScanTarget(scan)} />
                  </code>
                  <span className="shrink-0 text-[10px]">
                    {t("web.admin.libraries.scanQueue.waiting")}
                  </span>
                </li>
              ))}
            </ul>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}
