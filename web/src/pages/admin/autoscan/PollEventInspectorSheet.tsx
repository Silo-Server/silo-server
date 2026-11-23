import { useMemo, useState } from "react";
import { AlertTriangle, ChevronRight, Search, X } from "lucide-react";

import type {
  AutoscanEvent,
  AutoscanEventScanRun,
  AutoscanScanStatus,
  Library,
  ScanRun,
} from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { TablePagination } from "@/components/ui/pagination";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { numberText, useT } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";
import type { SourceLabelLookups } from "@/lib/autoscanLabels";
import { formatActiveScanMode } from "@/lib/scanRuns";
import { cn } from "@/lib/utils";

import {
  ChangeExplanation,
  ChangeOutcomeBadge,
  DeliveryBadge,
  PathDisplay,
  PollMetricStrip,
  PollStatusBadge,
  ScanResultText,
  ScanStatusBadge,
} from "./PollActivityBadges";
import {
  CHANGE_OUTCOME_ORDER,
  CHANGE_OUTCOME_TONES,
  SCAN_STATUS_FILTER_LABELS,
  SCAN_STATUS_ORDER,
  SEPARATOR,
  changeReasonText,
  countValues,
  formatTimestamp,
  isKnownChangeOutcome,
  joinedRunCount,
  libraryName,
  pollEventDuration,
  pollEventTimestamp,
  pollSourceName,
} from "./pollEventDisplay";

const PATHS_PAGE_SIZE_OPTIONS = [15, 25, 50];
const DEFAULT_PATHS_PAGE_SIZE = 15;
const SCANS_PAGE_SIZE_OPTIONS = [20, 50, 100];
const DEFAULT_SCANS_PAGE_SIZE = 20;

function FilterChip({
  active,
  onClick,
  label,
  count,
}: {
  active: boolean;
  onClick: () => void;
  label: string;
  count: number;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        "focus-visible:ring-ring inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs font-medium transition-colors focus-visible:ring-2 focus-visible:outline-none",
        active
          ? "border-foreground/25 bg-accent text-foreground"
          : "border-border/70 bg-background/60 text-muted-foreground hover:bg-surface-hover hover:text-foreground",
      )}
    >
      <span>{label}</span>
      <span
        className={cn(
          "rounded px-1 text-[0.6875rem] tabular-nums",
          active ? "bg-background/70 text-foreground" : "bg-muted text-muted-foreground",
        )}
      >
        {numberText(count)}
      </span>
    </button>
  );
}

function FilterSearchInput({
  value,
  onChange,
}: {
  value: string;
  onChange: (next: string) => void;
}) {
  const t = useT();
  return (
    <div className="relative min-w-0 flex-1">
      <Search
        className="text-muted-foreground pointer-events-none absolute start-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2"
        aria-hidden="true"
      />
      <Input
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder={t("web.ui.select.searchPlaceholder")}
        className="h-8 ps-8 pe-8 text-xs"
      />
      {value ? (
        <button
          type="button"
          className="text-muted-foreground hover:text-foreground absolute end-1.5 top-1/2 -translate-y-1/2 rounded p-1 transition-colors"
          onClick={() => onChange("")}
          aria-label={t("web.admin.autoscan.activity.history.clearSearch")}
        >
          <X className="h-3.5 w-3.5" />
        </button>
      ) : null}
    </div>
  );
}

type InspectorTab = "paths" | "scans";

function PollEventInspectorBody({
  event,
  lookups,
  librariesByID,
}: {
  event: AutoscanEvent;
  lookups: SourceLabelLookups;
  librariesByID: Map<number, Library>;
}) {
  const t = useT();
  const [activeTab, setActiveTab] = useState<InspectorTab>(() =>
    event.changes.length === 0 && event.scan_runs.length > 0 ? "scans" : "paths",
  );
  const [pathsSearch, setPathsSearch] = useState("");
  const [outcomeFilter, setOutcomeFilter] = useState<string>("all");
  const [pathsPage, setPathsPage] = useState(0);
  const [pathsPageSize, setPathsPageSize] = useState(DEFAULT_PATHS_PAGE_SIZE);

  const [scansSearch, setScansSearch] = useState("");
  const [scanStatusFilter, setScanStatusFilter] = useState<AutoscanScanStatus | "all">("all");
  const [scansPage, setScansPage] = useState(0);
  const [scansPageSize, setScansPageSize] = useState(DEFAULT_SCANS_PAGE_SIZE);

  const scanRunIDs = useMemo(
    () => new Set(event.scan_runs.map((run) => run.id)),
    [event.scan_runs],
  );
  const joinedRuns = useMemo(() => joinedRunCount(event), [event]);

  const outcomeCounts = useMemo(() => {
    const counts = new Map<string, number>();
    for (const change of event.changes) {
      counts.set(change.outcome, (counts.get(change.outcome) ?? 0) + 1);
    }
    const ordered: Array<{ outcome: string; count: number }> = [];
    for (const known of CHANGE_OUTCOME_ORDER) {
      const count = counts.get(known);
      if (count) {
        ordered.push({ outcome: known, count });
        counts.delete(known);
      }
    }
    for (const [outcome, count] of counts) {
      ordered.push({ outcome, count });
    }
    return ordered;
  }, [event.changes]);

  const scanStatusCounts = useMemo(() => {
    const counts = new Map<AutoscanScanStatus, number>();
    for (const run of event.scan_runs) {
      counts.set(run.status, (counts.get(run.status) ?? 0) + 1);
    }
    const ordered: Array<{ status: AutoscanScanStatus; count: number }> = [];
    for (const status of SCAN_STATUS_ORDER) {
      const count = counts.get(status);
      if (count) ordered.push({ status, count });
    }
    return ordered;
  }, [event.scan_runs]);

  const filteredChanges = useMemo(() => {
    const needle = pathsSearch.trim().toLowerCase();
    return event.changes.filter((change) => {
      if (outcomeFilter !== "all" && change.outcome !== outcomeFilter) return false;
      if (!needle) return true;
      const lib = change.library_id ? libraryName(t, librariesByID, change.library_id) : "";
      const reason = changeReasonText(t, change.reason) ?? "";
      return (
        change.source_path.toLowerCase().includes(needle) ||
        (change.rewritten_path?.toLowerCase().includes(needle) ?? false) ||
        (change.target_path?.toLowerCase().includes(needle) ?? false) ||
        (change.scan_run_id?.toLowerCase().includes(needle) ?? false) ||
        (change.detail?.toLowerCase().includes(needle) ?? false) ||
        (change.reason?.toLowerCase().includes(needle) ?? false) ||
        reason.toLowerCase().includes(needle) ||
        lib.toLowerCase().includes(needle)
      );
    });
  }, [event.changes, librariesByID, outcomeFilter, pathsSearch, t]);

  const filteredRuns = useMemo(() => {
    const needle = scansSearch.trim().toLowerCase();
    return event.scan_runs.filter((run) => {
      if (scanStatusFilter !== "all" && run.status !== scanStatusFilter) return false;
      if (!needle) return true;
      const lib = libraryName(t, librariesByID, run.library_id);
      const mode = formatActiveScanMode({ mode: run.mode as ScanRun["mode"] });
      return (
        run.id.toLowerCase().includes(needle) ||
        (run.path?.toLowerCase().includes(needle) ?? false) ||
        run.mode.toLowerCase().includes(needle) ||
        mode.toLowerCase().includes(needle) ||
        lib.toLowerCase().includes(needle) ||
        (run.error_message?.toLowerCase().includes(needle) ?? false)
      );
    });
  }, [event.scan_runs, librariesByID, scanStatusFilter, scansSearch, t]);

  const pathsPageCount = Math.max(1, Math.ceil(filteredChanges.length / pathsPageSize));
  const safePathsPage = Math.min(pathsPage, pathsPageCount - 1);
  const visibleChanges = filteredChanges.slice(
    safePathsPage * pathsPageSize,
    (safePathsPage + 1) * pathsPageSize,
  );

  const scansPageCount = Math.max(1, Math.ceil(filteredRuns.length / scansPageSize));
  const safeScansPage = Math.min(scansPage, scansPageCount - 1);
  const visibleRuns = filteredRuns.slice(
    safeScansPage * scansPageSize,
    (safeScansPage + 1) * scansPageSize,
  );

  function jumpToScanRun(runID: string) {
    setScanStatusFilter("all");
    setScansSearch(runID);
    setScansPage(0);
    setActiveTab("scans");
  }

  function resetPathFilters() {
    setPathsSearch("");
    setOutcomeFilter("all");
    setPathsPage(0);
  }

  function resetScanFilters() {
    setScansSearch("");
    setScanStatusFilter("all");
    setScansPage(0);
  }

  const headerMeta = [pollEventTimestamp(event), pollEventDuration(t, event)].join(SEPARATOR);

  return (
    <>
      <SheetHeader className="shrink-0 gap-2 border-b ps-6 pe-12 pt-5 pb-4">
        <div className="flex flex-wrap items-center gap-2">
          <PollStatusBadge status={event.status} />
          <SheetTitle className="min-w-0 truncate text-base font-semibold">
            {pollSourceName(t, event, lookups)}
          </SheetTitle>
          <DeliveryBadge event={event} />
        </div>
        <SheetDescription className="text-xs tabular-nums">{headerMeta}</SheetDescription>
        {event.error_message ? (
          <div
            tabIndex={0}
            className="border-destructive/30 bg-destructive/10 text-destructive focus-visible:ring-ring mt-1 max-h-32 overflow-y-auto overscroll-contain rounded-md border px-3 py-2 text-xs [overflow-wrap:anywhere] focus-visible:ring-2 focus-visible:outline-none"
          >
            {event.error_message}
          </div>
        ) : null}
        <div className="pt-1">
          <PollMetricStrip event={event} variant="pills" />
        </div>
      </SheetHeader>

      <Tabs
        value={activeTab}
        onValueChange={(value) => setActiveTab(value as InspectorTab)}
        className="flex min-h-0 flex-1 flex-col gap-0"
      >
        <div className="shrink-0 border-b px-6 pt-1.5">
          <TabsList variant="line" className="h-9 gap-4 p-0">
            <TabsTrigger value="paths" className="px-1 pb-2 text-xs font-medium">
              <span>{t("web.admin.autoscan.activity.details.paths")}</span>
              <span className="bg-muted text-muted-foreground rounded px-1.5 py-0.5 text-[0.6875rem] tabular-nums">
                {numberText(event.changes_returned)}
              </span>
            </TabsTrigger>
            <TabsTrigger value="scans" className="px-1 pb-2 text-xs font-medium">
              <span>{t("web.admin.autoscan.activity.details.scansCreated")}</span>
              <span className="bg-muted text-muted-foreground rounded px-1.5 py-0.5 text-[0.6875rem] tabular-nums">
                {numberText(event.scan_runs.length)}
              </span>
            </TabsTrigger>
          </TabsList>
        </div>

        <TabsContent
          value="paths"
          className="mt-0 flex min-h-0 flex-1 flex-col overflow-hidden data-[state=inactive]:hidden"
        >
          {event.changes_truncated ? (
            <div className="text-muted-foreground flex shrink-0 items-center gap-2 border-b bg-amber-500/5 px-6 py-2 text-xs">
              <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-amber-500" aria-hidden="true" />
              <span>
                {t("web.admin.autoscan.activity.changes.truncated", {
                  ...countValues(event.changes_returned),
                  shown: event.changes.length.toLocaleString(formatLocale()),
                })}
              </span>
            </div>
          ) : null}

          {event.changes.length > 0 ? (
            <div className="bg-muted/20 shrink-0 space-y-2.5 border-b px-6 py-3">
              <div className="flex items-center gap-2">
                <FilterSearchInput
                  value={pathsSearch}
                  onChange={(next) => {
                    setPathsSearch(next);
                    setPathsPage(0);
                  }}
                />
                {pathsSearch || outcomeFilter !== "all" ? (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="h-8 text-xs"
                    onClick={resetPathFilters}
                  >
                    {t("web.admin.autoscan.activity.history.reset")}
                  </Button>
                ) : null}
              </div>
              {outcomeCounts.length > 1 ? (
                <div className="flex flex-wrap gap-1.5">
                  <FilterChip
                    active={outcomeFilter === "all"}
                    onClick={() => {
                      setOutcomeFilter("all");
                      setPathsPage(0);
                    }}
                    label={t("web.admin.autoscan.activity.filter.allStatuses")}
                    count={event.changes.length}
                  />
                  {outcomeCounts.map(({ outcome, count }) => (
                    <FilterChip
                      key={outcome}
                      active={outcomeFilter === outcome}
                      onClick={() => {
                        setOutcomeFilter(outcome);
                        setPathsPage(0);
                      }}
                      label={
                        isKnownChangeOutcome(outcome)
                          ? t(CHANGE_OUTCOME_TONES[outcome].label)
                          : outcome
                      }
                      count={count}
                    />
                  ))}
                </div>
              ) : null}
            </div>
          ) : null}

          <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-6 py-4">
            {event.changes.length === 0 ? (
              <div className="text-muted-foreground rounded-lg border border-dashed p-8 text-center text-xs">
                {event.changes_returned > 0
                  ? t("web.admin.autoscan.activity.changes.notRecorded")
                  : t("web.admin.autoscan.activity.changes.none")}
              </div>
            ) : filteredChanges.length === 0 ? (
              <div className="rounded-lg border border-dashed p-8 text-center">
                <p className="text-muted-foreground text-xs">{t("web.ui.select.noMatches")}</p>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="mt-3"
                  onClick={resetPathFilters}
                >
                  {t("web.admin.autoscan.activity.history.reset")}
                </Button>
              </div>
            ) : (
              <div className="divide-border/70 bg-card/30 divide-y overflow-hidden rounded-lg border">
                {visibleChanges.map((change, index) => {
                  const absoluteIndex = safePathsPage * pathsPageSize + index;
                  const canJumpToRun = Boolean(
                    change.scan_run_id && scanRunIDs.has(change.scan_run_id),
                  );
                  return (
                    <div
                      key={`${absoluteIndex}:${change.source_path}`}
                      className="hover:bg-surface-hover/50 space-y-1.5 px-4 py-3 transition-colors"
                    >
                      <div className="flex flex-wrap items-center justify-between gap-2">
                        <ChangeOutcomeBadge outcome={change.outcome} />
                        {change.scan_run_id ? (
                          canJumpToRun ? (
                            <button
                              type="button"
                              onClick={() => jumpToScanRun(change.scan_run_id!)}
                              className="border-border/80 bg-muted/40 text-muted-foreground hover:border-foreground/30 hover:text-foreground focus-visible:ring-ring inline-flex items-center gap-1 rounded border px-1.5 py-0.5 font-mono text-[0.6875rem] transition-colors focus-visible:ring-2 focus-visible:outline-none"
                            >
                              <span>{change.scan_run_id}</span>
                              <ChevronRight
                                className="h-3 w-3 rtl:-scale-x-100"
                                aria-hidden="true"
                              />
                            </button>
                          ) : (
                            <span className="text-muted-foreground font-mono text-[0.6875rem]">
                              {change.scan_run_id}
                            </span>
                          )
                        ) : null}
                      </div>

                      <PathDisplay path={change.source_path} />

                      {change.rewritten_path && change.rewritten_path !== change.source_path ? (
                        <div className="text-muted-foreground font-mono text-xs [overflow-wrap:anywhere]">
                          {t("web.admin.autoscan.activity.change.rewrittenTo", {
                            path: change.rewritten_path,
                            label: (chunks) => <span className="font-sans">{chunks}</span>,
                          })}
                        </div>
                      ) : null}

                      <ChangeExplanation change={change} librariesByID={librariesByID} />
                    </div>
                  );
                })}
              </div>
            )}
          </div>

          {filteredChanges.length > 0 ? (
            <div className="bg-background shrink-0 border-t px-6 py-3">
              <TablePagination
                page={safePathsPage}
                pageSize={pathsPageSize}
                total={filteredChanges.length}
                onPageChange={setPathsPage}
                onPageSizeChange={(size) => {
                  setPathsPageSize(size);
                  setPathsPage(0);
                }}
                pageSizeOptions={PATHS_PAGE_SIZE_OPTIONS}
              />
            </div>
          ) : null}
        </TabsContent>

        <TabsContent
          value="scans"
          className="mt-0 flex min-h-0 flex-1 flex-col overflow-hidden data-[state=inactive]:hidden"
        >
          {event.scan_runs.length > 0 ? (
            <div className="bg-muted/20 shrink-0 space-y-2.5 border-b px-6 py-3">
              <div className="flex items-center gap-2">
                <FilterSearchInput
                  value={scansSearch}
                  onChange={(next) => {
                    setScansSearch(next);
                    setScansPage(0);
                  }}
                />
                {scansSearch || scanStatusFilter !== "all" ? (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="h-8 text-xs"
                    onClick={resetScanFilters}
                  >
                    {t("web.admin.autoscan.activity.history.reset")}
                  </Button>
                ) : null}
              </div>
              {scanStatusCounts.length > 1 ? (
                <div className="flex flex-wrap gap-1.5">
                  <FilterChip
                    active={scanStatusFilter === "all"}
                    onClick={() => {
                      setScanStatusFilter("all");
                      setScansPage(0);
                    }}
                    label={t("web.admin.autoscan.activity.filter.allStatuses")}
                    count={event.scan_runs.length}
                  />
                  {scanStatusCounts.map(({ status, count }) => (
                    <FilterChip
                      key={status}
                      active={scanStatusFilter === status}
                      onClick={() => {
                        setScanStatusFilter(status);
                        setScansPage(0);
                      }}
                      label={t(SCAN_STATUS_FILTER_LABELS[status])}
                      count={count}
                    />
                  ))}
                </div>
              ) : null}
            </div>
          ) : null}

          <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-6 py-4">
            {event.scan_runs.length === 0 ? (
              <div className="text-muted-foreground rounded-lg border border-dashed p-8 text-center text-xs">
                {joinedRuns > 0
                  ? t("web.admin.autoscan.activity.runs.noneJoined")
                  : t("web.admin.autoscan.activity.runs.none")}
              </div>
            ) : filteredRuns.length === 0 ? (
              <div className="rounded-lg border border-dashed p-8 text-center">
                <p className="text-muted-foreground text-xs">{t("web.ui.select.noMatches")}</p>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="mt-3"
                  onClick={resetScanFilters}
                >
                  {t("web.admin.autoscan.activity.history.reset")}
                </Button>
              </div>
            ) : (
              <div className="divide-border/70 bg-card/30 divide-y overflow-hidden rounded-lg border">
                {visibleRuns.map((run: AutoscanEventScanRun) => (
                  <div
                    key={run.id}
                    className="hover:bg-surface-hover/50 space-y-1.5 px-4 py-3 transition-colors"
                  >
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <div className="flex min-w-0 flex-wrap items-center gap-2">
                        <ScanStatusBadge status={run.status} />
                        <span className="text-xs font-medium">
                          {libraryName(t, librariesByID, run.library_id)}
                        </span>
                        <span className="text-muted-foreground text-xs">
                          {formatActiveScanMode({ mode: run.mode as ScanRun["mode"] })}
                        </span>
                      </div>
                      <span className="text-muted-foreground text-xs whitespace-nowrap tabular-nums">
                        {formatTimestamp(run.completed_at ?? run.started_at ?? run.requested_at)}
                      </span>
                    </div>

                    {run.path ? (
                      <PathDisplay path={run.path} />
                    ) : (
                      <div className="text-muted-foreground text-xs">
                        {t("web.admin.autoscan.activity.entireLibrary")}
                      </div>
                    )}

                    <div className="flex flex-wrap items-center justify-between gap-2 pt-0.5">
                      <div className="min-w-0 flex-1 space-y-1">
                        <ScanResultText result={run.result} className="block" />
                        {run.error_message ? (
                          <div className="text-destructive text-xs [overflow-wrap:anywhere]">
                            {run.error_message}
                          </div>
                        ) : null}
                      </div>
                      <span className="text-muted-foreground font-mono text-[0.6875rem]">
                        {run.id}
                      </span>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>

          {filteredRuns.length > 0 ? (
            <div className="bg-background shrink-0 border-t px-6 py-3">
              <TablePagination
                page={safeScansPage}
                pageSize={scansPageSize}
                total={filteredRuns.length}
                onPageChange={setScansPage}
                onPageSizeChange={(size) => {
                  setScansPageSize(size);
                  setScansPage(0);
                }}
                pageSizeOptions={SCANS_PAGE_SIZE_OPTIONS}
                summaryKey="web.admin.common.pagination.scans"
              />
            </div>
          ) : null}
        </TabsContent>
      </Tabs>
    </>
  );
}

export function PollEventInspectorSheet({
  event,
  lookups,
  librariesByID,
  onClose,
  onCloseAutoFocus,
}: {
  event: AutoscanEvent | null;
  lookups: SourceLabelLookups;
  librariesByID: Map<number, Library>;
  onClose: () => void;
  onCloseAutoFocus?: (event: Event) => void;
}) {
  return (
    <Sheet
      open={event !== null}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <SheetContent
        side="right"
        onCloseAutoFocus={onCloseAutoFocus}
        className="flex h-full w-full flex-col gap-0 overflow-hidden overscroll-contain p-0 sm:max-w-2xl lg:max-w-4xl"
      >
        {event ? (
          <PollEventInspectorBody
            key={event.id}
            event={event}
            lookups={lookups}
            librariesByID={librariesByID}
          />
        ) : null}
      </SheetContent>
    </Sheet>
  );
}
