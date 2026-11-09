import type { ReactNode } from "react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, Navigate, useSearchParams } from "react-router";
import { AdminSessionActions } from "@/components/AdminSessionActions";
import { JellyfinSessionPill } from "@/components/JellyfinSessionPill";
import { PageHeader } from "@/components/page/PageHeader";
import { PlaybackRouteBadges } from "@/components/PlaybackRouteBadges";
import { useRealtimeEvents } from "@/components/realtimeEventsContext";
import { useOperationalLogs } from "@/hooks/queries/admin/logs";
import { usePageActivity } from "@/hooks/usePageActivity";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { DropdownMenuItem } from "@/components/ui/dropdown-menu";
import type { AdminSession, OperationalLogEntry, IPUserEntry } from "@/api/types";
import { useIPUsers } from "@/hooks/queries/admin/ips";
import { useAdminSessions } from "@/hooks/queries/admin/stats";
import {
  activityMethodMeta,
  classifyActivityMethod,
  compareActivityMethods,
  decisionBadgeClass,
  isJellyfinSession,
  formatAudioDetail,
  formatContainerDetail,
  formatDeliveredAudioSummary,
  formatDeliveredContainerSummary,
  formatDeliveredVideoSummary,
  formatAudioSummary,
  formatDecisionLabel,
  formatSessionBitrate,
  formatSourceContainerSummary,
  formatToneMapSummary,
  formatTranscodeModeSummary,
  getSessionClientLabel,
  getSessionClientLabelFull,
  getSessionRouteNodes,
  formatVideoDetail,
  formatVideoSummary,
  normalizeContainerDecision,
  normalizeStreamDecision,
  type ActivityRouteNode,
} from "@/pages/adminActivityPresentation";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  RefreshCw,
  Search,
  Radio,
  Filter,
  X,
  Play,
  Pause,
  Terminal,
  FileText,
  ChevronDown,
  ChevronUp,
} from "lucide-react";
import { formatDateTime, formatTime } from "@/lib/datetime";
import { formatLocale } from "@/lib/appLocale";
import { englishText, numberText, type TextValues, type Translate, useT } from "@/i18n";
import type { MessageKey } from "@/locales/keys";

type SortField = "username" | "media" | "method" | "node" | "started";
type SortDir = "asc" | "desc";

const REFRESH_SPINNER_MIN_VISIBLE_MS = 1_000;
// Punctuation and symbols, the same in every language.
const MIDDLE_DOT = "·";
const SORT_ASC_MARK = "▲";
const SORT_DESC_MARK = "▼";

// The stream table's header and rows share one column template, whose minimums
// add up to 977px plus 4rem of gaps and padding (1041px at a 16px root font,
// 1057px at 20px). Keep the spacing in rem in the container breakpoint so larger
// fonts show stacked rows until the columns fit, regardless of sidebar width.
const STREAM_TABLE_COLUMNS =
  "grid-cols-[minmax(120px,1.1fr)_minmax(190px,1.7fr)_minmax(220px,1.9fr)_minmax(90px,0.8fr)_minmax(125px,0.9fr)_minmax(232px,1.4fr)]";

const STACKED_SORT_FIELDS: [SortField, MessageKey][] = [
  ["username", "web.admin.activity.column.user"],
  ["media", "web.admin.activity.column.stream"],
  ["method", "web.admin.activity.column.playback"],
  ["node", "web.admin.activity.column.route"],
  ["started", "web.admin.activity.column.time"],
];

// Sort keys and search compare English text (and the shown text, for search),
// so a language change neither reorders rows nor loses a typed English query.
function routeSortValue(session: AdminSession): string {
  return getSessionRouteNodes(session, englishText)
    .map((node) => `${node.label} ${node.name}`)
    .join(" ");
}

export default function AdminActivity() {
  const t = useT();
  const { data: sessions = [], isLoading, refetch: refresh } = useAdminSessions();
  const [searchParams] = useSearchParams();
  const { connectionState } = useRealtimeEvents();
  const pageActivity = usePageActivity();
  const error = undefined;
  const manualRefreshStartedAtRef = useRef<number | null>(null);
  const ipLookupRef = useRef<HTMLDetailsElement | null>(null);
  const wasRealtimePausedRef = useRef(!pageActivity.canApplyRealtimeUpdates);
  const [isManualRefreshPending, setIsManualRefreshPending] = useState(false);
  const [search, setSearch] = useState("");
  const [methodFilter, setMethodFilter] = useState<string | null>(null);
  const [nodeFilter, setNodeFilter] = useState<string | null>(null);
  const [typeFilter, setTypeFilter] = useState<string | null>(null);
  const [sortField, setSortField] = useState<SortField>("started");
  const [sortDir, setSortDir] = useState<SortDir>("desc");
  const [ipSearch, setIPSearch] = useState("");
  const [activeIP, setActiveIP] = useState("");
  const [ipLookupOpen, setIPLookupOpen] = useState(false);
  const ipHistory = useIPUsers(activeIP);
  const { data: ipUsers = [], isLoading: ipLoading } = ipHistory;

  const refreshActivity = useCallback(
    async ({ manual }: { manual: boolean }) => {
      if (manual) {
        manualRefreshStartedAtRef.current = Date.now();
        setIsManualRefreshPending(true);
      }
      try {
        await refresh();
      } finally {
        if (manual) {
          const startedAt = manualRefreshStartedAtRef.current;
          if (startedAt !== null) {
            const elapsed = Date.now() - startedAt;
            const remaining = REFRESH_SPINNER_MIN_VISIBLE_MS - elapsed;
            if (remaining > 0) {
              await delay(remaining);
            }
          }
          manualRefreshStartedAtRef.current = null;
          setIsManualRefreshPending(false);
        }
      }
    },
    [refresh],
  );

  useEffect(() => {
    if (!pageActivity.canApplyRealtimeUpdates) {
      wasRealtimePausedRef.current = true;
      return;
    }
    if (!wasRealtimePausedRef.current || isManualRefreshPending) {
      return;
    }

    wasRealtimePausedRef.current = false;
    void refreshActivity({ manual: true });
  }, [isManualRefreshPending, pageActivity.canApplyRealtimeUpdates, refreshActivity]);

  // Aggregate counts
  const methods = useMemo(() => {
    const counts: Record<string, number> = {};
    for (const s of sessions) {
      const method = classifyActivityMethod(s);
      counts[method] = (counts[method] || 0) + 1;
    }
    return counts;
  }, [sessions]);

  const routeNodes = useMemo(() => {
    const counts = new Map<string, ActivityRouteNode & { count: number }>();
    for (const session of sessions) {
      for (const node of getSessionRouteNodes(session)) {
        const current = counts.get(node.key);
        counts.set(node.key, {
          ...node,
          count: (current?.count ?? 0) + 1,
        });
      }
    }
    return [...counts.values()];
    // Node labels come from t() inside getSessionRouteNodes.
    // eslint-disable-next-line react-hooks/exhaustive-deps -- rebuild on a language change
  }, [sessions, t]);

  // Filter + sort
  const filtered = useMemo(() => {
    let result = sessions;
    if (search) {
      const q = search.toLowerCase();
      result = result.filter(
        (s) =>
          s.username?.toLowerCase().includes(q) ||
          s.media_title?.toLowerCase().includes(q) ||
          s.series_name?.toLowerCase().includes(q) ||
          s.episode_name?.toLowerCase().includes(q) ||
          // Search the exact label, not the compact one: "which sessions are on
          // build 5?" is the question this identity exists to answer, and the
          // compact label deliberately omits the build.
          getSessionClientLabelFull(s).toLowerCase().includes(q) ||
          s.client_user_agent?.toLowerCase().includes(q) ||
          s.client_ip?.toLowerCase().includes(q) ||
          [...getSessionRouteNodes(s), ...getSessionRouteNodes(s, englishText)].some((node) =>
            `${node.label} ${node.name}`.toLowerCase().includes(q),
          ),
      );
    }
    if (methodFilter) result = result.filter((s) => classifyActivityMethod(s) === methodFilter);
    if (nodeFilter) {
      result = result.filter((s) =>
        getSessionRouteNodes(s).some((node) => node.key === nodeFilter),
      );
    }
    if (typeFilter) result = result.filter((s) => s.media_type === typeFilter);

    return [...result].sort((a, b) => {
      let cmp = 0;
      switch (sortField) {
        case "username":
          cmp = (a.username || "").localeCompare(b.username || "");
          break;
        case "media":
          cmp = getDisplayTitle(a, englishText).localeCompare(getDisplayTitle(b, englishText));
          break;
        case "method":
          cmp = compareActivityMethods(classifyActivityMethod(a), classifyActivityMethod(b));
          break;
        case "node":
          cmp = routeSortValue(a).localeCompare(routeSortValue(b));
          break;
        case "started":
          cmp = new Date(a.started_at).getTime() - new Date(b.started_at).getTime();
          break;
      }
      return sortDir === "asc" ? cmp : -cmp;
    });
    // Search also matches the shown route text, which comes from t() inside
    // getSessionRouteNodes.
    // eslint-disable-next-line react-hooks/exhaustive-deps -- re-filter on a language change
  }, [sessions, search, methodFilter, nodeFilter, typeFilter, sortField, sortDir, t]);

  const toggleSort = (field: SortField) => {
    if (sortField === field) {
      setSortDir((d) => (d === "asc" ? "desc" : "asc"));
    } else {
      setSortField(field);
      setSortDir(field === "started" ? "desc" : "asc");
    }
  };

  const activeFilters = [methodFilter, nodeFilter, typeFilter].filter(Boolean).length;

  const runIPLookup = useCallback((ip: string) => {
    const trimmed = ip.trim();
    if (!trimmed) {
      return;
    }

    setIPSearch(trimmed);
    setActiveIP(trimmed);
    setIPLookupOpen(true);
    window.requestAnimationFrame(() => {
      ipLookupRef.current?.scrollIntoView({ behavior: "smooth", block: "nearest" });
    });
  }, []);

  const reloadHistoryButton = (
    <Button onClick={() => void ipHistory.restart()}>
      {t("web.admin.activity.ipLookup.reload")}
    </Button>
  );

  // Download preparation moved to the Downloads page; keep old links working.
  if (searchParams.get("view") === "preparations") {
    return <Navigate to="/admin/downloads?tab=preparation" replace />;
  }
  if (isLoading)
    return <div className="text-muted-foreground pb-8">{t("web.admin.activity.page.loading")}</div>;

  return (
    <div className="space-y-5 lg:space-y-6">
      <PageHeader
        title={t("web.admin.activity.page.title")}
        subtitle={
          sessions.length === 0
            ? t("web.admin.activity.page.noStreams")
            : t("web.admin.activity.page.subtitle", {
                count: sessions.length,
                count_text: sessions.length,
                nodes: routeNodes.length,
                nodes_text: routeNodes.length,
              })
        }
        actions={
          <>
            {sessions.length > 0 && (
              <span className="live-badge flex items-center gap-1.5">
                <Radio className="h-3 w-3" />
                {t("web.admin.activity.page.liveCount", {
                  count: sessions.length,
                  count_text: sessions.length,
                })}
              </span>
            )}
            <Button
              variant="outline"
              size="sm"
              className="min-w-[8.25rem] justify-center"
              onClick={() => void refreshActivity({ manual: true })}
              disabled={isManualRefreshPending}
              aria-busy={isManualRefreshPending}
            >
              <RefreshCw
                className={`h-3.5 w-3.5 ${isManualRefreshPending ? "animate-spin" : ""}`}
              />
              {isManualRefreshPending
                ? t("web.admin.activity.page.refreshing")
                : t("web.admin.activity.page.refresh")}
            </Button>
            <div className="min-w-[8.75rem] px-1 text-end">
              <div className="text-muted-foreground text-[10px] font-semibold tracking-wider uppercase">
                {t("web.admin.activity.realtime.heading")}
              </div>
              <div className="text-[12px]">{formatConnectionState(connectionState, t)}</div>
              {error && <div className="text-muted-foreground text-[11px]">{error}</div>}
            </div>
          </>
        }
      />

      {/* IP Lookup */}
      <details
        ref={ipLookupRef}
        open={ipLookupOpen}
        onToggle={(event) => setIPLookupOpen(event.currentTarget.open)}
        className="surface-panel rounded-2xl border-0"
      >
        <summary className="cursor-pointer px-4 py-3 text-sm font-medium select-none">
          {t("web.admin.activity.ipLookup.heading")}
        </summary>
        <div className="px-4 pb-4">
          <form
            onSubmit={(e) => {
              e.preventDefault();
              setActiveIP(ipSearch.trim());
            }}
            className="flex items-center gap-2"
          >
            <Input
              type="text"
              placeholder={t("web.admin.activity.ipLookup.placeholder")}
              value={ipSearch}
              onChange={(e) => setIPSearch(e.target.value)}
              className="max-w-xs font-mono text-sm"
            />
            <Button type="submit" variant="outline" size="sm" disabled={!ipSearch.trim()}>
              <Search className="me-1 h-3.5 w-3.5" />
              {t("web.admin.activity.ipLookup.submit")}
            </Button>
          </form>
          {activeIP && (
            <div className="mt-3">
              {ipLoading ? (
                <p className="text-muted-foreground text-sm">
                  {t("web.admin.activity.ipLookup.searching")}
                </p>
              ) : ipHistory.isError && ipUsers.length === 0 ? (
                <p role="alert">
                  {t("web.admin.activity.ipLookup.loadError")} {reloadHistoryButton}
                </p>
              ) : ipUsers.length === 0 ? (
                <p className="text-muted-foreground text-sm">
                  {t("web.admin.activity.ipLookup.empty", { ip: activeIP })}
                </p>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t("web.admin.activity.ipLookup.column.user")}</TableHead>
                      <TableHead>{t("web.admin.activity.ipLookup.column.firstSeen")}</TableHead>
                      <TableHead>{t("web.admin.activity.ipLookup.column.lastSeen")}</TableHead>
                      <TableHead className="text-end">
                        {t("web.admin.activity.ipLookup.column.requests")}
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {ipUsers.map((entry: IPUserEntry) => (
                      <TableRow key={entry.user_id}>
                        <TableCell>
                          <Link
                            to={`/admin/users/${entry.user_id}`}
                            className="text-primary font-medium hover:underline"
                          >
                            {entry.username ||
                              t("web.admin.activity.userFallback", { id: entry.user_id })}
                          </Link>
                        </TableCell>
                        <TableCell className="text-sm">
                          {formatDateTime(entry.first_seen)}
                        </TableCell>
                        <TableCell className="text-sm">{formatDateTime(entry.last_seen)}</TableCell>
                        <TableCell className="text-end">
                          {entry.request_count.toLocaleString(formatLocale())}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
              {ipHistory.isError && ipUsers.length > 0 && (
                <p role="alert">
                  {t("web.admin.activity.ipLookup.loadMoreError")} {reloadHistoryButton}
                </p>
              )}
              {ipHistory.hasNextPage && (
                <Button
                  disabled={ipHistory.isFetchingNextPage}
                  onClick={() => void ipHistory.fetchNextPage()}
                >
                  {t("web.admin.activity.ipLookup.loadMore")}
                </Button>
              )}
            </div>
          )}
        </div>
      </details>

      {/* Summary strip */}
      {sessions.length > 0 && (
        <div className="surface-panel rounded-2xl border-0 p-4">
          {/* Method distribution bar */}
          <div className="mb-3">
            <div className="text-muted-foreground mb-2 text-[10px] font-semibold tracking-wider uppercase">
              {t("web.admin.activity.summary.methodHeading")}
            </div>
            <div
              role="img"
              aria-label={t("web.admin.activity.summary.methodDistribution")}
              className="flex h-1.5 overflow-hidden rounded-full"
            >
              {Object.entries(methods)
                .sort(([a], [b]) => compareActivityMethods(a, b))
                .map(([method, count]) => (
                  <div
                    key={method}
                    title={t("web.admin.activity.summary.methodSegment", {
                      method: activityMethodMeta(method).label,
                      count: count,
                    })}
                    className={`transition-all duration-500 ${activityMethodMeta(method).swatchClass}`}
                    style={{ width: `${(count / sessions.length) * 100}%` }}
                  />
                ))}
            </div>
            <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1">
              {Object.entries(methods)
                .sort(([a], [b]) => compareActivityMethods(a, b))
                .map(([method, count]) => (
                  <button
                    key={method}
                    type="button"
                    title={activityMethodMeta(method).description}
                    aria-label={t("web.admin.activity.summary.methodFilter", {
                      method: activityMethodMeta(method).label,
                      count: count,
                    })}
                    aria-pressed={methodFilter === method}
                    onClick={() => setMethodFilter(methodFilter === method ? null : method)}
                    className={`flex items-center gap-1.5 text-[11px] transition-opacity ${
                      methodFilter && methodFilter !== method ? "opacity-30" : ""
                    }`}
                  >
                    <span
                      className={`inline-block h-2 w-2 rounded-full ${activityMethodMeta(method).swatchClass}`}
                    />
                    <span className="font-medium">{activityMethodMeta(method).label}</span>
                    <span className="text-muted-foreground tabular-nums">{count}</span>
                  </button>
                ))}
            </div>
          </div>

          {/* Node breakdown */}
          {routeNodes.length > 1 && (
            <div className="border-border border-t pt-3">
              <div className="text-muted-foreground mb-2 text-[10px] font-semibold tracking-wider uppercase">
                {t("web.admin.activity.summary.byNode")}
              </div>
              <div className="flex flex-wrap gap-1.5">
                {[...routeNodes]
                  .sort((a, b) => b.count - a.count)
                  .map((node) => (
                    <button
                      key={node.key}
                      onClick={() => setNodeFilter(nodeFilter === node.key ? null : node.key)}
                      className={`bg-surface border-border hover:border-primary/20 rounded-md border px-2.5 py-1 text-[11px] font-medium transition-all ${
                        nodeFilter === node.key
                          ? "border-primary/40 bg-primary/10 text-primary"
                          : nodeFilter
                            ? "opacity-30"
                            : ""
                      }`}
                    >
                      <span className="text-muted-foreground me-1">{node.label}</span>
                      {node.name}
                      <span className="text-muted-foreground ms-1.5 tabular-nums">
                        {node.count}
                      </span>
                    </button>
                  ))}
              </div>
            </div>
          )}
        </div>
      )}

      {/* Search + filters */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-[200px] flex-1">
          <Search className="text-muted-foreground pointer-events-none absolute start-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2" />
          <Input
            placeholder={t("web.admin.activity.filter.placeholder")}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="h-8 ps-9 text-[13px]"
          />
          {search && (
            <button
              onClick={() => setSearch("")}
              className="text-muted-foreground hover:text-foreground absolute end-2.5 top-1/2 -translate-y-1/2"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          )}
        </div>
        <div className="flex gap-1">
          {(["movie", "series"] as const).map((type) => (
            <button
              key={type}
              onClick={() => setTypeFilter(typeFilter === type ? null : type)}
              className={`rounded-md border px-2.5 py-1.5 text-[11px] font-medium capitalize transition-all ${
                typeFilter === type
                  ? "border-primary/40 bg-primary/10 text-primary"
                  : "border-border bg-surface text-muted-foreground hover:text-foreground"
              }`}
            >
              {type === "movie"
                ? t("web.admin.activity.filter.movies")
                : t("web.admin.activity.filter.series")}
            </button>
          ))}
        </div>
        {activeFilters > 0 && (
          <button
            onClick={() => {
              setMethodFilter(null);
              setNodeFilter(null);
              setTypeFilter(null);
            }}
            className="text-muted-foreground hover:text-foreground flex items-center gap-1 text-[11px]"
          >
            <X className="h-3 w-3" />
            {t("web.admin.activity.filter.clear")}
          </button>
        )}
      </div>

      {/* Filter status */}
      {(search || activeFilters > 0) && (
        <div className="text-muted-foreground text-[11px]">
          {t("web.admin.activity.filter.showing", {
            total: sessions.length,
            shown: filtered.length,
            total_text: sessions.length,
          })}
        </div>
      )}

      {/* Stream table */}
      {filtered.length === 0 ? (
        <EmptyState hasData={sessions.length > 0} />
      ) : (
        <div className="bg-card border-border @container overflow-hidden rounded-lg border">
          {/* Sort controls for the stacked rows, which have no column headers */}
          <div
            role="group"
            aria-label={t("web.admin.activity.sort.group")}
            className="border-border bg-surface/50 flex flex-wrap items-center gap-x-4 gap-y-1.5 border-b px-4 py-2.5 @min-[calc(977px+4rem)]:hidden"
          >
            <span className="text-muted-foreground/70 text-[10px] font-semibold tracking-wider uppercase">
              {t("web.admin.activity.sort.heading")}
            </span>
            {STACKED_SORT_FIELDS.map(([field, label]) => (
              <SortHeader
                key={field}
                field={field}
                current={sortField}
                dir={sortDir}
                onClick={toggleSort}
              >
                {t(label)}
              </SortHeader>
            ))}
          </div>

          {/* Table header */}
          <div
            className={`border-border bg-surface/50 hidden ${STREAM_TABLE_COLUMNS} items-center gap-2 border-b px-3 py-2.5 @min-[calc(977px+4rem)]:grid`}
          >
            <SortHeader field="username" current={sortField} dir={sortDir} onClick={toggleSort}>
              {t("web.admin.activity.column.user")}
            </SortHeader>
            <SortHeader field="media" current={sortField} dir={sortDir} onClick={toggleSort}>
              {t("web.admin.activity.column.stream")}
            </SortHeader>
            <SortHeader field="method" current={sortField} dir={sortDir} onClick={toggleSort}>
              {t("web.admin.activity.column.playback")}
            </SortHeader>
            <SortHeader field="node" current={sortField} dir={sortDir} onClick={toggleSort}>
              {t("web.admin.activity.column.route")}
            </SortHeader>
            <SortHeader
              field="started"
              current={sortField}
              dir={sortDir}
              onClick={toggleSort}
              className="justify-end"
            >
              {t("web.admin.activity.column.time")}
            </SortHeader>
            <div className="text-muted-foreground text-end text-[10px] font-semibold tracking-wider uppercase">
              {t("web.admin.activity.column.actions")}
            </div>
          </div>

          {/* Rows */}
          <div className="max-h-[calc(100vh-420px)] overflow-y-auto">
            {filtered.map((session, i) => (
              <StreamRow
                key={session.session_id}
                session={session}
                even={i % 2 === 0}
                onIPLookup={runIPLookup}
              />
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

// --- Sub-components ---

function StreamRow({
  session,
  even,
  onIPLookup,
}: {
  session: AdminSession;
  even: boolean;
  onIPLookup: (ip: string) => void;
}) {
  const t = useT();
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [ffmpegOpen, setFFmpegOpen] = useState(false);
  const title = getDisplayTitle(session, t);
  const subtitle = getDisplaySubtitle(session, t);
  const username =
    session.username || t("web.admin.activity.userFallback", { id: session.user_id });
  const elapsed = getElapsed(session.started_at);
  const profileDisplay = session.profile_name || session.profile_id || "";
  const userHref = `/admin/users/${session.user_id}`;
  const itemHref = session.content_id ? `/item/${session.content_id}` : "";
  const sourceContainer = session.source_container?.trim().toUpperCase();
  const streamBitrate = formatSessionBitrate(session.stream_bitrate_kbps);
  const streamMeta = [sourceContainer, streamBitrate].filter(Boolean).join(" · ");
  const clientIP = session.client_ip?.trim() || "";
  const clientLabel = getSessionClientLabel(session);
  // The row stays compact; the exact version, build, and channel live in the
  // tooltip and in the expanded panel's Client card.
  const clientLabelFull = getSessionClientLabelFull(session);
  const clientUserAgent = session.client_user_agent?.trim() || "";
  const clientTitle = clientUserAgent
    ? `${clientLabelFull || clientLabel} — ${clientUserAgent}`
    : clientLabelFull || clientLabel;
  const playbackPosition = formatPlaybackPosition(session);
  const transcodeMode = formatTranscodeModeSummary(session);
  const toneMap = formatToneMapSummary(session);
  const activityMethod = classifyActivityMethod(session);
  const containerDecision = normalizeContainerDecision(session.play_method);
  const videoDecision = normalizeStreamDecision(session.video_decision || session.play_method);
  const audioDecision = normalizeStreamDecision(
    session.audio_decision || (session.transcode_audio ? "transcode" : session.play_method),
  );
  const logsHref = `/admin/logs?playback_session_id=${encodeURIComponent(session.session_id)}&focus=playback`;
  const ffmpegLogsHref = `${logsHref}&component=ffmpeg`;
  const ffmpegLogs = useOperationalLogs(
    {
      playback_session_id: session.session_id,
      component: "ffmpeg",
      limit: 12,
    },
    ffmpegOpen,
  );
  const ffmpegRows = ffmpegLogs.data?.entries ?? [];
  const expandedOpen = detailsOpen || ffmpegOpen;

  const toggleDetails = () => {
    setDetailsOpen((open) => {
      const nextOpen = !open;
      if (!nextOpen) {
        setFFmpegOpen(false);
      }
      return nextOpen;
    });
  };

  const toggleFFmpeg = () => {
    setFFmpegOpen((open) => {
      const nextOpen = !open;
      if (nextOpen) {
        setDetailsOpen(true);
      }
      return nextOpen;
    });
  };

  return (
    <div
      className={`border-border/30 hover:bg-surface/60 border-b transition-colors duration-100 ${
        even ? "" : "bg-surface/20"
      }`}
    >
      {/* Desktop row */}
      <div
        className={`hidden ${STREAM_TABLE_COLUMNS} items-center gap-2 px-3 py-2.5 @min-[calc(977px+4rem)]:grid`}
      >
        {/* User */}
        <div className="flex min-w-0 items-center gap-2">
          <Link to={userHref} className="hover:text-primary shrink-0 transition-colors">
            <div
              className="text-primary-foreground flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full text-[9px] font-bold"
              style={{ background: "var(--primary)" }}
            >
              {username.charAt(0).toUpperCase()}
            </div>
          </Link>
          <div className="min-w-0">
            <Link to={userHref} className="hover:text-primary block truncate transition-colors">
              <span className="text-[13px] font-medium">{username}</span>
            </Link>
            {profileDisplay ? (
              <div className="mt-0.5 flex min-w-0 items-center gap-1.5">
                <span className="border-primary/30 bg-primary/15 text-primary max-w-full truncate rounded border px-1.5 py-0.5 text-[10px] leading-none">
                  {profileDisplay}
                </span>
              </div>
            ) : null}
            {(clientLabel || clientIP || isJellyfinSession(session)) && (
              <div className="text-muted-foreground mt-1 flex min-w-0 items-center gap-1.5 text-[10px]">
                <JellyfinSessionPill session={session} />
                {clientLabel ? (
                  <span title={clientTitle} className="max-w-[8rem] min-w-0 truncate">
                    {clientLabel}
                  </span>
                ) : null}
                {clientLabel && clientIP ? <span className="shrink-0">{MIDDLE_DOT}</span> : null}
                {clientIP ? (
                  <button
                    type="button"
                    onClick={() => onIPLookup(clientIP)}
                    className="hover:text-primary min-w-0 cursor-pointer truncate transition-colors"
                  >
                    {clientIP}
                  </button>
                ) : null}
              </div>
            )}
          </div>
        </div>

        {/* Stream */}
        <div className="min-w-0">
          {itemHref ? (
            <Link to={itemHref} className="hover:text-primary block min-w-0 transition-colors">
              <div className="truncate text-[13px] font-medium">{title}</div>
              {subtitle && (
                <div className="text-muted-foreground hover:text-primary truncate text-[10px] transition-colors">
                  {subtitle}
                </div>
              )}
              {streamMeta && (
                <div className="text-muted-foreground hover:text-primary truncate text-[10px] transition-colors">
                  {streamMeta}
                </div>
              )}
            </Link>
          ) : (
            <>
              <div className="truncate text-[13px] font-medium">{title}</div>
              {subtitle && (
                <div className="text-muted-foreground truncate text-[10px]">{subtitle}</div>
              )}
              {streamMeta && (
                <div className="text-muted-foreground truncate text-[10px]">{streamMeta}</div>
              )}
            </>
          )}
        </div>

        {/* Playback */}
        <div className="min-w-0">
          <div className="flex min-w-0 flex-wrap items-center gap-1.5">
            <ActivityMethodBadge method={activityMethod} />
            <button
              type="button"
              onClick={toggleDetails}
              aria-expanded={expandedOpen}
              className="text-muted-foreground hover:text-primary inline-flex items-center gap-0.5 text-[10px] font-medium transition-colors"
            >
              {t("web.admin.activity.row.details")}
              {expandedOpen ? (
                <ChevronUp className="h-3 w-3" />
              ) : (
                <ChevronDown className="h-3 w-3" />
              )}
            </button>
          </div>
          <div className="mt-1.5 space-y-1">
            <PlaybackSummaryLine
              label={t("web.admin.activity.row.container")}
              decision={containerDecision}
              value={formatDeliveredContainerSummary(session)}
            />
            <PlaybackSummaryLine
              label={t("web.admin.activity.row.video")}
              decision={videoDecision}
              value={formatDeliveredVideoSummary(session)}
            />
            <PlaybackSummaryLine
              label={t("web.admin.activity.row.audio")}
              decision={audioDecision}
              value={formatDeliveredAudioSummary(session)}
            />
          </div>
        </div>

        {/* Node */}
        <div className="min-w-0">
          <div className="flex min-w-0 flex-wrap gap-1">
            <PlaybackRouteBadges session={session} />
          </div>
        </div>

        {/* Duration */}
        <div className="min-w-0 text-end">
          <div className="flex items-center justify-end">
            <span
              className={`inline-flex min-w-[4.75rem] items-center justify-center gap-1 rounded border px-1.5 py-0.5 text-[9px] font-semibold ${
                session.is_paused
                  ? "border-amber-400/35 bg-amber-400/10 text-amber-300"
                  : "border-emerald-400/35 bg-emerald-400/10 text-emerald-300"
              }`}
            >
              {session.is_paused ? (
                <Pause className="h-2.5 w-2.5" />
              ) : (
                <Play className="h-2.5 w-2.5" />
              )}
              {session.is_paused
                ? t("web.admin.activity.row.paused")
                : t("web.admin.activity.row.playing")}
            </span>
          </div>
          <div className="text-foreground mt-1 font-mono text-[12px] leading-tight tabular-nums">
            {playbackPosition}
          </div>
          <div className="text-muted-foreground mt-0.5 text-[10px] leading-tight tabular-nums">
            {t("web.admin.activity.row.sessionActive", { elapsed })}
          </div>
        </div>

        {/* Actions */}
        <div className="min-w-0">
          <AdminSessionActions
            session={session}
            compact
            layout="inline"
            showInlineTerminate
            inlinePrefixActions={
              <Button
                variant="outline"
                size="sm"
                className="h-7 gap-1.5 px-2 text-[11px]"
                onClick={toggleFFmpeg}
              >
                <Terminal className="h-3.5 w-3.5" />
                {ffmpegOpen
                  ? t("web.admin.activity.row.hideFfmpeg")
                  : t("web.admin.activity.row.ffmpeg")}
              </Button>
            }
            extraMenuItems={
              <>
                <DropdownMenuItem asChild>
                  <Link to={logsHref}>
                    <FileText className="h-4 w-4" />
                    {t("web.admin.activity.row.viewLogs")}
                  </Link>
                </DropdownMenuItem>
                <DropdownMenuItem asChild>
                  <Link to={ffmpegLogsHref}>
                    <Terminal className="h-4 w-4" />
                    {t("web.admin.activity.row.ffmpegLogs")}
                  </Link>
                </DropdownMenuItem>
              </>
            }
          />
        </div>
      </div>

      {/* Mobile row */}
      <div className="flex gap-3 px-4 py-3 @min-[calc(977px+4rem)]:hidden">
        <Link
          to={userHref}
          className="text-primary-foreground flex h-7 w-7 flex-shrink-0 items-center justify-center rounded-full text-[10px] font-bold"
          style={{ background: "var(--primary)" }}
        >
          {username.charAt(0).toUpperCase()}
        </Link>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <Link to={userHref} className="hover:text-primary truncate transition-colors">
              <span className="text-[13px] font-semibold">{username}</span>
              {profileDisplay ? (
                <span className="border-primary/30 bg-primary/15 text-primary ms-1.5 rounded border px-1.5 py-0.5 text-[10px] leading-none">
                  {profileDisplay}
                </span>
              ) : null}
            </Link>
            <JellyfinSessionPill session={session} />
          </div>
          <div className="text-muted-foreground mt-0.5 flex items-center gap-1.5 text-[11px]">
            {itemHref ? (
              <Link to={itemHref} className="hover:text-primary truncate transition-colors">
                {title}
              </Link>
            ) : (
              <span className="truncate">{title}</span>
            )}
            <span className="flex-shrink-0">{MIDDLE_DOT}</span>
            <span className="flex-shrink-0 font-mono tabular-nums">{elapsed}</span>
          </div>
          {(clientLabel || clientIP || streamMeta) && (
            <div className="text-muted-foreground mt-1 flex min-w-0 gap-1.5 text-[10px]">
              {clientLabel ? (
                <span title={clientTitle} className="max-w-[9rem] shrink-0 truncate">
                  {clientLabel}
                </span>
              ) : null}
              {clientLabel && (clientIP || streamMeta) ? (
                <span className="shrink-0">{MIDDLE_DOT}</span>
              ) : null}
              {clientIP ? (
                <button
                  type="button"
                  onClick={() => onIPLookup(clientIP)}
                  className="hover:text-primary shrink-0 cursor-pointer transition-colors"
                >
                  {clientIP}
                </button>
              ) : null}
              {clientIP && streamMeta ? <span className="shrink-0">{MIDDLE_DOT}</span> : null}
              {streamMeta ? (
                itemHref ? (
                  <Link to={itemHref} className="hover:text-primary min-w-0 truncate">
                    {streamMeta}
                  </Link>
                ) : (
                  <span className="min-w-0 truncate">{streamMeta}</span>
                )
              ) : null}
            </div>
          )}
          <div className="text-muted-foreground mt-1 flex items-center gap-2 text-[10px]">
            <span
              className={`inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[9px] font-semibold ${
                session.is_paused
                  ? "border-amber-400/35 bg-amber-400/10 text-amber-300"
                  : "border-emerald-400/35 bg-emerald-400/10 text-emerald-300"
              }`}
            >
              {session.is_paused ? (
                <Pause className="h-2.5 w-2.5" />
              ) : (
                <Play className="h-2.5 w-2.5" />
              )}
              {session.is_paused
                ? t("web.admin.activity.row.paused")
                : t("web.admin.activity.row.playing")}
            </span>
            <span className="font-mono tabular-nums">{playbackPosition}</span>
          </div>
          <div className="mt-1.5 flex min-w-0 flex-wrap gap-1">
            <PlaybackRouteBadges session={session} />
          </div>
          <div className="mt-2 rounded-md border border-white/6 bg-white/[0.03] px-2 py-1.5">
            <div className="flex min-w-0 flex-wrap items-center gap-1.5">
              <ActivityMethodBadge method={activityMethod} />
              <button
                type="button"
                onClick={toggleDetails}
                aria-expanded={expandedOpen}
                className="text-muted-foreground hover:text-primary inline-flex items-center gap-0.5 text-[10px] font-medium transition-colors"
              >
                {t("web.admin.activity.row.details")}
                {expandedOpen ? (
                  <ChevronUp className="h-3 w-3" />
                ) : (
                  <ChevronDown className="h-3 w-3" />
                )}
              </button>
            </div>
            <div className="mt-1.5 space-y-1">
              <PlaybackSummaryLine
                label={t("web.admin.activity.row.container")}
                decision={containerDecision}
                value={formatDeliveredContainerSummary(session)}
              />
              <PlaybackSummaryLine
                label={t("web.admin.activity.row.video")}
                decision={videoDecision}
                value={formatDeliveredVideoSummary(session)}
              />
              <PlaybackSummaryLine
                label={t("web.admin.activity.row.audio")}
                decision={audioDecision}
                value={formatDeliveredAudioSummary(session)}
              />
            </div>
          </div>
          <div className="mt-2 flex items-center justify-end">
            <AdminSessionActions
              session={session}
              compact
              layout="inline"
              showInlineTerminate
              inlinePrefixActions={
                <Button
                  variant="outline"
                  size="sm"
                  className="h-7 gap-1.5 px-2 text-[11px]"
                  onClick={toggleFFmpeg}
                >
                  <Terminal className="h-3.5 w-3.5" />
                  {ffmpegOpen
                    ? t("web.admin.activity.row.hideFfmpeg")
                    : t("web.admin.activity.row.ffmpeg")}
                </Button>
              }
              extraMenuItems={
                <>
                  <DropdownMenuItem asChild>
                    <Link to={logsHref}>
                      <FileText className="h-4 w-4" />
                      {t("web.admin.activity.row.viewLogs")}
                    </Link>
                  </DropdownMenuItem>
                  <DropdownMenuItem asChild>
                    <Link to={ffmpegLogsHref}>
                      <Terminal className="h-4 w-4" />
                      {t("web.admin.activity.row.ffmpegLogs")}
                    </Link>
                  </DropdownMenuItem>
                </>
              }
            />
          </div>
        </div>
      </div>

      {expandedOpen && (
        <PlaybackExpandedPanel
          session={session}
          sessionID={session.session_id}
          containerDecision={containerDecision}
          videoDecision={videoDecision}
          audioDecision={audioDecision}
          transcodeMode={transcodeMode}
          toneMapping={toneMap?.detail ?? null}
          showFFmpeg={ffmpegOpen}
          rows={ffmpegRows}
          isLoading={ffmpegLogs.isLoading}
          isFetching={ffmpegLogs.isFetching}
          logsHref={`${logsHref}&component=ffmpeg`}
        />
      )}
    </div>
  );
}

function PlaybackSummaryLine({
  label,
  decision,
  value,
}: {
  label: string;
  decision: string;
  value: string;
}) {
  return (
    <div className="flex min-w-0 items-center gap-1.5 text-[11px] leading-tight">
      <span className="text-muted-foreground w-14 shrink-0 text-[9px] font-semibold tracking-wide uppercase">
        {label}
      </span>
      <span
        className={`inline-flex shrink-0 rounded border px-1.5 py-0.5 text-[8px] leading-none font-semibold ${decisionBadgeClass(decision)}`}
      >
        {formatDecisionLabel(decision)}
      </span>
      <span className="truncate font-medium">{value}</span>
    </div>
  );
}

function ActivityMethodBadge({ method }: { method: string }) {
  const t = useT();
  const meta = activityMethodMeta(method);
  return (
    <span
      aria-label={t("web.admin.activity.row.methodBadge", { method: meta.label })}
      title={meta.description}
      className={`inline-flex shrink-0 rounded border px-1.5 py-0.5 text-[9px] font-semibold ${meta.badgeClass}`}
    >
      {meta.label}
    </span>
  );
}

function PlaybackExpandedPanel({
  session,
  sessionID,
  containerDecision,
  videoDecision,
  audioDecision,
  transcodeMode,
  toneMapping,
  showFFmpeg,
  rows,
  isLoading,
  isFetching,
  logsHref,
}: {
  session: AdminSession;
  sessionID: string;
  containerDecision: string;
  videoDecision: string;
  audioDecision: string;
  transcodeMode: string | null;
  toneMapping: string | null;
  showFFmpeg: boolean;
  rows: OperationalLogEntry[];
  isLoading: boolean;
  isFetching: boolean;
  logsHref: string;
}) {
  const t = useT();
  return (
    <div className="terminal-surface border-border/50 bg-card border-t px-4 py-3">
      <div className="mb-2 flex items-center justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <div className="rounded-full border border-[var(--terminal-border)] bg-[var(--terminal-bg)] px-2 py-0.5 text-[10px] font-semibold tracking-[0.2em] text-[var(--terminal-fg)] uppercase">
              {t("web.admin.activity.panel.heading")}
            </div>
            <div className="text-foreground/85 text-[11px] font-medium">
              {showFFmpeg
                ? t("web.admin.activity.panel.streamDetailsWithConsole")
                : t("web.admin.activity.panel.streamDetails")}
            </div>
          </div>
          <div className="text-muted-foreground mt-1 font-mono text-[10px]">{sessionID}</div>
        </div>
        {showFFmpeg && isFetching ? (
          <div className="text-muted-foreground text-[10px]">
            {t("web.admin.activity.panel.refreshing")}
          </div>
        ) : null}
      </div>

      <div className="mb-3 grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
        <PlaybackDetailCard
          label={t("web.admin.activity.row.container")}
          decision={containerDecision}
          source={formatSourceContainerSummary(session)}
          delivered={formatDeliveredContainerSummary(session)}
          detail={formatContainerDetail(session)}
        />
        <PlaybackDetailCard
          label={t("web.admin.activity.row.video")}
          decision={videoDecision}
          source={formatVideoSummary(session)}
          delivered={formatDeliveredVideoSummary(session)}
          detail={formatVideoDetail(session)}
          mode={videoDecision === "transcode" ? transcodeMode : null}
          toneMapping={videoDecision === "transcode" ? toneMapping : null}
        />
        <PlaybackDetailCard
          label={t("web.admin.activity.row.audio")}
          decision={audioDecision}
          source={formatAudioSummary(session)}
          delivered={formatDeliveredAudioSummary(session)}
          detail={formatAudioDetail(session)}
          mode={
            audioDecision === "transcode" && videoDecision !== "transcode" ? transcodeMode : null
          }
        />
        <PlaybackClientCard session={session} />
      </div>

      {showFFmpeg ? (
        <div className="space-y-2">
          <div className="flex justify-end">
            <Link
              to={logsHref}
              className="text-[11px] font-medium text-[var(--terminal-fg)] hover:text-[var(--terminal-fg)]/80"
            >
              {t("web.admin.activity.panel.openFfmpegLogs")}
            </Link>
          </div>
          <div className="overflow-hidden rounded-xl border border-[var(--terminal-border)] bg-[var(--terminal-bg)] shadow-[0_18px_60px_rgba(0,0,0,0.35)]">
            {isLoading ? (
              <div className="px-4 py-6 font-mono text-[11px] text-[var(--terminal-muted)]">
                {t("web.admin.activity.panel.loadingFfmpeg")}
              </div>
            ) : rows.length === 0 ? (
              <div className="px-4 py-6 font-mono text-[11px] text-[var(--terminal-muted)]">
                {t("web.admin.activity.panel.noFfmpegRows")}
              </div>
            ) : (
              <div className="max-h-64 overflow-y-auto">
                {rows.map((row) => (
                  <div
                    key={row.id}
                    className="grid grid-cols-[120px_1fr] gap-3 border-b border-[var(--terminal-border)]/30 px-4 py-2.5 last:border-b-0"
                  >
                    <div className="space-y-1">
                      <div className="font-mono text-[10px] text-[var(--terminal-muted)]">
                        {formatTimeOnly(row.timestamp)}
                      </div>
                      <div className="text-[10px] tracking-[0.18em] text-[var(--terminal-muted)]/60 uppercase">
                        {row.message.includes("stderr")
                          ? t("web.admin.activity.panel.stderr")
                          : t("web.admin.activity.panel.event")}
                      </div>
                    </div>
                    <div className="min-w-0">
                      <div className="font-mono text-[11px] leading-5 break-words text-[var(--terminal-fg)]">
                        {ffmpegRowText(row)}
                      </div>
                      <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-[10px] text-[var(--terminal-muted)]/60">
                        {row.node_id && <span>{row.node_id}</span>}
                        {stringAttr(row, "target_resolution") !== "-" && (
                          <span>{stringAttr(row, "target_resolution")}</span>
                        )}
                        {stringAttr(row, "hw_accel") !== "-" && (
                          <span>{stringAttr(row, "hw_accel")}</span>
                        )}
                        {stringAttr(row, "restart_count") !== "-" && (
                          <span>
                            {t("web.admin.activity.panel.restart", {
                              count: stringAttr(row, "restart_count"),
                            })}
                          </span>
                        )}
                      </div>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      ) : null}
    </div>
  );
}

function PlaybackDetailCard({
  label,
  decision,
  source,
  delivered,
  detail,
  mode,
  toneMapping,
}: {
  label: string;
  decision: string;
  source: string;
  delivered: string;
  detail: string;
  mode?: string | null;
  toneMapping?: string | null;
}) {
  const t = useT();
  return (
    <PlaybackDetailCardShell
      label={label}
      badge={
        <span
          className={`inline-flex rounded border px-1.5 py-0.5 text-[9px] font-semibold ${decisionBadgeClass(decision)}`}
        >
          {formatDecisionLabel(decision)}
        </span>
      }
    >
      <PlaybackDetailLine label={t("web.admin.activity.detail.source")} value={source} />
      <PlaybackDetailLine label={t("web.admin.activity.detail.delivered")} value={delivered} />
      {mode ? (
        <PlaybackDetailLine label={t("web.admin.activity.detail.mode")} value={mode} />
      ) : null}
      {toneMapping ? (
        <PlaybackDetailLine
          label={t("web.admin.activity.detail.toneMapping")}
          value={toneMapping}
        />
      ) : null}
      <PlaybackDetailLine label={t("web.admin.activity.detail.detail")} value={detail} muted />
    </PlaybackDetailCardShell>
  );
}

/**
 * The exact streaming client: app name, version, build, and channel, with the
 * raw user agent underneath. This is the surface that answers "which build is
 * this?" — the session rows only have room for the compact label.
 */
function PlaybackClientCard({ session }: { session: AdminSession }) {
  const t = useT();
  const label = getSessionClientLabelFull(session) || t("web.admin.activity.client.unknown");
  const userAgent = session.client_user_agent?.trim() || "";
  return (
    <PlaybackDetailCardShell label={t("web.admin.activity.client.heading")}>
      <PlaybackDetailLine label={t("web.admin.activity.client.app")} value={label} />
      {userAgent ? (
        <PlaybackDetailLine label={t("web.admin.activity.client.agent")} value={userAgent} muted />
      ) : null}
    </PlaybackDetailCardShell>
  );
}

function PlaybackDetailCardShell({
  label,
  badge,
  children,
}: {
  label: string;
  badge?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="rounded-lg border border-[var(--terminal-border)]/60 bg-[var(--terminal-bg)]/60 px-3 py-2">
      <div className="mb-2 flex items-center gap-2">
        <span className="text-[10px] font-semibold tracking-[0.18em] text-[var(--terminal-muted)] uppercase">
          {label}
        </span>
        {badge}
      </div>
      <div className="grid gap-1 text-[11px]">{children}</div>
    </div>
  );
}

function PlaybackDetailLine({
  label,
  value,
  muted = false,
}: {
  label: string;
  value: string;
  muted?: boolean;
}) {
  return (
    <div className="grid min-w-0 grid-cols-[4.25rem_1fr] gap-2">
      <span className="text-[10px] text-[var(--terminal-muted)]">{label}</span>
      <span
        className={`min-w-0 font-medium break-words ${
          muted ? "text-[var(--terminal-muted)]" : "text-[var(--terminal-fg)]"
        }`}
      >
        {value}
      </span>
    </div>
  );
}

function SortHeader({
  field,
  current,
  dir,
  onClick,
  children,
  className = "",
}: {
  field: SortField;
  current: SortField;
  dir: SortDir;
  onClick: (field: SortField) => void;
  children: ReactNode;
  className?: string;
}) {
  const active = current === field;
  return (
    <button
      onClick={() => onClick(field)}
      className={`text-muted-foreground flex items-center gap-1 text-[10px] font-semibold tracking-wider uppercase transition-colors ${
        active ? "text-foreground" : "hover:text-foreground"
      } ${className}`}
    >
      {children}
      {active && (
        <span className="text-[8px]">{dir === "asc" ? SORT_ASC_MARK : SORT_DESC_MARK}</span>
      )}
    </button>
  );
}

function formatConnectionState(state: "connecting" | "live" | "disconnected", t: Translate) {
  switch (state) {
    case "connecting":
      return t("web.admin.activity.realtime.connecting");
    case "live":
      return t("web.admin.activity.realtime.live");
    default:
      return t("web.admin.activity.realtime.disconnected");
  }
}

function delay(ms: number) {
  return new Promise<void>((resolve) => {
    window.setTimeout(resolve, ms);
  });
}

function ffmpegRowText(entry: OperationalLogEntry) {
  const line = stringAttr(entry, "ffmpeg_line");
  if (line !== "-") return line;
  const event = stringAttr(entry, "ffmpeg_event");
  if (event !== "-") return event;
  return entry.message;
}

function formatTimeOnly(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return formatTime(date, { second: "2-digit" });
}

function EmptyState({ hasData }: { hasData: boolean }) {
  const t = useT();
  return (
    <div className="text-muted-foreground flex flex-col items-center justify-center py-20 text-sm">
      {hasData ? (
        <>
          <Filter className="mb-3 h-8 w-8 opacity-20" />
          <span>{t("web.admin.activity.empty.noMatches")}</span>
        </>
      ) : (
        <>
          <Play className="mb-3 h-8 w-8 opacity-20" />
          <span>{t("web.admin.activity.page.noStreams")}</span>
        </>
      )}
    </div>
  );
}

// --- Helpers ---

function getDisplayTitle(
  session: AdminSession,
  t: (id: MessageKey, values?: TextValues) => string,
): string {
  if (session.series_name && session.season_number != null && session.episode_number != null) {
    return session.episode_name || `S${session.season_number}E${session.episode_number}`;
  }
  return session.media_title || t("web.common.activity.title.file", { id: session.media_file_id });
}

function getDisplaySubtitle(session: AdminSession, t: Translate): string | null {
  if (session.series_name && session.season_number != null && session.episode_number != null) {
    const ep = `S${session.season_number}E${session.episode_number}`;
    return session.series_name ? `${ep} · ${session.series_name}` : ep;
  }
  if (session.media_type === "movie") return t("web.common.activity.subtitle.movie");
  if (session.media_type === "series") return t("web.common.activity.subtitle.series");
  return null;
}

function getElapsed(dateStr: string): string {
  const diff = Math.max(0, Date.now() - new Date(dateStr).getTime());
  const totalSec = Math.floor(diff / 1000);
  const h = Math.floor(totalSec / 3600);
  const m = Math.floor((totalSec % 3600) / 60);
  const s = totalSec % 60;
  if (h > 0) return numberText(`${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`);
  return numberText(`${m}:${String(s).padStart(2, "0")}`);
}

function formatPlaybackPosition(session: AdminSession): string {
  const position = formatClockTime(session.position_seconds);
  if (session.file_duration == null || session.file_duration <= 0) {
    return position;
  }
  return `${position} / ${formatClockTime(session.file_duration)}`;
}

function formatClockTime(seconds: number): string {
  const safeSeconds = Math.max(0, Math.floor(Number.isFinite(seconds) ? seconds : 0));
  const h = Math.floor(safeSeconds / 3600);
  const m = Math.floor((safeSeconds % 3600) / 60);
  const s = safeSeconds % 60;
  if (h > 0) {
    return numberText(`${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`);
  }
  return numberText(`${m}:${String(s).padStart(2, "0")}`);
}

function stringAttr(entry: OperationalLogEntry, key: string) {
  const value = entry.attrs?.[key];
  if (typeof value === "string" && value.length > 0) return value;
  if (typeof value === "number") return String(value);
  return "-";
}
