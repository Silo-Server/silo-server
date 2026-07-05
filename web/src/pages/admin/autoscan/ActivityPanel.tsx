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
  AutoscanEvent,
  AutoscanEventScanRun,
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
import {
  buildPluginDisplayNames,
  resolveEventSourceName,
  type SourceLabelLookups,
} from "@/lib/autoscanLabels";
import {
  compareActiveScans,
  formatActiveScanMode,
  formatActiveScanProgress,
  formatActiveScanTime,
  formatActiveScanTrigger,
} from "@/lib/scanRuns";
import { cn } from "@/lib/utils";
import { formatTime as formatTimePreferred, preferredDateLocale } from "@/lib/datetime";

const HISTORY_PAGE_SIZE_OPTIONS = [25, 50, 100];
const DEFAULT_HISTORY_PAGE_SIZE = 25;
const QUEUE_PAGE_SIZE = 8;

type HistoryView = "scans" | "polls";

function formatTime(value: string | undefined): string {
  if (!value) return "-";
  return formatTimePreferred(value, { hour: "2-digit" });
}

function formatTimestamp(value: string | undefined): string {
  if (!value) return "-";
  const day = new Date(value).toLocaleDateString(preferredDateLocale(), {
    month: "short",
    day: "numeric",
  });
  return `${day}, ${formatTimePreferred(value, { hour: "2-digit" })}`;
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${Math.round(ms / 100) / 10}s`;
  const minutes = Math.floor(ms / 60_000);
  const seconds = Math.round((ms % 60_000) / 1000);
  return `${minutes}m ${seconds}s`;
}

function pollEventDuration(event: AutoscanEvent): string {
  if (event.status !== "running") {
    return formatDuration(event.duration_ms);
  }
  const elapsed = Math.max(0, Date.now() - new Date(event.started_at).getTime());
  return formatDuration(elapsed);
}

function pollEventTimestamp(event: AutoscanEvent): string {
  return formatTimestamp(event.status === "running" ? event.started_at : event.completed_at);
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
    case "running":
      return {
        label: "Running",
        icon: RefreshCw,
        className: "border-primary/30 bg-primary/10 text-primary",
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

// arr-plugin sources fan out one-per-connection under a single generic
// capability, so resolve every Activity row through the shared label chain:
// operator label -> connection name -> manifest display_name -> capability_id.
type PollSourceRef = {
  source_id?: string | null;
  plugin_id?: string | null;
  capability_id?: string;
};

function pollSourceName(event: PollSourceRef, lookups: SourceLabelLookups): string {
  return (
    resolveEventSourceName(event, lookups) || event.plugin_id || event.capability_id || "Autoscan"
  );
}

function scanSourceName(scan: AutoscanScan, lookups: SourceLabelLookups): string {
  return resolveEventSourceName(scan, lookups) || "Autoscan";
}

function libraryName(librariesByID: Map<number, Library>, libraryID: number): string {
  return librariesByID.get(libraryID)?.name ?? `Library #${libraryID}`;
}

function PollStatusBadge({ status }: { status: AutoscanEventStatus }) {
  const tone = eventStatusTone(status);
  const Icon = tone.icon;
  return (
    <Badge variant="outline" className={tone.className}>
      <Icon className={cn("h-3.5 w-3.5", status === "running" && "animate-spin")} />
      {tone.label}
    </Badge>
  );
}

function ScanStatusBadge({ status }: { status: AutoscanScanStatus | ScanRun["status"] }) {
  return (
    <Badge variant="outline" className={cn("capitalize tabular-nums", scanStatusClass(status))}>
      {scanStatusLabel(status)}
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
          <div className="text-muted-foreground whitespace-nowrap tabular-nums">
            {formatTime(run.completed_at ?? run.started_at ?? run.requested_at)}
          </div>
        </div>
      ))}
    </div>
  );
}

function PollMetricStrip({ event }: { event: AutoscanEvent }) {
  return (
    <div className="text-muted-foreground flex flex-wrap gap-x-4 gap-y-1 text-xs tabular-nums">
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
            <TableHead>Status</TableHead>
            <TableHead>Library</TableHead>
            <TableHead>Scope</TableHead>
            <TableHead>Progress</TableHead>
            <TableHead className="w-20 text-right">Action</TableHead>
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
              <TableCell className="text-right">
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
      </DataTable>

      {scans.length > QUEUE_PAGE_SIZE ? (
        <TablePagination
          page={safePage}
          pageSize={QUEUE_PAGE_SIZE}
          total={scans.length}
          onPageChange={setPage}
          itemNoun="scan"
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
  if (polls.length === 0) return null;

  return (
    <section className="space-y-3" role="status" aria-live="polite">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="flex items-center gap-2 text-sm font-semibold">
            <RefreshCw className="text-primary h-4 w-4 animate-spin" />
            Polling now
          </div>
          <p className="text-muted-foreground mt-1 text-xs">
            Scan-source plugin calls currently in progress.
          </p>
        </div>
        <Badge variant="secondary" className="tabular-nums">
          {polls.length} active
        </Badge>
      </div>

      <div className="grid gap-3 lg:grid-cols-2">
        {polls.map((poll) => (
          <div key={poll.id} className="border-border rounded-lg border p-4">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div className="min-w-0">
                <div className="font-medium">{pollSourceName(poll, lookups)}</div>
                <div className="text-muted-foreground mt-1 text-xs [overflow-wrap:anywhere]">
                  {poll.plugin_id} · {poll.capability_id}
                </div>
              </div>
              <PollStatusBadge status="running" />
            </div>
            <div className="text-muted-foreground mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs tabular-nums">
              <span>Started {formatTimestamp(poll.started_at)}</span>
              <span>{formatDuration(poll.elapsed_ms)} elapsed</span>
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
            <span>{scanSourceName(scan, lookups)}</span>
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
      <div className="text-muted-foreground mt-3 font-mono text-[11px] [overflow-wrap:anywhere]">
        {scan.id}
      </div>
    </div>
  );
}

function ScanHistoryTable({
  scans,
  librariesByID,
  lookups,
}: {
  scans: AutoscanScan[];
  librariesByID: Map<number, Library>;
  lookups: SourceLabelLookups;
}) {
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
            <TableHead>Status</TableHead>
            <TableHead>Library</TableHead>
            <TableHead>Source</TableHead>
            <TableHead>Scope</TableHead>
            <TableHead>Time</TableHead>
            <TableHead>Poll</TableHead>
          </>
        }
      >
        {scans.map((scan) => (
          <TableRow key={scan.id}>
            <TableCell>
              <ScanStatusBadge status={scan.status} />
            </TableCell>
            <TableCell className="font-medium">
              {libraryName(librariesByID, scan.library_id)}
              <div className="text-muted-foreground mt-1 font-mono text-[11px]">{scan.id}</div>
            </TableCell>
            <TableCell className="whitespace-nowrap">{scanSourceName(scan, lookups)}</TableCell>
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
            <TableCell className="text-muted-foreground whitespace-nowrap tabular-nums">
              {formatTimestamp(scan.completed_at ?? scan.started_at ?? scan.requested_at)}
            </TableCell>
            <TableCell>
              {scan.event_status ? <PollStatusBadge status={scan.event_status} /> : "-"}
            </TableCell>
          </TableRow>
        ))}
      </DataTable>
    </>
  );
}

function PollEventCard({ event, lookups }: { event: AutoscanEvent; lookups: SourceLabelLookups }) {
  return (
    <div className="border-border rounded-lg border p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <PollStatusBadge status={event.status} />
            <span className="font-medium">{pollSourceName(event, lookups)}</span>
          </div>
          <PollMetricStrip event={event} />
        </div>
        <div className="text-muted-foreground text-right text-xs tabular-nums">
          <div>{pollEventTimestamp(event)}</div>
          <div>{pollEventDuration(event)}</div>
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

function PollEventTable({
  events,
  lookups,
}: {
  events: AutoscanEvent[];
  lookups: SourceLabelLookups;
}) {
  return (
    <>
      <div className="space-y-3 lg:hidden">
        {events.map((event) => (
          <PollEventCard key={event.id} event={event} lookups={lookups} />
        ))}
      </div>
      <DataTable
        head={
          <>
            <TableHead>Status</TableHead>
            <TableHead>Source</TableHead>
            <TableHead>Counts</TableHead>
            <TableHead>Scans</TableHead>
            <TableHead>Duration</TableHead>
            <TableHead>Time</TableHead>
          </>
        }
      >
        {events.map((event) => (
          <TableRow key={event.id}>
            <TableCell>
              <PollStatusBadge status={event.status} />
            </TableCell>
            <TableCell className="font-medium">{pollSourceName(event, lookups)}</TableCell>
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
            <TableCell className="whitespace-nowrap tabular-nums">
              {pollEventDuration(event)}
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
  const itemNoun = historyView === "scans" ? "scan" : "poll";

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
        <StatTile label="Active" value={queue?.active_scans ?? 0} icon={ScanLine} />
        <StatTile label="Queued" value={queue?.accepted_scans ?? 0} />
        <StatTile label="Running scans" value={queue?.running_scans ?? 0} />
        <StatTile label="Polling" value={queue?.running_polls?.length ?? 0} icon={RefreshCw} />
        <div className="flex items-start justify-between gap-3 sm:block">
          <StatTile label="Latest poll" value={formatTime(queue?.latest_event_at)} icon={Clock} />
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
                  setPage(0);
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
                  setPage(0);
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
                  setPage(0);
                }}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">All statuses</SelectItem>
                  <SelectItem value="running">Running</SelectItem>
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
          <HistorySkeleton />
        ) : activeQuery.isError ? (
          <div className="border-destructive/30 bg-destructive/5 rounded-lg border p-8 text-center">
            <p className="text-destructive text-sm">Failed to load autoscan activity.</p>
            <Button variant="outline" size="sm" className="mt-3" onClick={refresh}>
              Try again
            </Button>
          </div>
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
          <div className="space-y-4">
            {historyView === "scans" ? (
              <ScanHistoryTable
                scans={activeRows as AutoscanScan[]}
                librariesByID={librariesByID}
                lookups={labelLookups}
              />
            ) : (
              <PollEventTable events={activeRows as AutoscanEvent[]} lookups={labelLookups} />
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
              itemNoun={itemNoun}
              isFetching={activeQuery.isFetching}
            />
          </div>
        )}
      </section>
    </div>
  );
}
