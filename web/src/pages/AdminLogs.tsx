import { memo, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { useSearchParams } from "react-router";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type { AuditLogEntry, OperationalLogEntry } from "@/api/types";
import { captureProfileRequestContext } from "@/api/client";
import { useOptionalAuth } from "@/hooks/useAuth";
import { useOperationalLogs, useAuditLogs } from "@/hooks/queries/admin/logs";
import { useAdminLogStream } from "@/hooks/admin/useAdminLogStream";
import { formatDateTime as formatPreferredDateTime } from "@/lib/datetime";
import { useDateTimeFormat } from "@/hooks/useDateTimeFormat";

export default function AdminLogs() {
  useOptionalAuth();
  const authority = captureProfileRequestContext();
  // Discard displayed logs when the administrator authority changes.
  const scope = JSON.stringify([
    authority?.serverOrigin,
    authority?.authContextVersion,
    authority?.profileId,
    authority?.profileTokenGeneration,
  ]);
  return <AdminLogsPage key={scope} />;
}

function AdminLogsPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const scope = searchParams.toString();
  const [history, setHistory] = useState<{ scope: string; cursors: (string | undefined)[] }>({
    scope,
    cursors: [],
  });
  // A server cursor belongs to the filters that produced it. Keep the filter
  // inputs mounted as users type, while discarding history from other filters.
  const historyCursors = history.scope === scope ? history.cursors : [];
  function setHistoryCursors(cursors: (string | undefined)[]) {
    setHistory({ scope, cursors });
  }
  const browsingHistory = historyCursors.length > 0;
  const cursor = historyCursors.at(-1);
  const focus = searchParams.get("focus") ?? "";
  const playbackFocused = focus === "playback";
  const tabParam = searchParams.get("tab");
  const tab = tabParam === "audit" ? "audit" : "app";
  const requestID = searchParams.get("request_id") ?? "";
  const messageQuery = searchParams.get("q") ?? "";
  const component =
    searchParams.get("component") ??
    (playbackFocused && searchParams.get("playback_session_id") ? "" : "");
  const method = searchParams.get("method") ?? "";
  const clientIP = searchParams.get("client_ip") ?? "";
  const playbackSessionID = searchParams.get("playback_session_id") ?? "";
  const [selectedEntry, setSelectedEntry] = useState<OperationalLogEntry | null>(null);

  function updateSearchParam(key: string, value: string) {
    const next = new URLSearchParams(searchParams);
    if (value.trim()) {
      next.set(key, value);
    } else {
      next.delete(key);
    }
    setHistoryCursors([]);
    setSearchParams(next, { replace: true });
  }

  const operationalParams = useMemo(
    () => ({
      request_id: requestID || undefined,
      q: messageQuery || undefined,
      component: component || undefined,
      playback_session_id: playbackSessionID || undefined,
    }),
    [requestID, messageQuery, component, playbackSessionID],
  );
  const auditParams = useMemo(
    () => ({
      request_id: requestID || undefined,
      method: method || undefined,
      client_ip: clientIP || undefined,
      playback_session_id: playbackSessionID || undefined,
    }),
    [requestID, method, clientIP, playbackSessionID],
  );

  const appLogs = useAdminLogStream("app", operationalParams, tab === "app" && !browsingHistory);
  const auditLogs = useAdminLogStream("audit", auditParams, tab === "audit" && !browsingHistory);
  const appHistory = useOperationalLogs(
    { ...operationalParams, cursor },
    tab === "app" && browsingHistory,
  );
  const auditHistory = useAuditLogs({ ...auditParams, cursor }, tab === "audit" && browsingHistory);
  const activeStream = tab === "app" ? appLogs : auditLogs;
  const activeHistory = tab === "app" ? appHistory : auditHistory;
  const appRows = browsingHistory ? (appHistory.data?.entries ?? []) : appLogs.rows;
  const auditRows = browsingHistory ? (auditHistory.data?.entries ?? []) : auditLogs.rows;

  return (
    <div className="space-y-6">
      <div className="page-header gap-5">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Logs</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Search application logs and request audit trails without leaving the admin UI.
          </p>
        </div>
        <div className="text-right">
          <div className="text-muted-foreground text-xs font-medium tracking-[0.2em] uppercase">
            Stream
          </div>
          <div className="text-sm">
            {browsingHistory
              ? "Historical results"
              : formatConnectionState(activeStream.connectionState)}
          </div>
          {!browsingHistory && activeStream.error && (
            <div className="text-muted-foreground text-xs">{activeStream.error}</div>
          )}
          {!browsingHistory && activeStream.connectionState === "disconnected" && (
            <Button
              variant="ghost"
              size="sm"
              className="mt-1 h-7 px-2 text-xs"
              onClick={activeStream.reconnect}
            >
              Reconnect
            </Button>
          )}
        </div>
      </div>

      <div className="surface-panel rounded-2xl border-0 px-3 py-3">
        <div className="flex flex-wrap items-center gap-2">
          <Input
            placeholder="Playback Session ID"
            value={playbackSessionID}
            onChange={(e) => updateSearchParam("playback_session_id", e.target.value)}
            className="max-w-md font-mono text-xs"
          />
          {playbackSessionID && (
            <div className="bg-background border-border rounded-md border px-3 py-1.5 text-xs">
              Playback session {shortID(playbackSessionID)}
            </div>
          )}
        </div>
      </div>

      {playbackSessionID && (
        <PlaybackSessionSummary
          playbackSessionID={playbackSessionID}
          appRows={appRows}
          auditRows={auditRows}
          component={component}
          onFilterFFmpeg={() =>
            updateSearchParam("component", component === "ffmpeg" ? "" : "ffmpeg")
          }
        />
      )}

      <Tabs
        value={playbackFocused && playbackSessionID && tabParam !== "audit" ? "app" : tab}
        onValueChange={(value) => updateSearchParam("tab", value)}
      >
        <TabsList>
          <TabsTrigger value="app">Application</TabsTrigger>
          <TabsTrigger value="audit">Audit</TabsTrigger>
        </TabsList>

        <TabsContent value="app" className="space-y-4">
          <div className="surface-panel-subtle flex flex-wrap gap-2 rounded-xl p-3">
            <Input
              placeholder="Request ID"
              value={requestID}
              onChange={(e) => updateSearchParam("request_id", e.target.value)}
              className="max-w-xs font-mono"
            />
            <Input
              placeholder="Message contains..."
              value={messageQuery}
              onChange={(e) => updateSearchParam("q", e.target.value)}
              className="max-w-sm"
            />
            <Input
              placeholder="Component"
              value={component}
              onChange={(e) => updateSearchParam("component", e.target.value)}
              className="max-w-xs"
            />
            {playbackFocused && playbackSessionID && (
              <button
                type="button"
                className={`rounded-md border px-3 py-2 text-xs font-medium ${
                  component === "ffmpeg"
                    ? "border-primary bg-primary/10 text-primary"
                    : "border-border bg-background text-muted-foreground"
                }`}
                onClick={() =>
                  updateSearchParam("component", component === "ffmpeg" ? "" : "ffmpeg")
                }
              >
                {component === "ffmpeg" ? "Showing ffmpeg only" : "Filter ffmpeg"}
              </button>
            )}
          </div>
          <LogTable
            rows={appRows}
            isLoading={
              browsingHistory ? appHistory.isPending : appLogs.isConnecting && appRows.length === 0
            }
            empty={
              browsingHistory && appHistory.isError
                ? "Application logs could not be loaded."
                : "No application logs matched the current filters."
            }
            renderRow={(entry) => (
              <OperationalLogRow
                entry={entry}
                key={`app-${entry.id}`}
                highlight={playbackFocused && entry.component === "ffmpeg"}
                onSelectEntry={setSelectedEntry}
              />
            )}
            header={
              <TableRow>
                <TableHead>Time</TableHead>
                <TableHead>Level</TableHead>
                <TableHead>Component</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Duration</TableHead>
                <TableHead>Message</TableHead>
              </TableRow>
            }
          />
        </TabsContent>

        <TabsContent value="audit" className="space-y-4">
          <div className="surface-panel-subtle flex flex-wrap gap-2 rounded-xl p-3">
            <Input
              placeholder="Request ID"
              value={requestID}
              onChange={(e) => updateSearchParam("request_id", e.target.value)}
              className="max-w-xs font-mono"
            />
            <Input
              placeholder="Method"
              value={method}
              onChange={(e) => updateSearchParam("method", e.target.value)}
              className="max-w-[120px]"
            />
            <Input
              placeholder="Client IP"
              value={clientIP}
              onChange={(e) => updateSearchParam("client_ip", e.target.value)}
              className="max-w-xs font-mono"
            />
          </div>
          <LogTable
            rows={auditRows}
            isLoading={
              browsingHistory
                ? auditHistory.isPending
                : auditLogs.isConnecting && auditRows.length === 0
            }
            empty={
              browsingHistory && auditHistory.isError
                ? "Audit logs could not be loaded."
                : "No audit logs matched the current filters."
            }
            renderRow={(entry) => <AuditLogRow entry={entry} key={`audit-${entry.id}`} />}
            header={
              <TableRow>
                <TableHead>Time</TableHead>
                <TableHead>Method</TableHead>
                <TableHead>Path</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Client</TableHead>
                <TableHead>User</TableHead>
                <TableHead>Session</TableHead>
                <TableHead>Playback</TableHead>
                <TableHead>Request</TableHead>
              </TableRow>
            }
          />
        </TabsContent>
      </Tabs>

      <div className="flex flex-wrap items-center gap-3">
        {browsingHistory ? (
          <>
            <Button variant="outline" onClick={() => setHistoryCursors([])}>
              Return to live logs
            </Button>
            <Button
              variant="outline"
              disabled={historyCursors.length < 2 || activeHistory.isFetching}
              onClick={() => setHistoryCursors(historyCursors.slice(0, -1))}
            >
              Newer
            </Button>
            <span className="text-muted-foreground text-sm">Page {historyCursors.length}</span>
            <Button
              variant="outline"
              disabled={
                !activeHistory.data?.next_cursor ||
                activeHistory.isFetching ||
                activeHistory.isError
              }
              onClick={() => {
                const next = activeHistory.data?.next_cursor;
                if (next) setHistoryCursors([...historyCursors, next]);
              }}
            >
              Older
            </Button>
          </>
        ) : (
          // Start with a fresh REST snapshot: the live stream may have trimmed
          // rows since its snapshot cursor was issued.
          <Button variant="outline" onClick={() => setHistoryCursors([undefined])}>
            Browse log history
          </Button>
        )}
        {browsingHistory && activeHistory.isError && (
          <div role="alert" className="flex items-center gap-2">
            <span>Log history could not be loaded.</span>
            <Button
              variant="outline"
              disabled={activeHistory.isFetching}
              onClick={() => void activeHistory.refetch()}
            >
              Retry log history
            </Button>
          </div>
        )}
      </div>

      <Sheet open={selectedEntry !== null} onOpenChange={(open) => !open && setSelectedEntry(null)}>
        <SheetContent className="w-full sm:max-w-2xl">
          {selectedEntry && (
            <>
              <SheetHeader>
                <SheetTitle>{selectedEntry.message}</SheetTitle>
                <SheetDescription>
                  {selectedEntry.component} · {selectedEntry.level.toUpperCase()} ·{" "}
                  {formatDateTime(selectedEntry.timestamp)}
                </SheetDescription>
              </SheetHeader>
              <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-4 pb-6">
                <div className="grid grid-cols-2 gap-3 text-sm">
                  <DetailField label="Request ID" value={selectedEntry.request_id || "-"} mono />
                  <DetailField label="Node" value={selectedEntry.node_id || "-"} />
                  <DetailField label="Method" value={stringAttr(selectedEntry, "method")} />
                  <DetailField label="Status" value={stringAttr(selectedEntry, "status")} />
                  <DetailField label="Duration" value={durationAttr(selectedEntry)} />
                  <DetailField
                    label="Client IP"
                    value={selectedEntry.client_ip || stringAttr(selectedEntry, "client_ip")}
                    mono
                  />
                  <DetailField
                    label="User ID"
                    value={selectedEntry.user_id ? String(selectedEntry.user_id) : "-"}
                  />
                  <DetailField
                    label="Session ID"
                    value={selectedEntry.session_id || stringAttr(selectedEntry, "session_id")}
                    mono
                  />
                  <DetailField
                    label="Playback Session"
                    value={
                      selectedEntry.playback_session_id ||
                      stringAttr(selectedEntry, "playback_session_id")
                    }
                    mono
                  />
                </div>
                {(selectedEntry.playback_session_id ||
                  stringAttr(selectedEntry, "playback_session_id") !== "-") && (
                  <button
                    type="button"
                    className="text-primary text-sm font-medium"
                    onClick={() => {
                      const value =
                        selectedEntry.playback_session_id ||
                        stringAttr(selectedEntry, "playback_session_id");
                      updateSearchParam("playback_session_id", value === "-" ? "" : value);
                      setSelectedEntry(null);
                    }}
                  >
                    View related playback session logs
                  </button>
                )}
                <div>
                  <div className="mb-1 text-sm font-medium">Path</div>
                  <div className="bg-muted rounded-md px-3 py-2 font-mono text-xs break-all">
                    {stringAttr(selectedEntry, "path")}
                  </div>
                </div>
                <div>
                  <div className="mb-1 text-sm font-medium">Attributes</div>
                  <pre className="bg-muted max-h-[420px] overflow-auto rounded-md p-3 text-xs leading-5 break-all whitespace-pre-wrap">
                    {JSON.stringify(selectedEntry.attrs ?? {}, null, 2)}
                  </pre>
                </div>
              </div>
            </>
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}

function LogTable<T>({
  rows,
  isLoading,
  empty,
  header,
  renderRow,
}: {
  rows: T[];
  isLoading: boolean;
  empty: string;
  header: ReactNode;
  renderRow: (row: T) => ReactNode;
}) {
  if (isLoading) return <div className="text-muted-foreground py-8 text-sm">Loading logs...</div>;
  if (rows.length === 0) return <div className="text-muted-foreground py-8 text-sm">{empty}</div>;

  return (
    <Table>
      <TableHeader>{header}</TableHeader>
      <TableBody>{rows.map(renderRow)}</TableBody>
    </Table>
  );
}

const OperationalLogRow = memo(function OperationalLogRow({
  entry,
  highlight = false,
  onSelectEntry,
}: {
  entry: OperationalLogEntry;
  highlight?: boolean;
  onSelectEntry: (entry: OperationalLogEntry) => void;
}) {
  useDateTimeFormat();
  return (
    <TableRow
      className={`cursor-pointer ${highlight ? "bg-primary/5" : ""}`}
      onClick={() => onSelectEntry(entry)}
    >
      <TableCell className="whitespace-nowrap">{formatDateTime(entry.timestamp)}</TableCell>
      <TableCell className="uppercase">{entry.level}</TableCell>
      <TableCell>{entry.component}</TableCell>
      <TableCell>{stringAttr(entry, "status")}</TableCell>
      <TableCell>{durationAttr(entry)}</TableCell>
      <TableCell>
        <div className="max-w-[360px] truncate" title={entry.message}>
          {entry.message}
        </div>
      </TableCell>
    </TableRow>
  );
});

const AuditLogRow = memo(function AuditLogRow({ entry }: { entry: AuditLogEntry }) {
  useDateTimeFormat();
  return (
    <TableRow>
      <TableCell className="whitespace-nowrap">{formatDateTime(entry.timestamp)}</TableCell>
      <TableCell>{entry.method}</TableCell>
      <TableCell>
        <div className="max-w-[420px] truncate font-mono text-xs" title={entry.path}>
          {entry.path}
        </div>
      </TableCell>
      <TableCell>{entry.status_code}</TableCell>
      <TableCell className="font-mono text-xs">{formatClientIP(entry.client_ip)}</TableCell>
      <TableCell>{entry.user_id ? `#${entry.user_id}` : "-"}</TableCell>
      <TableCell className="font-mono text-xs">{entry.session_id || "-"}</TableCell>
      <TableCell className="font-mono text-xs">{entry.playback_session_id || "-"}</TableCell>
      <TableCell className="font-mono text-xs">{entry.request_id || "-"}</TableCell>
    </TableRow>
  );
});

function PlaybackSessionSummary({
  playbackSessionID,
  appRows,
  auditRows,
  component,
  onFilterFFmpeg,
}: {
  playbackSessionID: string;
  appRows: OperationalLogEntry[];
  auditRows: AuditLogEntry[];
  component: string;
  onFilterFFmpeg: () => void;
}) {
  const { appCount, ffmpegCount, auditCount, firstSeen, lastSeen, nodes } = useMemo(() => {
    const matchingAppRows = appRows.filter((row) => matchesPlaybackSession(row, playbackSessionID));
    const matchingAuditRows = auditRows.filter((row) =>
      matchesPlaybackSession(row, playbackSessionID),
    );
    const timestamps = [...matchingAppRows, ...matchingAuditRows]
      .map((row) => row.timestamp)
      .sort();

    return {
      appCount: matchingAppRows.length,
      ffmpegCount: matchingAppRows.filter((row) => row.component === "ffmpeg").length,
      auditCount: matchingAuditRows.length,
      firstSeen: timestamps[0],
      lastSeen: timestamps[timestamps.length - 1],
      nodes: new Set(
        [...matchingAppRows, ...matchingAuditRows].map((row) => row.node_id).filter(Boolean),
      ),
    };
  }, [appRows, auditRows, playbackSessionID]);

  return (
    <div className="bg-card border-border rounded-lg border p-4 text-sm">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
        <div className="flex flex-wrap gap-3 md:grid md:flex-1 md:grid-cols-6">
          <SummaryMetric label="Playback Session" value={shortID(playbackSessionID)} mono />
          <SummaryMetric label="Application Logs" value={String(appCount)} />
          <SummaryMetric label="FFmpeg Logs" value={String(ffmpegCount)} />
          <SummaryMetric label="Audit Logs" value={String(auditCount)} />
          <SummaryMetric label="First Seen" value={firstSeen ? formatDateTime(firstSeen) : "-"} />
          <SummaryMetric
            label="Nodes Seen"
            value={nodes.size > 0 ? Array.from(nodes).join(", ") : "-"}
            mono={nodes.size > 0}
          />
          {lastSeen && <SummaryMetric label="Last Seen" value={formatDateTime(lastSeen)} />}
        </div>
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={onFilterFFmpeg}
            className={`rounded-md border px-3 py-2 text-xs font-medium ${
              component === "ffmpeg"
                ? "border-primary bg-primary/10 text-primary"
                : "border-border bg-background text-muted-foreground"
            }`}
          >
            {component === "ffmpeg" ? "Showing ffmpeg only" : "Open ffmpeg logs"}
          </button>
        </div>
      </div>
    </div>
  );
}

function SummaryMetric({
  label,
  value,
  mono = false,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div>
      <div className="text-muted-foreground mb-1 text-xs">{label}</div>
      <div className={mono ? "font-mono text-xs break-all" : "text-sm"}>{value}</div>
    </div>
  );
}

function matchesPlaybackSession(
  entry: OperationalLogEntry | AuditLogEntry,
  playbackSessionID: string,
) {
  if (!playbackSessionID) return true;
  return entry.playback_session_id === playbackSessionID;
}

function shortID(value: string) {
  if (value.length <= 12) return value;
  return `${value.slice(0, 8)}...${value.slice(-4)}`;
}

function formatDateTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return formatPreferredDateTime(date);
}

function formatConnectionState(state: "connecting" | "live" | "disconnected") {
  switch (state) {
    case "connecting":
      return "Connecting...";
    case "live":
      return "Live";
    default:
      return "Disconnected";
  }
}

function formatClientIP(value: string) {
  if (!value) return "-";
  return value.replace(/\/\d+$/, "");
}

function stringAttr(entry: OperationalLogEntry, key: string) {
  const value = entry.attrs?.[key];
  if (typeof value === "string" && value.length > 0) return value;
  if (typeof value === "number") return String(value);
  return "-";
}

function durationAttr(entry: OperationalLogEntry) {
  const value = entry.attrs?.duration_ms;
  if (typeof value === "number") return `${value} ms`;
  if (typeof value === "string" && value.length > 0) return `${value} ms`;
  return "-";
}

function DetailField({
  label,
  value,
  mono = false,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div>
      <div className="text-muted-foreground mb-1 text-xs">{label}</div>
      <div className={mono ? "font-mono text-xs break-all" : "text-sm"}>{value}</div>
    </div>
  );
}
