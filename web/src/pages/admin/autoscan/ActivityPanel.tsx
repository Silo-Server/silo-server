import { useMemo, useRef, useState, type MouseEvent, type ReactNode } from "react";
import {
  ChevronRight,
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
  AutoscanEvent,
  AutoscanEventStatus,
  AutoscanRunningPoll,
  AutoscanScan,
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
import { useT } from "@/i18n";
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

import {
  NO_VALUE,
  SEPARATOR,
  eventDetailsSummary,
  formatDuration,
  formatTime,
  formatTimestamp,
  libraryName,
  pollEventDuration,
  pollEventTimestamp,
  pollSourceName,
  rawCountValues,
  scanSourceName,
} from "./pollEventDisplay";
import {
  DeliveryBadge,
  PollMetricStrip,
  PollStatusBadge,
  ScanResultText,
  ScanStatusBadge,
} from "./PollActivityBadges";
import { PollEventInspectorSheet } from "./PollEventInspectorSheet";

const HISTORY_PAGE_SIZE_OPTIONS = [25, 50, 100];
const DEFAULT_HISTORY_PAGE_SIZE = 25;
const QUEUE_PAGE_SIZE = 8;

type HistoryView = "scans" | "polls";

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
  selected,
  onInspect,
}: {
  event: AutoscanEvent;
  lookups: SourceLabelLookups;
  selected: boolean;
  onInspect: (event: AutoscanEvent, trigger?: HTMLElement | null) => void;
}) {
  const t = useT();
  const hasActivity = event.changes_returned > 0 || event.scan_runs.length > 0;
  return (
    <div
      className={cn(
        "border-border rounded-lg border p-4 transition-colors",
        selected && "border-foreground/25 bg-muted/40",
      )}
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1.5">
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
        <p className="text-destructive mt-3 line-clamp-2 text-xs [overflow-wrap:anywhere]">
          {event.error_message}
        </p>
      ) : null}
      <button
        type="button"
        onClick={(mouseEvent) => onInspect(event, mouseEvent.currentTarget)}
        aria-haspopup="dialog"
        className={cn(
          "border-border/70 bg-muted/25 hover:bg-surface-hover focus-visible:ring-ring mt-3 flex w-full items-center justify-between gap-2 rounded-md border px-3 py-2 text-xs transition-colors focus-visible:ring-2 focus-visible:outline-none",
          hasActivity ? "text-foreground font-medium" : "text-muted-foreground",
        )}
      >
        <span className="truncate tabular-nums">{eventDetailsSummary(t, event)}</span>
        <ChevronRight
          className="text-muted-foreground h-3.5 w-3.5 shrink-0 rtl:-scale-x-100"
          aria-hidden="true"
        />
      </button>
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
  const [inspected, setInspected] = useState<{ id: number; snapshot: AutoscanEvent } | null>(null);
  const returnFocusRef = useRef<HTMLElement | null>(null);

  // Prefer the live event from the current page and keep the fallback snapshot
  // up to date so scan statuses and results do not revert if pagination or a
  // later refresh moves the inspected poll off-page.
  const liveEvent = inspected ? events.find((event) => event.id === inspected.id) : undefined;
  if (inspected && liveEvent && liveEvent !== inspected.snapshot) {
    setInspected({ id: inspected.id, snapshot: liveEvent });
  }
  const selectedEvent = inspected ? (liveEvent ?? inspected.snapshot) : null;

  function openInspector(event: AutoscanEvent, trigger?: HTMLElement | null) {
    returnFocusRef.current =
      trigger ?? (document.activeElement instanceof HTMLElement ? document.activeElement : null);
    setInspected({ id: event.id, snapshot: event });
  }

  function handleRowClick(mouseEvent: MouseEvent<HTMLTableRowElement>, event: AutoscanEvent) {
    if (window.getSelection()?.toString()) return;
    if ((mouseEvent.target as HTMLElement).closest("button, a, input, select, textarea")) return;
    const trigger = mouseEvent.currentTarget.querySelector<HTMLElement>(
      "[data-poll-details-trigger]",
    );
    openInspector(event, trigger);
  }

  return (
    <>
      <div className="space-y-3 lg:hidden">
        {events.map((event) => (
          <PollEventCard
            key={event.id}
            event={event}
            lookups={lookups}
            selected={selectedEvent?.id === event.id}
            onInspect={openInspector}
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
        {events.map((event) => {
          const isSelected = selectedEvent?.id === event.id;
          const hasActivity = event.changes_returned > 0 || event.scan_runs.length > 0;
          return (
            <TableRow
              key={event.id}
              data-state={isSelected ? "selected" : undefined}
              onClick={(mouseEvent) => handleRowClick(mouseEvent, event)}
              className="group hover:bg-surface-hover cursor-pointer"
            >
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
                {event.error_message ? (
                  <div className="text-destructive mt-1 max-w-md truncate text-xs">
                    {event.error_message}
                  </div>
                ) : null}
              </TableCell>
              <TableCell>
                <button
                  type="button"
                  data-poll-details-trigger
                  onClick={(mouseEvent) => openInspector(event, mouseEvent.currentTarget)}
                  aria-haspopup="dialog"
                  className={cn(
                    "focus-visible:ring-ring -ms-2 inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs whitespace-nowrap tabular-nums transition-colors focus-visible:ring-2 focus-visible:outline-none",
                    hasActivity
                      ? "text-foreground font-medium"
                      : "text-muted-foreground group-hover:text-foreground",
                  )}
                >
                  <span>{eventDetailsSummary(t, event)}</span>
                  <ChevronRight
                    className="text-muted-foreground/70 group-hover:text-foreground h-3.5 w-3.5 shrink-0 transition-colors rtl:-scale-x-100"
                    aria-hidden="true"
                  />
                </button>
              </TableCell>
              <TableCell className="whitespace-nowrap tabular-nums">
                {pollEventDuration(t, event)}
              </TableCell>
              <TableCell className="text-muted-foreground whitespace-nowrap tabular-nums">
                {pollEventTimestamp(event)}
              </TableCell>
            </TableRow>
          );
        })}
      </DataTable>

      <PollEventInspectorSheet
        event={selectedEvent}
        lookups={lookups}
        librariesByID={librariesByID}
        onClose={() => setInspected(null)}
        onCloseAutoFocus={(closeEvent) => {
          const target = returnFocusRef.current;
          returnFocusRef.current = null;
          if (target?.isConnected) {
            closeEvent.preventDefault();
            target.focus();
          }
        }}
      />
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
