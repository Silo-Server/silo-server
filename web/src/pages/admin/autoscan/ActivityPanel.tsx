import { useMemo, useState } from "react";
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
  AutoscanEvent,
  AutoscanEventScanRun,
  AutoscanEventStatus,
  AutoscanScan,
  AutoscanScanStatus,
  Library,
  ScanRun,
} from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
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
import {
  useAutoscanEvents,
  useAutoscanScans,
  useAutoscanStatus,
} from "@/hooks/queries/useAutoscan";
import { useActiveScans } from "@/hooks/queries/admin/scans";
import { useAdminLibraries, useCancelLibraryScans } from "@/hooks/queries/admin/libraries";
import {
  compareActiveScans,
  formatActiveScanMode,
  formatActiveScanProgress,
  formatActiveScanTime,
  formatActiveScanTrigger,
} from "@/lib/scanRuns";
import { cn } from "@/lib/utils";

const HISTORY_PAGE_SIZE = 50;
const HISTORY_MAX_LIMIT = 200;

type HistoryView = "scans" | "polls";

function formatTime(value: string | undefined): string {
  if (!value) return "-";
  return new Date(value).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function formatTimestamp(value: string | undefined): string {
  if (!value) return "-";
  return new Date(value).toLocaleString([], {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${Math.round(ms / 100) / 10}s`;
  const minutes = Math.floor(ms / 60_000);
  const seconds = Math.round((ms % 60_000) / 1000);
  return `${minutes}m ${seconds}s`;
}

function eventStatusTone(status: AutoscanEventStatus): {
  label: string;
  icon: typeof CheckCircle2;
  className: string;
} {
  switch (status) {
    case "success":
      return {
        label: "Success",
        icon: CheckCircle2,
        className: "border-emerald-500/30 bg-emerald-500/10 text-emerald-500",
      };
    case "unresolved":
      return {
        label: "Unresolved",
        icon: AlertTriangle,
        className: "border-amber-500/30 bg-amber-500/10 text-amber-500",
      };
    case "error":
      return {
        label: "Error",
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

function scanStatusLabel(status: AutoscanScanStatus | ScanRun["status"]) {
  return status === "accepted" ? "queued" : status;
}

function pollSourceName(event: AutoscanEvent): string {
  return `${event.capability_id} #${event.installation_id}`;
}

function scanSourceName(scan: AutoscanScan): string {
  if (scan.capability_id && scan.installation_id != null) {
    return `${scan.capability_id} #${scan.installation_id}`;
  }
  return "Autoscan";
}

function libraryName(librariesByID: Map<number, Library>, libraryID: number): string {
  return librariesByID.get(libraryID)?.name ?? `Library #${libraryID}`;
}

function PollStatusBadge({ status }: { status: AutoscanEventStatus }) {
  const tone = eventStatusTone(status);
  const Icon = tone.icon;
  return (
    <Badge variant="outline" className={tone.className}>
      <Icon className="h-3.5 w-3.5" />
      {tone.label}
    </Badge>
  );
}

function ScanStatusBadge({ status }: { status: AutoscanScanStatus | ScanRun["status"] }) {
  return (
    <Badge variant="outline" className={scanStatusClass(status)}>
      {scanStatusLabel(status)}
    </Badge>
  );
}

function RunList({ runs }: { runs: AutoscanEventScanRun[] }) {
  if (runs.length === 0) {
    return <span className="text-muted-foreground text-xs">No new scan rows were created.</span>;
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
              {run.path || "Entire library"}
            </div>
          </div>
          <div className="text-muted-foreground whitespace-nowrap">
            {formatTime(run.completed_at ?? run.started_at ?? run.requested_at)}
          </div>
        </div>
      ))}
    </div>
  );
}

function PollMetricStrip({ event }: { event: AutoscanEvent }) {
  return (
    <div className="text-muted-foreground flex flex-wrap gap-x-4 gap-y-1 text-xs">
      <span>{event.changes_returned} changes</span>
      <span>{event.targets_claimed} targets</span>
      <span>{event.scans_created} created</span>
      <span>{event.scans_reused} reused</span>
      <span>{event.scans_suppressed} suppressed</span>
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
  const progress = formatActiveScanProgress(scan);
  return (
    <div className="border-border rounded-lg border p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <ScanStatusBadge status={scan.status} />
            <span className="font-medium">{libraryName(librariesByID, scan.library_id)}</span>
          </div>
          <div className="text-muted-foreground flex flex-wrap gap-x-3 gap-y-1 text-xs">
            <span>{formatActiveScanMode(scan)}</span>
            <span>{formatActiveScanTrigger(scan.trigger)}</span>
            <span>
              {scan.status === "running"
                ? formatActiveScanTime(scan.started_at, "Started")
                : "Waiting for capacity"}
            </span>
          </div>
        </div>
        <Button
          variant="ghost"
          size="icon"
          className="text-destructive h-8 w-8"
          disabled={cancellingLibraryID === scan.library_id}
          onClick={() => onCancel(scan.library_id)}
          aria-label="Cancel scans for this library"
        >
          <Square className="h-3.5 w-3.5" />
        </Button>
      </div>
      <div className="text-muted-foreground mt-3 font-mono text-xs [overflow-wrap:anywhere]">
        {scan.path || "Entire library"}
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
  if (scans.length === 0) {
    return (
      <div className="border-border rounded-lg border border-dashed p-6">
        <div className="flex items-center gap-2 text-sm font-medium">
          <ScanLine className="text-muted-foreground h-4 w-4" />
          Autoscan queue
        </div>
        <p className="text-muted-foreground mt-2 text-sm">
          {statusActiveCount > 0
            ? "Queue counts are active, but live scan details have not arrived yet."
            : "No autoscan scans are queued or running."}
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
            Autoscan queue
          </div>
          <p className="text-muted-foreground mt-1 text-xs">
            Live scans created by autoscan sources.
          </p>
        </div>
        <Badge variant="secondary" className="tabular-nums">
          {scans.length} active
        </Badge>
      </div>

      <div className="space-y-3 lg:hidden">
        {scans.map((scan) => (
          <QueueCard
            key={scan.id}
            scan={scan}
            librariesByID={librariesByID}
            cancellingLibraryID={cancellingLibraryID}
            onCancel={onCancel}
          />
        ))}
      </div>

      <div className="hidden rounded-lg border lg:block">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Status</TableHead>
              <TableHead>Library</TableHead>
              <TableHead>Scope</TableHead>
              <TableHead>Progress</TableHead>
              <TableHead className="w-20">Action</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {scans.map((scan) => {
              const progress = formatActiveScanProgress(scan);
              return (
                <TableRow key={scan.id}>
                  <TableCell>
                    <ScanStatusBadge status={scan.status} />
                  </TableCell>
                  <TableCell className="font-medium">
                    {libraryName(librariesByID, scan.library_id)}
                    <div className="text-muted-foreground mt-1 text-xs">
                      {scan.status === "running"
                        ? formatActiveScanTime(scan.started_at, "Started")
                        : "Waiting for capacity"}
                    </div>
                  </TableCell>
                  <TableCell className="max-w-xl">
                    <div className="text-sm">{formatActiveScanMode(scan)}</div>
                    <div className="text-muted-foreground mt-1 font-mono text-xs [overflow-wrap:anywhere]">
                      {scan.path || "Entire library"}
                    </div>
                  </TableCell>
                  <TableCell className="text-muted-foreground text-xs [overflow-wrap:anywhere]">
                    {progress || formatActiveScanTrigger(scan.trigger)}
                  </TableCell>
                  <TableCell>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="text-destructive h-8 w-8"
                      disabled={cancellingLibraryID === scan.library_id}
                      onClick={() => onCancel(scan.library_id)}
                      aria-label="Cancel scans for this library"
                    >
                      <Square className="h-3.5 w-3.5" />
                    </Button>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </div>
    </section>
  );
}

function ScanHistoryCard({
  scan,
  librariesByID,
}: {
  scan: AutoscanScan;
  librariesByID: Map<number, Library>;
}) {
  return (
    <div className="border-border rounded-lg border p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <ScanStatusBadge status={scan.status} />
            <span className="font-medium">{libraryName(librariesByID, scan.library_id)}</span>
          </div>
          <div className="text-muted-foreground flex flex-wrap gap-x-3 gap-y-1 text-xs">
            <span>{formatActiveScanMode(scan)}</span>
            <span>{scanSourceName(scan)}</span>
            <span>
              {formatTimestamp(scan.completed_at ?? scan.started_at ?? scan.requested_at)}
            </span>
          </div>
        </div>
        {scan.event_status ? <PollStatusBadge status={scan.event_status} /> : null}
      </div>
      <div className="text-muted-foreground mt-3 font-mono text-xs [overflow-wrap:anywhere]">
        {scan.path || "Entire library"}
      </div>
      {scan.error_message ? (
        <div className="text-destructive mt-2 text-xs [overflow-wrap:anywhere]">
          {scan.error_message}
        </div>
      ) : null}
      <div className="text-muted-foreground mt-3 text-[11px] [overflow-wrap:anywhere]">
        {scan.id}
      </div>
    </div>
  );
}

function ScanHistoryTable({
  scans,
  librariesByID,
}: {
  scans: AutoscanScan[];
  librariesByID: Map<number, Library>;
}) {
  return (
    <>
      <div className="space-y-3 lg:hidden">
        {scans.map((scan) => (
          <ScanHistoryCard key={scan.id} scan={scan} librariesByID={librariesByID} />
        ))}
      </div>
      <div className="hidden rounded-lg border lg:block">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Status</TableHead>
              <TableHead>Library</TableHead>
              <TableHead>Source</TableHead>
              <TableHead>Scope</TableHead>
              <TableHead>Time</TableHead>
              <TableHead>Poll</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {scans.map((scan) => (
              <TableRow key={scan.id}>
                <TableCell>
                  <ScanStatusBadge status={scan.status} />
                </TableCell>
                <TableCell className="font-medium">
                  {libraryName(librariesByID, scan.library_id)}
                  <div className="text-muted-foreground mt-1 text-[11px]">{scan.id}</div>
                </TableCell>
                <TableCell className="whitespace-nowrap">{scanSourceName(scan)}</TableCell>
                <TableCell className="max-w-xl">
                  <div className="text-sm">{formatActiveScanMode(scan)}</div>
                  <div className="text-muted-foreground mt-1 font-mono text-xs [overflow-wrap:anywhere]">
                    {scan.path || "Entire library"}
                  </div>
                  {scan.error_message ? (
                    <div className="text-destructive mt-1 text-xs [overflow-wrap:anywhere]">
                      {scan.error_message}
                    </div>
                  ) : null}
                </TableCell>
                <TableCell className="whitespace-nowrap">
                  {formatTimestamp(scan.completed_at ?? scan.started_at ?? scan.requested_at)}
                </TableCell>
                <TableCell>
                  {scan.event_status ? <PollStatusBadge status={scan.event_status} /> : "-"}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </>
  );
}

function PollEventCard({ event }: { event: AutoscanEvent }) {
  return (
    <div className="border-border rounded-lg border p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <PollStatusBadge status={event.status} />
            <span className="font-medium">{pollSourceName(event)}</span>
          </div>
          <PollMetricStrip event={event} />
        </div>
        <div className="text-muted-foreground text-right text-xs">
          <div>{formatTimestamp(event.completed_at)}</div>
          <div>{formatDuration(event.duration_ms)}</div>
        </div>
      </div>
      {event.error_message ? (
        <p className="text-destructive mt-3 text-xs [overflow-wrap:anywhere]">
          {event.error_message}
        </p>
      ) : null}
      <details className="mt-3">
        <summary className="text-muted-foreground cursor-pointer text-xs">
          {event.scan_runs.length} linked scan {event.scan_runs.length === 1 ? "run" : "runs"}
        </summary>
        <div className="mt-2">
          <RunList runs={event.scan_runs} />
        </div>
      </details>
    </div>
  );
}

function PollEventTable({ events }: { events: AutoscanEvent[] }) {
  return (
    <>
      <div className="space-y-3 lg:hidden">
        {events.map((event) => (
          <PollEventCard key={event.id} event={event} />
        ))}
      </div>
      <div className="hidden rounded-lg border lg:block">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Status</TableHead>
              <TableHead>Source</TableHead>
              <TableHead>Counts</TableHead>
              <TableHead>Scans</TableHead>
              <TableHead>Duration</TableHead>
              <TableHead>Completed</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {events.map((event) => (
              <TableRow key={event.id}>
                <TableCell>
                  <PollStatusBadge status={event.status} />
                </TableCell>
                <TableCell className="font-medium">{pollSourceName(event)}</TableCell>
                <TableCell>
                  <PollMetricStrip event={event} />
                  {event.error_message ? (
                    <div className="text-destructive mt-1 max-w-md text-xs [overflow-wrap:anywhere]">
                      {event.error_message}
                    </div>
                  ) : null}
                </TableCell>
                <TableCell>
                  <details>
                    <summary className="text-muted-foreground cursor-pointer text-xs">
                      {event.scan_runs.length} linked
                    </summary>
                    <div className="mt-2 w-[min(42rem,70vw)]">
                      <RunList runs={event.scan_runs} />
                    </div>
                  </details>
                </TableCell>
                <TableCell className="whitespace-nowrap">
                  {formatDuration(event.duration_ms)}
                </TableCell>
                <TableCell className="whitespace-nowrap">
                  {formatTimestamp(event.completed_at)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </>
  );
}

export default function ActivityPanel() {
  useEventChannel("scans");
  const [historyView, setHistoryView] = useState<HistoryView>("scans");
  const [historyQuery, setHistoryQuery] = useState("");
  const [scanStatus, setScanStatus] = useState<AutoscanScanStatus | "all">("all");
  const [pollStatus, setPollStatus] = useState<AutoscanEventStatus | "all">("all");
  const [historyLimit, setHistoryLimit] = useState(HISTORY_PAGE_SIZE);
  const debouncedHistoryQuery = useDebounce(historyQuery.trim(), 300);

  const scanHistory = useAutoscanScans({
    limit: historyLimit,
    query: debouncedHistoryQuery || undefined,
    status: scanStatus === "all" ? undefined : scanStatus,
    enabled: historyView === "scans",
  });
  const pollEvents = useAutoscanEvents({
    limit: historyLimit,
    query: debouncedHistoryQuery || undefined,
    status: pollStatus === "all" ? undefined : pollStatus,
    enabled: historyView === "polls",
  });
  const status = useAutoscanStatus();
  const { data: activeScans = [] } = useActiveScans();
  const { data: libraries = [] } = useAdminLibraries();
  const cancelScans = useCancelLibraryScans();

  const queue = status.data;
  const librariesByID = useMemo(
    () => new Map(libraries.map((library) => [library.id, library])),
    [libraries],
  );
  const autoscanQueue = useMemo(
    () =>
      activeScans
        .filter((scan) => scan.trigger === "autoscan")
        .slice()
        .sort(compareActiveScans),
    [activeScans],
  );

  const activeRows = historyView === "scans" ? (scanHistory.data ?? []) : (pollEvents.data ?? []);
  const activeStatus = historyView === "scans" ? scanStatus : pollStatus;
  const activeQuery = historyView === "scans" ? scanHistory : pollEvents;
  const hasHistoryFilters = debouncedHistoryQuery !== "" || activeStatus !== "all";
  const canLoadMore = activeRows.length >= historyLimit && historyLimit < HISTORY_MAX_LIMIT;
  const isRefreshing = status.isFetching || activeQuery.isFetching;

  function refresh() {
    void status.refetch();
    void activeQuery.refetch();
  }

  function resetHistoryFilters() {
    setHistoryQuery("");
    setScanStatus("all");
    setPollStatus("all");
    setHistoryLimit(HISTORY_PAGE_SIZE);
  }

  function switchHistoryView(nextView: HistoryView) {
    setHistoryView(nextView);
    setHistoryLimit(HISTORY_PAGE_SIZE);
  }

  return (
    <div className="space-y-6">
      <div className="border-border grid gap-3 rounded-lg border p-4 sm:grid-cols-4">
        <div>
          <div className="text-muted-foreground flex items-center gap-2 text-xs">
            <ScanLine className="h-3.5 w-3.5" />
            Active
          </div>
          <div className="mt-1 text-2xl font-semibold">{queue?.active_scans ?? 0}</div>
        </div>
        <div>
          <div className="text-muted-foreground text-xs">Queued</div>
          <div className="mt-1 text-2xl font-semibold">{queue?.accepted_scans ?? 0}</div>
        </div>
        <div>
          <div className="text-muted-foreground text-xs">Running</div>
          <div className="mt-1 text-2xl font-semibold">{queue?.running_scans ?? 0}</div>
        </div>
        <div className="flex items-start justify-between gap-3 sm:block">
          <div>
            <div className="text-muted-foreground flex items-center gap-2 text-xs">
              <Clock className="h-3.5 w-3.5" />
              Latest poll
            </div>
            <div className="mt-1 text-sm font-medium">{formatTime(queue?.latest_event_at)}</div>
          </div>
          <Button
            variant="outline"
            size="icon"
            className="sm:mt-2"
            onClick={refresh}
            disabled={isRefreshing}
            aria-label="Refresh autoscan activity"
          >
            <RefreshCw className={cn(isRefreshing && "animate-spin")} />
          </Button>
        </div>
      </div>

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
              {historyView === "scans" ? "Scan history" : "Poll log"}
            </div>
            <p className="text-muted-foreground mt-1 text-xs">
              {historyView === "scans"
                ? "Real scan rows created by autoscan, searchable by path, library, source, status, or scan id."
                : "Diagnostic poll records from scan-source plugins."}
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
                Scans
              </Button>
              <Button
                type="button"
                variant={historyView === "polls" ? "secondary" : "ghost"}
                size="sm"
                className="h-7"
                onClick={() => switchHistoryView("polls")}
              >
                Polls
              </Button>
            </div>
            <div className="relative min-w-0">
              <Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
              <Input
                value={historyQuery}
                onChange={(event) => {
                  setHistoryQuery(event.target.value);
                  setHistoryLimit(HISTORY_PAGE_SIZE);
                }}
                placeholder={historyView === "scans" ? "Search scan history" : "Search poll log"}
                className="pr-9 pl-9"
              />
              {historyQuery ? (
                <button
                  type="button"
                  className="text-muted-foreground hover:text-foreground absolute top-1/2 right-2 rounded p-1 transition-colors"
                  onClick={() => setHistoryQuery("")}
                  aria-label="Clear history search"
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
                  setHistoryLimit(HISTORY_PAGE_SIZE);
                }}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">All statuses</SelectItem>
                  <SelectItem value="accepted">Queued</SelectItem>
                  <SelectItem value="running">Running</SelectItem>
                  <SelectItem value="completed">Completed</SelectItem>
                  <SelectItem value="failed">Failed</SelectItem>
                  <SelectItem value="cancelled">Cancelled</SelectItem>
                </SelectContent>
              </Select>
            ) : (
              <Select
                value={pollStatus}
                onValueChange={(value) => {
                  setPollStatus(value as AutoscanEventStatus | "all");
                  setHistoryLimit(HISTORY_PAGE_SIZE);
                }}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">All statuses</SelectItem>
                  <SelectItem value="success">Success</SelectItem>
                  <SelectItem value="unresolved">Unresolved</SelectItem>
                  <SelectItem value="error">Error</SelectItem>
                </SelectContent>
              </Select>
            )}
            <Button variant="outline" onClick={resetHistoryFilters} disabled={!hasHistoryFilters}>
              Reset
            </Button>
          </div>
        </div>

        {activeQuery.isLoading ? (
          <p className="text-muted-foreground py-4 text-sm">Loading activity...</p>
        ) : activeQuery.isError ? (
          <p className="text-destructive py-4 text-sm">Failed to load autoscan activity.</p>
        ) : activeRows.length === 0 ? (
          <div className="rounded-lg border border-dashed p-8 text-center">
            <p className="text-muted-foreground text-sm">
              {hasHistoryFilters
                ? "No autoscan activity matches those filters."
                : historyView === "scans"
                  ? "No autoscan scans have been created yet."
                  : "No autoscan polls have been recorded yet."}
            </p>
          </div>
        ) : (
          <>
            {historyView === "scans" ? (
              <ScanHistoryTable scans={scanHistory.data ?? []} librariesByID={librariesByID} />
            ) : (
              <PollEventTable events={pollEvents.data ?? []} />
            )}

            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="text-muted-foreground text-xs">
                Showing {activeRows.length} {historyView === "scans" ? "scan" : "poll"}
                {activeRows.length === 1 ? "" : "s"}
                {hasHistoryFilters ? " matching filters" : ""}
              </div>
              {canLoadMore ? (
                <Button
                  variant="outline"
                  onClick={() =>
                    setHistoryLimit((current) =>
                      Math.min(HISTORY_MAX_LIMIT, current + HISTORY_PAGE_SIZE),
                    )
                  }
                >
                  Load more
                </Button>
              ) : null}
            </div>
          </>
        )}
      </section>
    </div>
  );
}
