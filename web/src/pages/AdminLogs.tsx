import { memo, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { useSearchParams } from "react-router";
import { PageHeader } from "@/components/page/PageHeader";
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
import { useDateTimeFormat } from "@/hooks/useDateTimeFormat";
import { useT, type MessageKey, type Translate } from "@/i18n";
import {
  LOG_COMPONENT_FILTER_OPTIONS,
  LOG_FILTER_ALL,
  LOG_LEVEL_FILTER_OPTIONS,
  normalizeLogFilterParam,
  withUnknownFilterOption,
  type LogLevelFilterOption,
} from "@/pages/adminLogsFilters";
import {
  formatClientIP,
  formatConnectionState,
  formatDateTime,
  shortID,
  stringAttr,
} from "./AdminLogs.format";
import { PlaybackSessionSummary } from "./AdminLogsPlaybackSummary";

const SEPARATOR = "·";
const NO_VALUE = "-";

const LEVEL_LABELS: Record<LogLevelFilterOption, MessageKey> = {
  debug: "web.admin.logs.level.debug",
  info: "web.admin.logs.level.info",
  warn: "web.admin.logs.level.warn",
  error: "web.admin.logs.level.error",
};

function levelLabel(t: Translate, value: string): string {
  const key = LEVEL_LABELS[value as LogLevelFilterOption] as MessageKey | undefined;
  return key ? t(key) : value;
}

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
  const t = useT();
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
  const level = normalizeLogFilterParam(searchParams.get("level") ?? "");
  const component = searchParams.get("component") ?? "";
  const method = searchParams.get("method") ?? "";
  const action = searchParams.get("action") ?? "";
  const actor = searchParams.get("actor_user_id") ?? "";
  const targetID = searchParams.get("target_id") ?? "";
  const targetType = searchParams.get("target_type") ?? "";
  const clientIP = searchParams.get("client_ip") ?? "";
  const playbackSessionID = searchParams.get("playback_session_id") ?? "";
  const [selectedEntry, setSelectedEntry] = useState<OperationalLogEntry | null>(null);
  const levelOptions = useMemo(
    () => withUnknownFilterOption(LOG_LEVEL_FILTER_OPTIONS, level),
    [level],
  );

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
      level: level || undefined,
      component: normalizeLogFilterParam(component) || undefined,
      playback_session_id: playbackSessionID || undefined,
    }),
    [requestID, messageQuery, level, component, playbackSessionID],
  );
  const auditParams = useMemo(
    () => ({
      action: action || undefined,
      actor_user_id: actor || undefined,
      target_id: targetID || undefined,
      target_type: targetType || undefined,
      request_id: requestID || undefined,
      method: method || undefined,
      client_ip: clientIP || undefined,
      playback_session_id: playbackSessionID || undefined,
    }),
    [requestID, method, clientIP, playbackSessionID, action, actor, targetID, targetType],
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
      <PageHeader
        title={t("web.admin.logs.page.title")}
        subtitle={t("web.admin.logs.page.subtitle")}
        actions={
          <div className="text-end">
            <div className="text-muted-foreground text-xs font-medium tracking-[0.2em] uppercase">
              {t("web.admin.logs.page.streamLabel")}
            </div>
            <div className="text-sm">
              {browsingHistory
                ? t("web.admin.logs.page.historicalResults")
                : formatConnectionState(t, activeStream.connectionState)}
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
                {t("web.admin.logs.page.reconnect")}
              </Button>
            )}
          </div>
        }
      />

      <div className="surface-panel rounded-2xl border-0 px-3 py-3">
        <div className="flex flex-wrap items-center gap-2">
          <Input
            placeholder={t("web.admin.logs.filters.playbackSessionId")}
            value={playbackSessionID}
            onChange={(e) => updateSearchParam("playback_session_id", e.target.value)}
            className="max-w-md font-mono text-xs"
          />
          {playbackSessionID && (
            <div className="bg-background border-border rounded-md border px-3 py-1.5 text-xs">
              {t("web.admin.logs.filters.playbackSessionChip", { id: shortID(playbackSessionID) })}
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
          <TabsTrigger value="app">{t("web.admin.logs.tabs.application")}</TabsTrigger>
          <TabsTrigger value="audit">{t("web.admin.logs.tabs.audit")}</TabsTrigger>
        </TabsList>

        <TabsContent value="app" className="space-y-4">
          <div className="surface-panel-subtle flex flex-wrap gap-2 rounded-xl p-3">
            <Input
              placeholder={t("web.admin.logs.filters.requestId")}
              value={requestID}
              onChange={(e) => updateSearchParam("request_id", e.target.value)}
              className="max-w-xs font-mono"
            />
            <Input
              placeholder={t("web.admin.logs.filters.messageContains")}
              value={messageQuery}
              onChange={(e) => updateSearchParam("q", e.target.value)}
              className="max-w-sm"
            />
            <Select
              value={level || LOG_FILTER_ALL}
              onValueChange={(value) =>
                updateSearchParam("level", value === LOG_FILTER_ALL ? "" : value)
              }
            >
              <SelectTrigger className="w-[170px]" aria-label={t("web.admin.logs.filters.level")}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={LOG_FILTER_ALL}>
                  {t("web.admin.logs.filters.allLevels")}
                </SelectItem>
                {levelOptions.map((value) => (
                  <SelectItem key={value} value={value}>
                    {levelLabel(t, value)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Input
              aria-label={t("web.admin.logs.filters.component")}
              placeholder={t("web.admin.logs.filters.allComponents")}
              list="log-component-options"
              value={component}
              onChange={(e) => updateSearchParam("component", e.target.value)}
              className="max-w-xs"
            />
            <datalist id="log-component-options">
              {LOG_COMPONENT_FILTER_OPTIONS.map((value) => (
                <option key={value} value={value} />
              ))}
            </datalist>
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
                {component === "ffmpeg"
                  ? t("web.admin.logs.ffmpeg.showingOnly")
                  : t("web.admin.logs.ffmpeg.filter")}
              </button>
            )}
          </div>
          <LogTable
            rows={appRows}
            isLoading={
              browsingHistory ? appHistory.isPending : appLogs.isConnecting && appRows.length === 0
            }
            empty={
              (browsingHistory ? appHistory.isError : appLogs.error)
                ? t("web.admin.logs.app.loadError")
                : t("web.admin.logs.app.empty")
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
                <TableHead>{t("web.admin.logs.columns.time")}</TableHead>
                <TableHead>{t("web.admin.logs.columns.level")}</TableHead>
                <TableHead>{t("web.admin.logs.columns.component")}</TableHead>
                <TableHead>{t("web.admin.logs.columns.status")}</TableHead>
                <TableHead>{t("web.admin.logs.columns.duration")}</TableHead>
                <TableHead>{t("web.admin.logs.columns.message")}</TableHead>
              </TableRow>
            }
          />
        </TabsContent>

        <TabsContent value="audit" className="space-y-4">
          <div className="surface-panel-subtle flex flex-wrap gap-2 rounded-xl p-3">
            <Input
              placeholder={t("web.admin.logs.filters.requestId")}
              value={requestID}
              onChange={(e) => updateSearchParam("request_id", e.target.value)}
              className="max-w-xs font-mono"
            />
            <Input
              placeholder={t("web.admin.logs.filters.method")}
              value={method}
              onChange={(e) => updateSearchParam("method", e.target.value)}
              className="max-w-[120px]"
            />
            <Input
              placeholder={t("web.admin.logs.filters.clientIp")}
              value={clientIP}
              onChange={(e) => updateSearchParam("client_ip", e.target.value)}
              className="max-w-xs font-mono"
            />
            <Input
              aria-label={t("web.admin.logs.filters.auditAction")}
              placeholder={t("web.admin.logs.filters.auditActionPlaceholder")}
              value={action}
              onChange={(e) => updateSearchParam("action", e.target.value)}
              className="max-w-xs"
            />
            <Input
              aria-label={t("web.admin.logs.filters.auditActor")}
              placeholder={t("web.admin.logs.filters.auditActorPlaceholder")}
              inputMode="numeric"
              value={actor}
              onChange={(e) => updateSearchParam("actor_user_id", e.target.value)}
              className="max-w-[180px]"
            />
            <Input
              aria-label={t("web.admin.logs.filters.auditTargetType")}
              placeholder={t("web.admin.logs.filters.auditTargetTypePlaceholder")}
              value={targetType}
              onChange={(e) => updateSearchParam("target_type", e.target.value)}
              className="max-w-xs"
            />
            <Input
              aria-label={t("web.admin.logs.filters.auditTargetId")}
              placeholder={t("web.admin.logs.filters.auditTargetIdPlaceholder")}
              value={targetID}
              onChange={(e) => updateSearchParam("target_id", e.target.value)}
              className="max-w-[180px]"
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
              (browsingHistory ? auditHistory.isError : auditLogs.error)
                ? t("web.admin.logs.audit.loadError")
                : t("web.admin.logs.audit.empty")
            }
            renderRow={(entry) => <AuditLogRow entry={entry} key={`audit-${entry.id}`} />}
            header={
              <TableRow>
                <TableHead>{t("web.admin.logs.columns.time")}</TableHead>
                <TableHead>{t("web.admin.logs.columns.actionChanges")}</TableHead>
                <TableHead>{t("web.admin.logs.columns.target")}</TableHead>
                <TableHead>{t("web.admin.logs.columns.method")}</TableHead>
                <TableHead>{t("web.admin.logs.columns.path")}</TableHead>
                <TableHead>{t("web.admin.logs.columns.status")}</TableHead>
                <TableHead>{t("web.admin.logs.columns.client")}</TableHead>
                <TableHead>{t("web.admin.logs.columns.actor")}</TableHead>
                <TableHead>{t("web.admin.logs.columns.session")}</TableHead>
                <TableHead>{t("web.admin.logs.columns.playback")}</TableHead>
                <TableHead>{t("web.admin.logs.columns.request")}</TableHead>
              </TableRow>
            }
          />
        </TabsContent>
      </Tabs>

      <div className="flex flex-wrap items-center gap-3">
        {browsingHistory ? (
          <>
            <Button variant="outline" onClick={() => setHistoryCursors([])}>
              {t("web.admin.logs.history.returnToLive")}
            </Button>
            <Button
              variant="outline"
              disabled={historyCursors.length < 2 || activeHistory.isFetching}
              onClick={() => setHistoryCursors(historyCursors.slice(0, -1))}
            >
              {t("web.admin.logs.history.newer")}
            </Button>
            <span className="text-muted-foreground text-sm">
              {t("web.admin.logs.history.page", { page: historyCursors.length })}
            </span>
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
              {t("web.admin.logs.history.older")}
            </Button>
          </>
        ) : (
          // Start with a fresh REST snapshot: the live stream may have trimmed
          // rows since its snapshot cursor was issued.
          <Button variant="outline" onClick={() => setHistoryCursors([undefined])}>
            {t("web.admin.logs.history.browse")}
          </Button>
        )}
        {browsingHistory && activeHistory.isError && (
          <div role="alert" className="flex items-center gap-2">
            <span>{t("web.admin.logs.history.loadError")}</span>
            <Button
              variant="outline"
              disabled={activeHistory.isFetching}
              onClick={() => void activeHistory.refetch()}
            >
              {t("web.admin.logs.history.retry")}
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
                  {[
                    selectedEntry.component,
                    selectedEntry.level.toUpperCase(),
                    formatDateTime(selectedEntry.timestamp),
                  ].join(` ${SEPARATOR} `)}
                </SheetDescription>
              </SheetHeader>
              <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-4 pb-6">
                <div className="grid grid-cols-2 gap-3 text-sm">
                  <DetailField
                    label={t("web.admin.logs.detail.requestId")}
                    value={selectedEntry.request_id || "-"}
                    mono
                  />
                  <DetailField
                    label={t("web.admin.logs.detail.node")}
                    value={selectedEntry.node_id || "-"}
                  />
                  <DetailField
                    label={t("web.admin.logs.detail.method")}
                    value={stringAttr(selectedEntry, "method")}
                  />
                  <DetailField
                    label={t("web.admin.logs.detail.status")}
                    value={stringAttr(selectedEntry, "status")}
                  />
                  <DetailField
                    label={t("web.admin.logs.detail.duration")}
                    value={durationAttr(t, selectedEntry)}
                  />
                  <DetailField
                    label={t("web.admin.logs.detail.clientIp")}
                    value={selectedEntry.client_ip || stringAttr(selectedEntry, "client_ip")}
                    mono
                  />
                  <DetailField
                    label={t("web.admin.logs.detail.userId")}
                    value={selectedEntry.user_id ? String(selectedEntry.user_id) : "-"}
                  />
                  <DetailField
                    label={t("web.admin.logs.detail.sessionId")}
                    value={selectedEntry.session_id || stringAttr(selectedEntry, "session_id")}
                    mono
                  />
                  <DetailField
                    label={t("web.admin.logs.detail.playbackSession")}
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
                    {t("web.admin.logs.detail.viewPlaybackSessionLogs")}
                  </button>
                )}
                <div>
                  <div className="mb-1 text-sm font-medium">{t("web.admin.logs.detail.path")}</div>
                  <div className="bg-muted rounded-md px-3 py-2 font-mono text-xs break-all">
                    {stringAttr(selectedEntry, "path")}
                  </div>
                </div>
                <div>
                  <div className="mb-1 text-sm font-medium">
                    {t("web.admin.logs.detail.attributes")}
                  </div>
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
  const t = useT();
  if (isLoading) {
    return <div className="text-muted-foreground py-8 text-sm">{t("web.admin.logs.loading")}</div>;
  }
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
  const t = useT();
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
      <TableCell>{durationAttr(t, entry)}</TableCell>
      <TableCell>
        <div className="max-w-[360px] truncate" title={entry.message}>
          {entry.message}
        </div>
      </TableCell>
    </TableRow>
  );
});

function auditChangeValue(
  t: Translate,
  value: string | undefined,
  targetType?: string,
  field?: string,
): string {
  if (value === undefined) return t("web.admin.logs.auditValue.absent");
  try {
    const parsed: unknown = JSON.parse(value);
    if (parsed === null) {
      if (field === "access_group_id") return t("web.admin.logs.auditValue.none");
      if (targetType === "access_group") {
        if (field === "library_ids") return t("web.admin.logs.auditValue.allLibraries");
        if (field === "allowed_permissions") return t("web.admin.logs.auditValue.allAssignable");
        return t("web.admin.logs.auditValue.unset");
      }
      return t("web.admin.logs.auditValue.inherit");
    }
    if (field === "scopes" && Array.isArray(parsed) && !parsed.length) {
      return t("web.admin.logs.auditValue.fullAccess");
    }
    if (Array.isArray(parsed)) {
      return parsed.length ? parsed.join(", ") : t("web.admin.logs.auditValue.none");
    }
    return typeof parsed === "object" ? JSON.stringify(parsed) : String(parsed);
  } catch {
    return value;
  }
}

// A profile PIN is audited as a set/not-set marker; true → true is a PIN
// replaced by another one.
function auditPINChange(
  t: Translate,
  before: string | undefined,
  after: string | undefined,
): string {
  if (before !== undefined && after !== undefined) {
    if (before === after) return t("web.admin.logs.auditValue.changed");
    return after === "true"
      ? t("web.admin.logs.auditValue.pinSet")
      : t("web.admin.logs.auditValue.pinCleared");
  }
  const label = (value: string | undefined) =>
    value === undefined
      ? t("web.admin.logs.auditValue.absent")
      : value === "true"
        ? t("web.admin.logs.auditValue.pinSet")
        : t("web.admin.logs.auditValue.pinNotSet");
  return t("web.admin.logs.auditValue.transition", { before: label(before), after: label(after) });
}

type AuditChange = NonNullable<AuditLogEntry["changes"]>[number];

function auditChangeSummary(t: Translate, change: AuditChange, targetType?: string): string {
  if (change.before === undefined && change.after === undefined) {
    return t("web.admin.logs.auditValue.changed");
  }
  if (targetType === "profile" && change.field === "pin") {
    return auditPINChange(t, change.before, change.after);
  }
  return t("web.admin.logs.auditValue.transition", {
    before: auditChangeValue(t, change.before, targetType, change.field),
    after: auditChangeValue(t, change.after, targetType, change.field),
  });
}

function auditTargetLabel(t: Translate, targetType: string): string {
  return targetType === "api_key"
    ? t("web.admin.logs.audit.targetApiKey")
    : targetType.replaceAll("_", " ");
}

const AuditLogRow = memo(function AuditLogRow({ entry }: { entry: AuditLogEntry }) {
  const t = useT();
  useDateTimeFormat();
  return (
    <TableRow>
      <TableCell className="whitespace-nowrap">{formatDateTime(entry.timestamp)}</TableCell>
      <TableCell className="max-w-[520px] min-w-[240px]">
        <div className="font-medium">{entry.action || t("web.admin.logs.audit.httpRequest")}</div>
        {entry.changes?.map((change) => (
          <div key={change.field} className="text-muted-foreground text-xs break-words">
            {t("web.admin.logs.audit.change", {
              field: change.field.replaceAll("_", " "),
              summary: auditChangeSummary(t, change, entry.target_type),
              name: (chunks) => <span className="font-medium">{chunks}</span>,
            })}
          </div>
        ))}
      </TableCell>
      <TableCell className="whitespace-nowrap">
        {entry.target_type
          ? t("web.admin.logs.audit.target", {
              type: auditTargetLabel(t, entry.target_type),
              id: String(entry.target_id),
            })
          : NO_VALUE}
      </TableCell>
      <TableCell>{entry.method}</TableCell>
      <TableCell>
        <div className="max-w-[420px] truncate font-mono text-xs" title={entry.path}>
          {entry.path}
        </div>
      </TableCell>
      <TableCell>{entry.status_code}</TableCell>
      <TableCell className="font-mono text-xs">{formatClientIP(entry.client_ip)}</TableCell>
      <TableCell>
        {entry.impersonator_user_id
          ? t("web.admin.logs.audit.impersonatedActor", {
              impersonator: entry.impersonator_user_id,
              user: String(entry.user_id),
            })
          : entry.user_id
            ? t("web.admin.logs.audit.actor", { user: entry.user_id })
            : NO_VALUE}
      </TableCell>
      <TableCell className="font-mono text-xs">{entry.session_id || NO_VALUE}</TableCell>
      <TableCell className="font-mono text-xs">{entry.playback_session_id || NO_VALUE}</TableCell>
      <TableCell className="font-mono text-xs">{entry.request_id || NO_VALUE}</TableCell>
    </TableRow>
  );
});

function durationAttr(t: Translate, entry: OperationalLogEntry) {
  const value = entry.attrs?.duration_ms;
  if (typeof value === "number") return t("web.admin.logs.durationMs", { value: value });
  if (typeof value === "string" && value.length > 0) {
    return t("web.admin.logs.durationMs", { value });
  }
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
