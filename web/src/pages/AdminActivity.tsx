import type { MouseEvent as ReactMouseEvent, ReactNode } from "react";
import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
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
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import type { AdminSession, OperationalLogEntry, IPUserEntry } from "@/api/types";
import { useIPUsers } from "@/hooks/queries/admin/ips";
import { useAdminSessions } from "@/hooks/queries/admin/stats";
import {
  activityMethodMeta,
  classifyActivityMethod,
  compareActivityMethods,
  decisionBadgeClass,
  formatAudioSummary,
  formatDecisionLabel,
  formatDeliveredAudioSummary,
  formatDeliveredContainerSummary,
  formatDeliveredVideoSummary,
  formatPlaybackPosition,
  formatRequestedSourceSwitch,
  formatSessionBitrate,
  formatSourceContainerSummary,
  formatToneMapSummary,
  formatTranscodeModeSummary,
  formatVideoSummary,
  getAudioConversion,
  getPlaybackProgress,
  getPlaybackSessionSubtitle,
  getSessionBitrate,
  getSessionClientLabel,
  getSessionClientLabelFull,
  getSessionRouteNodes,
  getVideoConversion,
  isEpisodeSession,
  normalizeContainerDecision,
  normalizeStreamDecision,
  sessionRoutesVary,
  summarizeActivity,
  type ActivityRouteNode,
  type StreamConversion,
} from "@/pages/adminActivityPresentation";
import { getAdaptationReasons, type AdaptationReason } from "@/pages/adminActivityReasons";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  AudioLines,
  ChevronDown,
  ChevronRight,
  Clock,
  FileText,
  Film,
  Filter,
  Globe,
  Pause,
  Play,
  RefreshCw,
  Search,
  Terminal,
  X,
} from "lucide-react";
import { formatDateTime, formatTime } from "@/lib/datetime";
import { formatLocale, joinList } from "@/lib/appLocale";
import { englishText, numberText, type TextValues, type Translate, useT } from "@/i18n";
import type { MessageKey } from "@/locales/keys";

type SortField = "username" | "media" | "method" | "bitrate" | "node" | "started";
type SortDir = "asc" | "desc";

const REFRESH_SPINNER_MIN_VISIBLE_MS = 1_000;

const MIDDLE_DOT = "·";
const SORT_ASC_MARK = "▲";
const SORT_DESC_MARK = "▼";

/**
 * Links inside a row that itself toggles details, underlined on hover like the
 * other admin tables while the row's highlight steps back (ROW_HOVER_CLASS).
 * The default theme's primary matches its foreground, so the color alone
 * shows nothing there.
 */
const ROW_LINK_CLASS =
  "rounded-sm decoration-1 underline-offset-3 transition-colors hover:text-primary hover:underline active:underline focus-visible:text-primary focus-visible:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring";
/**
 * A row link's text, truncated with "…" inside the link. The link itself can't
 * truncate: Firefox and WebKit clip its text to its rounded corners with either
 * `overflow: hidden` or `overflow-x: clip`, which cuts off a first or last
 * letter's descender (the "j" in "jordan"). This box has no corners, and the
 * link keeps its rounded focus ring. It clips only the inline axis: `truncate`'s
 * `overflow: hidden` would cut the top of "Ẩ" and Arabic vowel marks below the
 * line. Padding offset by a negative margin, as in `OVERFLOW_CLIP`, moves the clip
 * past the text's edges, so ink that reaches beyond them (the hook of a leading
 * "j") stays whole. The box takes no pointer events, so its padding can't catch
 * a click meant for a neighbouring link or the row; the link's own box still
 * does. A flex-item link needs `min-w-0` to shrink; a block link needs
 * `flow-root`, or the negative margins pass through it and stretch its focus
 * ring.
 */
function RowLinkText({ children, className = "" }: { children: ReactNode; className?: string }) {
  return (
    <span
      className={`pointer-events-none -m-[0.25em] block overflow-x-clip p-[0.25em] text-ellipsis whitespace-nowrap ${className}`}
    >
      {children}
    </span>
  );
}
/**
 * A clickable row's highlight and lit expand chevron, held back while the
 * pointer is on a link or a button that does something else; the expand
 * controls keep them. Uses the app's pointer-gated hover variant.
 */
const ROW_HOVER_CLASS =
  "transition-colors hover:[&:not(:has(a:hover,button:not([data-row-toggle]):hover))]:bg-surface-hover hover:[&:not(:has(a:hover,button:not([data-row-toggle]):hover))_[data-row-chevron]]:text-foreground";

/**
 * The stream table's two layouts. Rows and the header share one column
 * template; the route column only exists when routes differ between sessions
 * (see sessionRoutesVary). Below the container width the columns need, rows
 * become stacked cards. Widths are in rem so larger fonts stack sooner.
 */
interface TableLayout {
  columns: string;
  /** Classes that show an element only in the table layout. */
  table: string;
  /** Classes that show an element only in the stacked layout. */
  stacked: string;
  /** Classes that put a row's details in three columns in the table layout. */
  panel: string;
  /** Classes that rule off the second and third details columns. */
  panelColumn: string;
  /**
   * Classes for the rows' container. In the table layout a control focused
   * while under the pinned header (Shift+Tab back up the table) scrolls to
   * 4rem, clear of the header and of the shell's activity control beside it.
   * `[data-row-scroller]` marks boxes that scroll on their own, which
   * Chromium and Firefox let the keyboard focus. A root scroll-padding would
   * also scroll the page when the header's own buttons take focus.
   */
  rows: string;
}

const TABLE_LAYOUT: TableLayout = {
  columns:
    "grid-cols-[1.75rem_minmax(13rem,1.5fr)_minmax(9.5rem,1fr)_minmax(14rem,1.6fr)_minmax(7rem,1fr)_4.5rem_auto]",
  table: "hidden @min-[62rem]:grid",
  stacked: "@min-[62rem]:hidden",
  panel: "@min-[62rem]:grid-cols-[minmax(0,1.7fr)_minmax(0,1fr)_minmax(0,1fr)]",
  panelColumn: "@min-[62rem]:border-border/40 @min-[62rem]:border-s @min-[62rem]:ps-5",
  rows: "@min-[62rem]:[&_:is(a,button,[tabindex],[data-row-scroller])]:scroll-mt-16",
};

const TABLE_LAYOUT_WITH_ROUTE: TableLayout = {
  columns:
    "grid-cols-[1.75rem_minmax(13rem,1.5fr)_minmax(9.5rem,1fr)_minmax(14rem,1.6fr)_minmax(7rem,1fr)_minmax(7rem,1fr)_4.5rem_auto]",
  table: "hidden @min-[70rem]:grid",
  stacked: "@min-[70rem]:hidden",
  panel: "@min-[70rem]:grid-cols-[minmax(0,1.7fr)_minmax(0,1fr)_minmax(0,1fr)]",
  panelColumn: "@min-[70rem]:border-border/40 @min-[70rem]:border-s @min-[70rem]:ps-5",
  rows: "@min-[70rem]:[&_:is(a,button,[tabindex],[data-row-scroller])]:scroll-mt-16",
};

const STACKED_SORT_FIELDS: [SortField, MessageKey][] = [
  ["username", "web.admin.activity.column.user"],
  ["media", "web.admin.activity.column.stream"],
  ["method", "web.admin.activity.column.playback"],
  ["bitrate", "web.admin.activity.column.bitrate"],
  ["node", "web.admin.activity.column.route"],
  ["started", "web.admin.activity.column.time"],
];

// Filter controls: a segmented group per filter, so one choice per group reads
// as one control instead of a run of loose chips. The 1px gap shows the group's
// border color between segments, which keeps the dividers right when a narrow
// screen wraps a group onto two lines. The active segment's tint sits over it.
const SEGMENTED_GROUP_CLASS =
  "border-border bg-border inline-flex max-w-full flex-wrap gap-px overflow-hidden rounded-md border";

function segmentClass(active: boolean): string {
  return `focus-visible:ring-ring inline-flex h-7.5 grow items-center justify-center gap-1.5 px-2.5 text-[0.75rem] font-medium whitespace-nowrap transition-colors focus-visible:ring-2 focus-visible:outline-none focus-visible:ring-inset ${
    active
      ? "bg-primary/10 text-foreground"
      : "bg-surface text-muted-foreground hover:bg-secondary hover:text-foreground"
  }`;
}

const NODE_KIND_ORDER: ActivityRouteNode["kind"][] = ["proxy", "transcode", "server", "legacy"];

/**
 * The node filter is a menu, not a chip per node: the list grows with the
 * cluster, and a row of chips would wrap the toolbar on a large deployment.
 */
function NodeFilterMenu({
  nodes,
  selected,
  onSelect,
}: {
  nodes: (ActivityRouteNode & { count: number })[];
  selected: string | null;
  onSelect: (key: string | null) => void;
}) {
  const t = useT();
  const selectedNode = nodes.find((node) => node.key === selected);
  const groups = NODE_KIND_ORDER.map((kind) =>
    nodes.filter((node) => node.kind === kind).sort((a, b) => b.count - a.count),
  ).filter((group) => group.length > 0);

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        className={`focus-visible:ring-ring inline-flex h-8 max-w-full items-center gap-1.5 rounded-md border px-2.5 text-[0.75rem] font-medium whitespace-nowrap transition-colors focus-visible:ring-2 focus-visible:outline-none ${
          selectedNode
            ? "border-primary/40 bg-primary/10 text-foreground"
            : "border-border bg-surface text-muted-foreground hover:text-foreground"
        }`}
      >
        {selectedNode ? (
          <>
            <span className="text-muted-foreground">{selectedNode.label}</span>
            <span dir="auto" className="truncate">
              {selectedNode.name}
            </span>
          </>
        ) : (
          t("web.admin.activity.route.node")
        )}
        <ChevronDown className="h-3.5 w-3.5 opacity-60" aria-hidden="true" />
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="start"
        // The cap and the padding keep a long node name's menu inside the screen with even
        // margins, so the name is cut instead of pushing the menu past the edge.
        collisionPadding={16}
        aria-label={t("web.admin.activity.summary.byNode")}
        className="max-w-[calc(100vw-2rem)] min-w-56"
      >
        {groups.map((group, index) => (
          <div key={group[0]!.kind} role="group" aria-label={group[0]!.label}>
            {index > 0 && <DropdownMenuSeparator />}
            <DropdownMenuLabel className="text-muted-foreground text-[0.6875rem] font-medium">
              {group[0]!.label}
            </DropdownMenuLabel>
            {group.map((node) => (
              <DropdownMenuCheckboxItem
                key={node.key}
                checked={node.key === selected}
                onCheckedChange={(checked) => onSelect(checked ? node.key : null)}
                className="text-[0.8125rem]"
              >
                <span dir="auto" className="truncate">
                  {node.name}
                </span>
                <span className="text-muted-foreground ms-auto ps-4 tabular-nums">
                  {numberText(node.count)}
                </span>
              </DropdownMenuCheckboxItem>
            ))}
          </div>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

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
  const manualRefreshStartedAtRef = useRef<number | null>(null);
  const wasRealtimePausedRef = useRef(!pageActivity.canApplyRealtimeUpdates);
  const [isManualRefreshPending, setIsManualRefreshPending] = useState(false);
  const [search, setSearch] = useState("");
  const [methodFilter, setMethodFilter] = useState<string | null>(null);
  const [nodeFilter, setNodeFilter] = useState<string | null>(null);
  const [typeFilter, setTypeFilter] = useState<string | null>(null);
  const [sortField, setSortField] = useState<SortField>("started");
  const [sortDir, setSortDir] = useState<SortDir>("desc");
  const [ipLookupOpen, setIPLookupOpen] = useState(false);
  const [ipLookupSeed, setIPLookupSeed] = useState("");

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

  const totals = useMemo(() => summarizeActivity(sessions), [sessions]);
  const layout = useMemo(
    () => (sessionRoutesVary(sessions) ? TABLE_LAYOUT_WITH_ROUTE : TABLE_LAYOUT),
    [sessions],
  );
  const showRoute = layout === TABLE_LAYOUT_WITH_ROUTE;

  // Filter + sort
  const filtered = useMemo(() => {
    let result = sessions;
    if (search) {
      const q = search.toLowerCase();
      result = result.filter(
        (s) =>
          s.username?.toLowerCase().includes(q) ||
          s.profile_name?.toLowerCase().includes(q) ||
          s.media_title?.toLowerCase().includes(q) ||
          s.series_title?.toLowerCase().includes(q) ||
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
    if (typeFilter) {
      // A file linked to a show with no matching episode row is reported as
      // the show itself, so the episode choice keeps both.
      result = result.filter((s) =>
        typeFilter === "episode"
          ? s.media_type === "episode" || s.media_type === "series"
          : s.media_type === typeFilter,
      );
    }

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
        case "bitrate":
          cmp = (a.stream_bitrate_kbps ?? 0) - (b.stream_bitrate_kbps ?? 0);
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
      setSortDir(field === "started" || field === "bitrate" ? "desc" : "asc");
    }
  };

  const activeFilters = [methodFilter, nodeFilter, typeFilter].filter(Boolean).length;

  const runIPLookup = useCallback((ip: string) => {
    const trimmed = ip.trim();
    if (!trimmed) {
      return;
    }
    setIPLookupSeed(trimmed);
    setIPLookupOpen(true);
  }, []);

  // Download preparation moved to the Downloads page; keep old links working.
  if (searchParams.get("view") === "preparations") {
    return <Navigate to="/admin/downloads?tab=preparation" replace />;
  }
  if (isLoading)
    return <div className="text-muted-foreground pb-8">{t("web.admin.activity.page.loading")}</div>;

  const summary =
    sessions.length === 0
      ? t("web.admin.activity.page.noStreams")
      : [
          t("web.admin.activity.page.subtitle", {
            count: sessions.length,
            count_text: sessions.length,
            nodes: routeNodes.length,
            nodes_text: routeNodes.length,
          }),
          totals.videoTranscodes > 0 &&
            t("web.admin.activity.summary.videoTranscodes", {
              count: totals.videoTranscodes,
              count_text: totals.videoTranscodes,
            }),
          totals.bitrateKbps > 0 &&
            t("web.admin.activity.summary.bandwidth", {
              rate: formatSessionBitrate(totals.bitrateKbps) ?? "",
            }),
          totals.remote > 0 &&
            t("web.admin.activity.summary.remote", { count_text: totals.remote }),
          totals.paused > 0 &&
            t("web.admin.activity.summary.paused", { count_text: totals.paused }),
        ]
          .filter(Boolean)
          .join(` ${MIDDLE_DOT} `);

  return (
    <div className="space-y-4">
      <PageHeader
        title={t("web.admin.activity.page.title")}
        subtitle={summary}
        actions={
          <>
            <RealtimeStatus state={connectionState} />
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setIPLookupSeed("");
                setIPLookupOpen(true);
              }}
            >
              <Globe className="h-3.5 w-3.5" />
              {t("web.admin.activity.ipLookup.heading")}
            </Button>
            <Button
              variant="outline"
              size="sm"
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
          </>
        }
      />

      {/* Search + filters */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative w-full sm:w-80">
          <Search className="text-muted-foreground pointer-events-none absolute start-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2" />
          <Input
            placeholder={t("web.admin.activity.filter.placeholder")}
            aria-label={t("web.admin.activity.filter.placeholder")}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="h-8 ps-9 pe-8 text-[0.8125rem]"
          />
          {search && (
            <button
              type="button"
              onClick={() => setSearch("")}
              aria-label={t("web.admin.users.list.clearSearch")}
              className="text-muted-foreground hover:text-foreground absolute end-2.5 top-1/2 -translate-y-1/2"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          )}
        </div>
        {sessions.length > 0 && (
          <div
            role="group"
            aria-label={t("web.admin.activity.summary.methodHeading")}
            className={SEGMENTED_GROUP_CLASS}
          >
            {Object.entries(methods)
              .sort(([a], [b]) => compareActivityMethods(a, b))
              .map(([method, count]) => {
                const meta = activityMethodMeta(method);
                return (
                  <button
                    key={method}
                    type="button"
                    title={meta.description}
                    aria-label={t("web.admin.activity.summary.methodFilter", {
                      method: meta.label,
                      count: count,
                    })}
                    aria-pressed={methodFilter === method}
                    onClick={() => setMethodFilter(methodFilter === method ? null : method)}
                    className={segmentClass(methodFilter === method)}
                  >
                    <span
                      aria-hidden="true"
                      className={`inline-block h-2 w-2 rounded-full ${meta.swatchClass}`}
                    />
                    {meta.label}
                    <span className="text-muted-foreground tabular-nums">{numberText(count)}</span>
                  </button>
                );
              })}
          </div>
        )}
        <div className={SEGMENTED_GROUP_CLASS}>
          {(["movie", "episode"] as const).map((type) => (
            <button
              key={type}
              type="button"
              aria-pressed={typeFilter === type}
              onClick={() => setTypeFilter(typeFilter === type ? null : type)}
              className={segmentClass(typeFilter === type)}
            >
              {type === "movie"
                ? t("web.admin.activity.filter.movies")
                : t("web.admin.activity.filter.series")}
            </button>
          ))}
        </div>
        {routeNodes.length > 1 && (
          <NodeFilterMenu nodes={routeNodes} selected={nodeFilter} onSelect={setNodeFilter} />
        )}
        {activeFilters > 0 && (
          <button
            type="button"
            onClick={() => {
              setMethodFilter(null);
              setNodeFilter(null);
              setTypeFilter(null);
            }}
            className="text-muted-foreground hover:text-foreground flex items-center gap-1 text-[0.75rem]"
          >
            <X className="h-3 w-3" />
            {t("web.admin.activity.filter.clear")}
          </button>
        )}
      </div>

      {/* Filter status */}
      {(search || activeFilters > 0) && (
        <div className="text-muted-foreground text-[0.75rem]">
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
        <div className="bg-card border-border @container overflow-clip rounded-lg border">
          {/* Sort controls for the stacked rows, which have no column headers */}
          <div
            role="group"
            aria-label={t("web.admin.activity.sort.group")}
            className={`border-border bg-surface/50 flex flex-wrap items-center gap-x-4 gap-y-1.5 border-b px-4 py-2.5 ${layout.stacked}`}
          >
            <span className="text-muted-foreground text-[0.75rem]">
              {t("web.admin.activity.sort.heading")}
            </span>
            {STACKED_SORT_FIELDS.filter(([field]) => showRoute || field !== "node").map(
              ([field, label]) => (
                <SortHeader
                  key={field}
                  field={field}
                  current={sortField}
                  dir={sortDir}
                  onClick={toggleSort}
                >
                  {t(label)}
                </SortHeader>
              ),
            )}
          </div>

          {/* Table header, which stays in view while the page scrolls, below any
              impersonation banner */}
          <div
            className={`border-border bg-card/95 sticky top-(--impersonation-banner-height) z-10 ${layout.table} ${layout.columns} items-center gap-3 border-b px-3 py-2 backdrop-blur`}
          >
            <span />
            <SortHeader field="media" current={sortField} dir={sortDir} onClick={toggleSort}>
              {t("web.admin.activity.column.stream")}
            </SortHeader>
            <SortHeader field="username" current={sortField} dir={sortDir} onClick={toggleSort}>
              {t("web.admin.activity.column.user")}
            </SortHeader>
            <SortHeader field="method" current={sortField} dir={sortDir} onClick={toggleSort}>
              {t("web.admin.activity.column.playback")}
            </SortHeader>
            <SortHeader field="bitrate" current={sortField} dir={sortDir} onClick={toggleSort}>
              {t("web.admin.activity.column.bitrate")}
            </SortHeader>
            {showRoute && (
              <SortHeader field="node" current={sortField} dir={sortDir} onClick={toggleSort}>
                {t("web.admin.activity.column.route")}
              </SortHeader>
            )}
            <SortHeader
              field="started"
              current={sortField}
              dir={sortDir}
              onClick={toggleSort}
              className="justify-end"
            >
              {t("web.admin.activity.column.time")}
            </SortHeader>
            <span className="sr-only">{t("web.admin.activity.column.actions")}</span>
          </div>

          {/* Rows */}
          <div className={layout.rows}>
            {filtered.map((session) => (
              <StreamRow
                key={session.session_id}
                session={session}
                layout={layout}
                showRoute={showRoute}
                onIPLookup={runIPLookup}
              />
            ))}
          </div>
        </div>
      )}

      <IPLookupDialog open={ipLookupOpen} onOpenChange={setIPLookupOpen} seed={ipLookupSeed} />
    </div>
  );
}

// --- Sub-components ---

/** One quiet status line; it only draws attention when updates aren't live. */
function RealtimeStatus({ state }: { state: "connecting" | "live" | "disconnected" }) {
  const t = useT();
  return (
    <span
      role="status"
      title={t("web.admin.activity.realtime.heading")}
      className={`inline-flex items-center gap-1.5 px-1 text-[0.75rem] ${
        state === "live" ? "text-muted-foreground" : "text-warning font-medium"
      }`}
    >
      <span
        aria-hidden="true"
        className={`h-1.5 w-1.5 rounded-full ${
          state === "live" ? "bg-success" : state === "connecting" ? "bg-warning" : "bg-destructive"
        }`}
      />
      {formatConnectionState(state, t)}
    </span>
  );
}

function StreamRow({
  session,
  layout,
  showRoute,
  onIPLookup,
}: {
  session: AdminSession;
  layout: TableLayout;
  showRoute: boolean;
  onIPLookup: (ip: string) => void;
}) {
  const t = useT();
  const panelId = useId();
  const username =
    session.username || t("web.admin.activity.userFallback", { id: session.user_id });
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [ffmpegOpen, setFFmpegOpen] = useState(false);
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
  const reasons = getAdaptationReasons(session);
  const activityMethod = classifyActivityMethod(session);

  const toggleDetails = () => {
    setDetailsOpen((open) => {
      const nextOpen = !open;
      if (!nextOpen) {
        setFFmpegOpen(false);
      }
      return nextOpen;
    });
  };
  const openDetails = () => setDetailsOpen(true);
  // A click anywhere on the row toggles its details, as a mouse shortcut for
  // the expand button (which stays the keyboard and screen-reader control).
  // Clicks on the row's own links and buttons keep their meaning, as do
  // clicks that end a text selection and clicks inside portaled menus and
  // dialogs, which React bubbles here though they sit outside the row.
  const onRowClick = (event: ReactMouseEvent<HTMLDivElement>) => {
    const target = event.target as Element;
    if (!event.currentTarget.contains(target)) return;
    if (target.closest("a, button, input, select, textarea, label, [role='button']")) return;
    if (window.getSelection()?.type === "Range") return;
    toggleDetails();
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

  const actions = (
    <AdminSessionActions
      session={session}
      compact
      layout="inline"
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
  );
  const expandButton = (
    <button
      type="button"
      data-row-toggle
      onClick={toggleDetails}
      aria-expanded={expandedOpen}
      aria-controls={expandedOpen ? panelId : undefined}
      aria-label={t("web.admin.activity.row.details")}
      title={t("web.admin.activity.row.details")}
      className="text-muted-foreground hover:text-foreground hover:bg-surface-raised flex h-7 w-7 shrink-0 items-center justify-center rounded-md transition-colors"
    >
      <ChevronRight
        aria-hidden="true"
        data-row-chevron
        className={`h-4 w-4 transition-[transform,color] rtl:-scale-x-100 ${expandedOpen ? "rotate-90 rtl:-rotate-90" : ""}`}
      />
    </button>
  );

  return (
    <div className="border-border/40 border-b last:border-b-0">
      {/* Table row */}
      <div
        onClick={onRowClick}
        className={`${layout.table} ${layout.columns} ${ROW_HOVER_CLASS} cursor-pointer items-center gap-3 px-3 py-2`}
      >
        {expandButton}
        <MediaCell session={session} />
        <ViewerCell session={session} onIPLookup={onIPLookup} />
        <div className="min-w-0 space-y-0.5">
          <MethodLine
            method={activityMethod}
            reasons={reasons}
            expanded={expandedOpen}
            panelId={panelId}
            onMore={openDetails}
          />
          <ConversionLine kind="video" session={session} />
          <ConversionLine kind="audio" session={session} />
        </div>
        <BitrateCell session={session} />
        {showRoute && (
          <div className="flex min-w-0 flex-wrap gap-1">
            <PlaybackRouteBadges session={session} showLocation={false} />
          </div>
        )}
        <SessionCell session={session} />
        <div className="flex items-center justify-end gap-0.5">
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7"
            onClick={toggleFFmpeg}
            aria-label={
              ffmpegOpen
                ? t("web.admin.activity.row.hideFfmpeg")
                : t("web.admin.activity.row.ffmpeg")
            }
            title={
              ffmpegOpen
                ? t("web.admin.activity.row.hideFfmpeg")
                : t("web.admin.activity.row.ffmpeg")
            }
          >
            <Terminal className="h-3.5 w-3.5" />
          </Button>
          {actions}
        </div>
      </div>

      {/* Stacked card */}
      <div
        onClick={onRowClick}
        className={`${layout.stacked} ${ROW_HOVER_CLASS} flex cursor-pointer gap-3 px-3 py-3`}
      >
        <Poster session={session} size="stacked" />
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex items-start gap-2">
            <div className="min-w-0 flex-1">
              <TitleLink session={session} />
              {isEpisodeSession(session) ? (
                // The episode code and title, under the show's title.
                <div className="text-muted-foreground truncate text-[0.75rem]">
                  {getPlaybackSessionSubtitle(session)}
                </div>
              ) : null}
              <div className="text-muted-foreground flex min-w-0 items-center gap-1.5 text-[0.75rem]">
                <Link
                  to={`/admin/users/${session.user_id}`}
                  title={username}
                  dir="auto"
                  className={`${ROW_LINK_CLASS} min-w-0`}
                >
                  <RowLinkText>{username}</RowLinkText>
                </Link>
                <ProfilePill session={session} />
                <JellyfinSessionPill session={session} />
                {getSessionClientLabel(session) ? (
                  <>
                    <span aria-hidden="true" className="shrink-0">
                      {MIDDLE_DOT}
                    </span>
                    <span dir="auto" className="truncate">
                      {getSessionClientLabel(session)}
                    </span>
                  </>
                ) : null}
              </div>
            </div>
            <div className="-me-1 shrink-0">{actions}</div>
          </div>
          <div className="flex items-center gap-1">
            <div className="min-w-0 flex-1">
              <MethodLine
                method={activityMethod}
                reasons={reasons}
                expanded={expandedOpen}
                panelId={panelId}
                onMore={openDetails}
              />
            </div>
            {expandButton}
          </div>
          <ProgressLine session={session} />
        </div>
      </div>

      {expandedOpen && (
        <PlaybackExpandedPanel
          id={panelId}
          session={session}
          reasons={reasons}
          layout={layout}
          showRoute={showRoute}
          onIPLookup={onIPLookup}
          ffmpegOpen={ffmpegOpen}
          onToggleFFmpeg={toggleFFmpeg}
          rows={ffmpegRows}
          isLoading={ffmpegLogs.isLoading}
          isFetching={ffmpegLogs.isFetching}
          logsHref={ffmpegLogsHref}
        />
      )}
    </div>
  );
}

function Poster({ session, size }: { session: AdminSession; size: "table" | "stacked" }) {
  const box = size === "table" ? "h-12 w-8" : "h-[60px] w-10";
  return session.poster_url ? (
    <img
      src={session.poster_url}
      alt=""
      loading="lazy"
      decoding="async"
      className={`${box} bg-surface shrink-0 rounded-sm object-cover`}
    />
  ) : (
    <div aria-hidden="true" className={`${box} bg-surface shrink-0 rounded-sm`} />
  );
}

function TitleLink({ session }: { session: AdminSession }) {
  const t = useT();
  const title = getDisplayTitle(session, t);
  const itemHref = session.media_item_id ? `/item/${session.media_item_id}` : "";
  return itemHref ? (
    <Link
      to={itemHref}
      title={title}
      dir="auto"
      className={`${ROW_LINK_CLASS} flow-root w-fit max-w-full text-[0.8125rem] font-medium`}
    >
      <RowLinkText className="bidi-text">{title}</RowLinkText>
    </Link>
  ) : (
    <div title={title} dir="auto" className="bidi-text truncate text-[0.8125rem] font-medium">
      {title}
    </div>
  );
}

function MediaCell({ session }: { session: AdminSession }) {
  const subtitle = getPlaybackSessionSubtitle(session);
  return (
    <div className="flex min-w-0 items-center gap-2.5">
      <Poster session={session} size="table" />
      <div className="min-w-0 flex-1">
        <TitleLink session={session} />
        {subtitle ? (
          <div className="text-muted-foreground truncate text-[0.75rem]">{subtitle}</div>
        ) : null}
        <ProgressLine session={session} />
      </div>
    </div>
  );
}

/**
 * Position through the title, with a bar, and whether the viewer paused. The
 * bar narrows before anything wraps; past that the line wraps, so its end
 * (usually the pause label) never paints over the next column. When the
 * position alone is wider than the column (a large minimum font size), it
 * wraps at the " / ", and a one-word pause label drops below its icon rather
 * than breaking mid-word.
 */
function ProgressLine({ session }: { session: AdminSession }) {
  const t = useT();
  const progress = getPlaybackProgress(session);
  return (
    <div className="text-muted-foreground flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-[0.6875rem]">
      {progress !== null ? (
        <span
          aria-hidden="true"
          className="bg-border h-1 max-w-14 min-w-9 flex-1 overflow-hidden rounded-full"
        >
          <span
            className={`block h-full rounded-full ${session.is_paused ? "bg-warning" : "bg-primary"}`}
            style={{ width: `${progress * 100}%` }}
          />
        </span>
      ) : null}
      <span className="max-w-full shrink-0 tabular-nums">{formatPlaybackPosition(session)}</span>
      {session.is_paused ? (
        <span className="text-warning inline-flex max-w-full shrink-0 flex-wrap items-center gap-1 font-medium">
          <Pause aria-hidden="true" className="h-3 w-3 shrink-0" />
          {t("web.admin.activity.row.paused")}
        </span>
      ) : (
        <span className="inline-flex shrink-0 items-center">
          <Play aria-hidden="true" className="h-3 w-3" />
          <span className="sr-only">{t("web.admin.activity.row.playing")}</span>
        </span>
      )}
    </div>
  );
}

/** The household profile playing, which can differ from the account's name. */
function ProfilePill({ session }: { session: AdminSession }) {
  const profile = session.profile_name?.trim() || session.profile_id?.trim();
  return profile ? (
    <span
      title={profile}
      dir="auto"
      className="border-primary/30 bg-primary/10 text-primary max-w-[7rem] shrink-0 truncate rounded border px-1.5 py-0.5 text-[0.625rem] leading-none"
    >
      {profile}
    </span>
  ) : null;
}

function ViewerCell({
  session,
  onIPLookup,
}: {
  session: AdminSession;
  onIPLookup: (ip: string) => void;
}) {
  const t = useT();
  const username =
    session.username || t("web.admin.activity.userFallback", { id: session.user_id });
  const clientLabel = getSessionClientLabel(session);
  // The row stays compact; the exact version, build, and channel live in the
  // tooltip and in the expanded panel.
  const clientLabelFull = getSessionClientLabelFull(session);
  const clientUserAgent = session.client_user_agent?.trim() || "";
  const clientTitle = clientUserAgent
    ? `${clientLabelFull || clientLabel} — ${clientUserAgent}`
    : clientLabelFull || clientLabel;
  const clientIP = session.client_ip?.trim() || "";
  // A long IPv6 address truncates, so the tooltip carries the whole address.
  const clientIPTitle = `${clientIP} — ${t("web.admin.activity.ipLookup.heading")}`;
  const location =
    session.stream_location === "local"
      ? t("web.player.routeBadge.local")
      : session.stream_location === "remote"
        ? t("web.player.routeBadge.remote")
        : "";
  return (
    <div className="min-w-0">
      <div className="flex min-w-0 items-center gap-1.5">
        <Link
          to={`/admin/users/${session.user_id}`}
          title={username}
          dir="auto"
          className={`${ROW_LINK_CLASS} min-w-0 text-[0.8125rem] font-medium`}
        >
          <RowLinkText>{username}</RowLinkText>
        </Link>
        <ProfilePill session={session} />
      </div>
      <div className="text-muted-foreground flex min-w-0 items-center gap-1.5 text-[0.75rem]">
        <JellyfinSessionPill session={session} />
        {clientLabel ? (
          <span title={clientTitle} dir="auto" className="truncate">
            {clientLabel}
          </span>
        ) : null}
      </div>
      {(location || clientIP) && (
        <div className="text-muted-foreground flex min-w-0 items-center gap-1.5 text-[0.6875rem]">
          {location ? <span className="shrink-0">{location}</span> : null}
          {location && clientIP ? (
            <span aria-hidden="true" className="shrink-0">
              {MIDDLE_DOT}
            </span>
          ) : null}
          {clientIP ? (
            <button
              type="button"
              onClick={() => onIPLookup(clientIP)}
              title={clientIPTitle}
              className={`${ROW_LINK_CLASS} min-w-0 tabular-nums`}
            >
              <RowLinkText>{clientIP}</RowLinkText>
            </button>
          ) : null}
        </div>
      )}
    </div>
  );
}

/**
 * The whole-session method, the first reason it isn't direct play, and a
 * button for the rest, which the expanded details list with explanations.
 */
function MethodLine({
  method,
  reasons,
  expanded,
  panelId,
  onMore,
}: {
  method: string;
  reasons: AdaptationReason[];
  expanded: boolean;
  panelId: string;
  onMore: () => void;
}) {
  const t = useT();
  const [first, ...rest] = reasons;
  return (
    <div className="flex min-w-0 items-center gap-2">
      <ActivityMethodBadge method={method} />
      {first ? (
        <span title={first.description} className="min-w-0 truncate text-[0.75rem]">
          {first.label}
        </span>
      ) : null}
      {rest.length > 0 ? (
        <button
          type="button"
          data-row-toggle
          onClick={onMore}
          aria-expanded={expanded}
          aria-controls={expanded ? panelId : undefined}
          aria-label={t("web.admin.activity.row.moreReasonsLabel", {
            count: rest.length,
            count_text: rest.length,
          })}
          title={joinList(rest.map((reason) => reason.label))}
          className="text-muted-foreground hover:text-foreground border-border shrink-0 rounded border px-1 text-[0.6875rem] leading-4 tabular-nums transition-colors"
        >
          {t("web.admin.activity.row.moreReasons", { count_text: rest.length })}
        </button>
      ) : null}
    </div>
  );
}

/**
 * One stream as "source → delivered" when the server converts it, or just the
 * source, muted, when it reaches the viewer untouched. The encoder (hardware
 * or software) follows a video transcode.
 */
function ConversionLine({ kind, session }: { kind: "video" | "audio"; session: AdminSession }) {
  const t = useT();
  const conversion: StreamConversion =
    kind === "video" ? getVideoConversion(session) : getAudioConversion(session);
  const Icon = kind === "video" ? Film : AudioLines;
  const mode =
    kind === "video" && conversion.delivered ? formatTranscodeModeSummary(session) : null;
  return (
    <div className="flex min-w-0 items-center gap-1.5 text-[0.75rem]">
      <Icon aria-hidden="true" className="text-muted-foreground h-3 w-3 shrink-0" />
      <span className="sr-only">
        {kind === "video" ? t("web.admin.activity.row.video") : t("web.admin.activity.row.audio")}
      </span>
      <span className={`min-w-0 truncate ${conversion.delivered ? "" : "text-muted-foreground"}`}>
        {conversion.delivered
          ? t("web.admin.activity.detail.conversion", {
              source: conversion.source,
              output: conversion.delivered,
            })
          : conversion.source}
      </span>
      {mode ? (
        <span className="border-border text-muted-foreground shrink-0 rounded border px-1 text-[0.625rem] leading-4">
          {mode}
        </span>
      ) : null}
    </div>
  );
}

function SessionCell({ session }: { session: AdminSession }) {
  const t = useT();
  const elapsed = getElapsed(session.started_at);
  return (
    <div className="min-w-0 text-end">
      <div
        title={t("web.admin.activity.row.sessionActive", { elapsed })}
        className="inline-flex items-center gap-1 text-[0.75rem] tabular-nums"
      >
        <Clock aria-hidden="true" className="text-muted-foreground h-3 w-3" />
        <span className="sr-only">{t("web.admin.activity.row.sessionActive", { elapsed })}</span>
        <span aria-hidden="true">{elapsed}</span>
      </div>
    </div>
  );
}

/**
 * The rate the viewer receives now, over the source's bitrate and, for a
 * transcode, the ceiling it encodes to. The bar is the delivered share of the
 * source: full for an untouched file, short when the route squeezes it.
 */
function BitrateCell({ session }: { session: AdminSession }) {
  const t = useT();
  const { live, source, limit, share } = getSessionBitrate(session);
  const reference = bitrateReference(t, limit, source);
  return (
    <div className="min-w-0 tabular-nums">
      {live ? <div className="truncate text-[0.75rem]">{live}</div> : null}
      {reference ? (
        <div className="text-muted-foreground text-[0.6875rem] leading-snug">{reference}</div>
      ) : null}
      {share !== null ? (
        <div
          aria-hidden="true"
          className="bg-border mt-1 h-1 max-w-32 overflow-hidden rounded-full"
        >
          <div
            className={`h-full rounded-full ${limit ? "bg-warning" : "bg-success"}`}
            style={{ width: `${share * 100}%` }}
          />
        </div>
      ) : null}
    </div>
  );
}

/** What the delivered rate is measured against: a transcode's limit, the source, or both. */
function bitrateReference(t: Translate, limit: string | null, source: string | null): string {
  if (limit && source) return t("web.admin.activity.bitrate.limitAndSource", { limit, source });
  if (limit) return t("web.admin.activity.bitrate.limit", { rate: limit });
  if (source) return t("web.admin.activity.bitrate.source", { rate: source });
  return "";
}

function ActivityMethodBadge({ method }: { method: string }) {
  const t = useT();
  const meta = activityMethodMeta(method);
  return (
    <span
      aria-label={t("web.admin.activity.row.methodBadge", { method: meta.label })}
      title={meta.description}
      className={`inline-flex shrink-0 rounded border px-1.5 py-0.5 text-[0.625rem] leading-none font-semibold ${meta.badgeClass}`}
    >
      {meta.label}
    </span>
  );
}

function PlaybackExpandedPanel({
  id,
  session,
  reasons,
  layout,
  showRoute,
  onIPLookup,
  ffmpegOpen,
  onToggleFFmpeg,
  rows,
  isLoading,
  isFetching,
  logsHref,
}: {
  id: string;
  session: AdminSession;
  reasons: AdaptationReason[];
  layout: TableLayout;
  /** Whether the table row has a route column. */
  showRoute: boolean;
  onIPLookup: (ip: string) => void;
  ffmpegOpen: boolean;
  onToggleFFmpeg: () => void;
  rows: OperationalLogEntry[];
  isLoading: boolean;
  isFetching: boolean;
  logsHref: string;
}) {
  const t = useT();
  const videoDecision = normalizeStreamDecision(session.video_decision || session.play_method);
  const audioDecision = normalizeStreamDecision(
    session.audio_decision || (session.transcode_audio ? "transcode" : session.play_method),
  );
  const method = classifyActivityMethod(session);
  const transcodeMode = formatTranscodeModeSummary(session);
  const toneMap = formatToneMapSummary(session);
  const clientIP = session.client_ip?.trim() || "";
  const bitrate = getSessionBitrate(session);
  const clientLabel = getSessionClientLabelFull(session) || t("web.admin.activity.client.unknown");
  const userAgent = session.client_user_agent?.trim() || "";
  const devicePlatform = session.device_platform?.trim() || "";
  const deviceID = session.device_id?.trim() || "";

  return (
    <div id={id} className="border-border/40 bg-surface/30 space-y-4 border-t px-4 py-3">
      <div className={`grid gap-x-5 gap-y-4 text-[0.75rem] ${layout.panel}`}>
        <div className="min-w-0 space-y-4">
          <dl className={FACT_LIST_CLASS}>
            <dt className="text-muted-foreground">{t("web.admin.activity.panel.decision")}</dt>
            <dd className="min-w-0">
              <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-0.5">
                <ActivityMethodBadge method={method} />
                <span>{activityMethodMeta(method).description}</span>
              </span>
            </dd>
            {reasons.length > 0 ? (
              <>
                <dt className="text-muted-foreground">{t("web.admin.activity.panel.reasons")}</dt>
                <dd className="min-w-0">
                  <ul className="space-y-0.5">
                    {reasons.map((reason) => (
                      <li key={reason.code} className="flex flex-col leading-snug sm:block">
                        <span className="font-medium">{reason.label}</span>
                        <span className="text-muted-foreground sm:ms-2">{reason.description}</span>
                      </li>
                    ))}
                  </ul>
                </dd>
              </>
            ) : null}
          </dl>

          <table className="w-full text-start">
            <thead>
              <tr className="text-muted-foreground">
                <th scope="col" className="pb-1 text-start font-normal sm:w-36">
                  <span className="sr-only">{t("web.admin.activity.panel.streamDetails")}</span>
                </th>
                <th scope="col" className="pb-1 text-start font-normal">
                  {t("web.admin.activity.detail.source")}
                </th>
                <th scope="col" className="pb-1 text-start font-normal">
                  {t("web.admin.activity.detail.delivered")}
                </th>
              </tr>
            </thead>
            <tbody className="align-top">
              <StreamDetailRow
                label={t("web.admin.activity.row.video")}
                decision={videoDecision}
                source={formatVideoSummary(session)}
                delivered={formatDeliveredVideoSummary(session)}
                tags={[
                  ...(videoDecision === "transcode" ? [transcodeMode, toneMap?.badge] : []),
                  formatRequestedSourceSwitch(session),
                ]}
              />
              <StreamDetailRow
                label={t("web.admin.activity.row.audio")}
                decision={audioDecision}
                source={formatAudioSummary(session)}
                delivered={formatDeliveredAudioSummary(session)}
                tags={
                  audioDecision === "transcode" && videoDecision !== "transcode"
                    ? [transcodeMode]
                    : []
                }
              />
              <StreamDetailRow
                label={t("web.admin.activity.row.container")}
                decision={normalizeContainerDecision(session.play_method)}
                source={formatSourceContainerSummary(session)}
                delivered={formatDeliveredContainerSummary(session)}
                tags={[]}
              />
              {bitrate.live || bitrate.source ? (
                <StreamDetailRow
                  label={t("web.admin.activity.column.bitrate")}
                  source={bitrate.source ?? ""}
                  delivered={bitrate.live ?? ""}
                  tags={[
                    bitrate.limit
                      ? t("web.admin.activity.bitrate.limit", { rate: bitrate.limit })
                      : null,
                  ]}
                />
              ) : null}
            </tbody>
          </table>
        </div>

        <section className={`min-w-0 ${layout.panelColumn}`}>
          <h3 className={PANEL_HEADING_CLASS}>{t("web.admin.activity.client.heading")}</h3>
          <dl className={SIDE_FACT_LIST_CLASS}>
            <dt className="text-muted-foreground">{t("web.admin.activity.client.app")}</dt>
            <dd className="min-w-0 break-words">{clientLabel}</dd>
            {devicePlatform || deviceID ? (
              <>
                <dt className="text-muted-foreground">{t("web.admin.activity.client.device")}</dt>
                <dd className="min-w-0">
                  {devicePlatform ? <div className="break-words">{devicePlatform}</div> : null}
                  {deviceID ? (
                    <div className="text-muted-foreground font-mono text-[0.6875rem] break-all">
                      {deviceID}
                    </div>
                  ) : null}
                </dd>
              </>
            ) : null}
            {userAgent ? (
              <>
                <dt className="text-muted-foreground">{t("web.admin.activity.client.agent")}</dt>
                <dd className="text-muted-foreground min-w-0 text-[0.6875rem] break-words">
                  {userAgent}
                </dd>
              </>
            ) : null}
          </dl>
          {/* A table row with a route column already shows the route. */}
          <dl className={`${SIDE_FACT_LIST_CLASS} ${showRoute ? layout.stacked : ""} mt-1`}>
            <dt className="text-muted-foreground">{t("web.admin.activity.column.route")}</dt>
            <dd className="flex min-w-0 flex-wrap items-center gap-1">
              <PlaybackRouteBadges session={session} />
              {clientIP ? (
                <button
                  type="button"
                  onClick={() => onIPLookup(clientIP)}
                  title={t("web.admin.activity.ipLookup.heading")}
                  className={`${ROW_LINK_CLASS} ms-1 tabular-nums`}
                >
                  {clientIP}
                </button>
              ) : null}
            </dd>
          </dl>
        </section>

        <section className={`min-w-0 ${layout.panelColumn}`}>
          <h3 className={PANEL_HEADING_CLASS}>{t("web.admin.activity.session.heading")}</h3>
          <dl className={SIDE_FACT_LIST_CLASS}>
            <dt className="text-muted-foreground">{t("web.admin.activity.session.started")}</dt>
            <dd className="min-w-0 tabular-nums">
              {t("web.admin.activity.session.startedAgo", {
                time: formatTime(session.started_at),
                elapsed: getElapsed(session.started_at),
              })}
            </dd>
            <dt className="text-muted-foreground">{t("web.admin.activity.session.lastReport")}</dt>
            <dd className="min-w-0 tabular-nums">
              {t("web.admin.activity.session.lastReportAgo", {
                elapsed: getElapsed(session.updated_at),
              })}
            </dd>
            <dt className="text-muted-foreground">
              {t("web.admin.activity.session.remoteControl")}
            </dt>
            <dd
              className={`min-w-0 ${session.has_playback_control ? "" : "text-muted-foreground"}`}
            >
              {session.has_playback_control
                ? t("web.admin.activity.session.remoteControlAvailable")
                : t("web.admin.activity.session.remoteControlUnavailable")}
            </dd>
            <dt className="text-muted-foreground">{t("web.admin.activity.session.id")}</dt>
            <dd className="text-muted-foreground min-w-0 font-mono text-[0.6875rem] break-all">
              {session.session_id}
            </dd>
          </dl>
        </section>
      </div>

      <div className={`${layout.stacked} flex flex-wrap gap-2`}>
        <Button variant="outline" size="sm" onClick={onToggleFFmpeg}>
          <Terminal className="h-3.5 w-3.5" />
          {ffmpegOpen ? t("web.admin.activity.row.hideFfmpeg") : t("web.admin.activity.row.ffmpeg")}
        </Button>
      </div>

      {ffmpegOpen ? (
        <FFmpegConsole
          rows={rows}
          isLoading={isLoading}
          isFetching={isFetching}
          logsHref={logsHref}
        />
      ) : null}
    </div>
  );
}

// The decision list and the stream table share one label column, so they line
// up; the client and session lists sit in narrower columns beside them.
const FACT_LIST_CLASS = "grid grid-cols-[6rem_1fr] gap-y-1 sm:grid-cols-[9rem_1fr] [&>dt]:pe-3";
const SIDE_FACT_LIST_CLASS =
  "grid grid-cols-[6rem_1fr] gap-y-1 sm:grid-cols-[7rem_1fr] [&>dt]:pe-3";
const PANEL_HEADING_CLASS = "text-muted-foreground mb-1.5 text-[0.6875rem] font-medium";

function StreamDetailRow({
  label,
  decision,
  source,
  delivered,
  tags,
}: {
  label: string;
  /** Absent for a row that compares values rather than a stream the route handles. */
  decision?: string;
  source: string;
  delivered: string;
  tags: (string | null | undefined)[];
}) {
  return (
    <tr>
      <th scope="row" className="py-0.5 pe-3 text-start font-normal">
        <span className="inline-flex items-center gap-1.5">
          <span>{label}</span>
          {decision ? (
            <span
              className={`inline-flex rounded border px-1 py-0.5 text-[0.625rem] leading-none font-semibold ${decisionBadgeClass(decision)}`}
            >
              {formatDecisionLabel(decision)}
            </span>
          ) : null}
        </span>
      </th>
      <td className="text-muted-foreground py-0.5 pe-3 break-words">{source}</td>
      <td className="py-0.5 break-words">
        {delivered}
        {tags.filter(Boolean).map((tag) => (
          <span
            key={tag}
            className="border-border text-muted-foreground ms-1.5 inline-block rounded border px-1 text-[0.625rem] leading-4"
          >
            {tag}
          </span>
        ))}
      </td>
    </tr>
  );
}

function FFmpegConsole({
  rows,
  isLoading,
  isFetching,
  logsHref,
}: {
  rows: OperationalLogEntry[];
  isLoading: boolean;
  isFetching: boolean;
  logsHref: string;
}) {
  const t = useT();
  return (
    <div className="terminal-surface space-y-2">
      <div className="flex items-center justify-between gap-3">
        <span className="text-muted-foreground text-[0.6875rem]">
          {isFetching ? t("web.admin.activity.panel.refreshing") : null}
        </span>
        <Link
          to={logsHref}
          className="text-[0.6875rem] font-medium text-[var(--terminal-fg)] hover:text-[var(--terminal-fg)]/80"
        >
          {t("web.admin.activity.panel.openFfmpegLogs")}
        </Link>
      </div>
      <div className="overflow-hidden rounded-xl border border-[var(--terminal-border)] bg-[var(--terminal-bg)]">
        {isLoading ? (
          <div className="px-4 py-6 font-mono text-[0.6875rem] text-[var(--terminal-muted)]">
            {t("web.admin.activity.panel.loadingFfmpeg")}
          </div>
        ) : rows.length === 0 ? (
          <div className="px-4 py-6 font-mono text-[0.6875rem] text-[var(--terminal-muted)]">
            {t("web.admin.activity.panel.noFfmpegRows")}
          </div>
        ) : (
          <div data-row-scroller className="max-h-64 overflow-y-auto">
            {rows.map((row) => (
              <div
                key={row.id}
                className="grid grid-cols-[120px_1fr] gap-3 border-b border-[var(--terminal-border)]/30 px-4 py-2.5 last:border-b-0"
              >
                <div className="space-y-1">
                  <div className="font-mono text-[0.625rem] text-[var(--terminal-muted)]">
                    {formatTimeOnly(row.timestamp)}
                  </div>
                  <div className="text-[0.625rem] text-[var(--terminal-muted)]/60">
                    {row.message.includes("stderr")
                      ? t("web.admin.activity.panel.stderr")
                      : t("web.admin.activity.panel.event")}
                  </div>
                </div>
                <div className="min-w-0">
                  <div className="font-mono text-[0.6875rem] leading-5 break-words text-[var(--terminal-fg)]">
                    {ffmpegRowText(row)}
                  </div>
                  <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-[0.625rem] text-[var(--terminal-muted)]/60">
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
  );
}

/**
 * Who has used an address recently. Opened from the toolbar or from a session's
 * address, which it looks up straight away.
 */
function IPLookupDialog({
  open,
  onOpenChange,
  seed,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  seed: string;
}) {
  const t = useT();
  const [ipSearch, setIPSearch] = useState(seed);
  const [activeIP, setActiveIP] = useState(seed);
  const [seen, setSeen] = useState({ open, seed });
  if (seen.open !== open || seen.seed !== seed) {
    // A new opening starts from the address it was opened for.
    setSeen({ open, seed });
    if (open) {
      setIPSearch(seed);
      setActiveIP(seed);
    }
  }
  const ipHistory = useIPUsers(activeIP);
  const { data: ipUsers = [], isLoading: ipLoading } = ipHistory;
  const reloadHistoryButton = (
    <Button onClick={() => void ipHistory.restart()}>
      {t("web.admin.activity.ipLookup.reload")}
    </Button>
  );

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t("web.admin.activity.ipLookup.heading")}</DialogTitle>
        </DialogHeader>
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
            aria-label={t("web.admin.activity.ipLookup.placeholder")}
            dir={ipSearch ? "ltr" : undefined}
            value={ipSearch}
            onChange={(e) => setIPSearch(e.target.value)}
            className="placeholder:font-ui max-w-xs font-mono text-sm"
          />
          <Button type="submit" variant="outline" size="sm" disabled={!ipSearch.trim()}>
            <Search className="me-1 h-3.5 w-3.5" />
            {t("web.admin.activity.ipLookup.submit")}
          </Button>
        </form>
        {activeIP && (
          <div className="max-h-[60vh] overflow-y-auto">
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
                      <TableCell className="text-sm">{formatDateTime(entry.first_seen)}</TableCell>
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
      </DialogContent>
    </Dialog>
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
      type="button"
      onClick={() => onClick(field)}
      className={`text-muted-foreground flex items-center gap-1 text-[0.75rem] font-medium transition-colors ${
        active ? "text-foreground" : "hover:text-foreground"
      } ${className}`}
    >
      {children}
      {active && (
        <span aria-hidden="true" className="text-[0.5625rem]">
          {dir === "asc" ? SORT_ASC_MARK : SORT_DESC_MARK}
        </span>
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
  if (isEpisodeSession(session)) {
    return session.series_title;
  }
  return session.media_title || t("web.common.activity.title.file", { id: session.media_file_id });
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

function stringAttr(entry: OperationalLogEntry, key: string) {
  const value = entry.attrs?.[key];
  if (typeof value === "string" && value.length > 0) return value;
  if (typeof value === "number") return String(value);
  return "-";
}
