import type { ReactNode } from "react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate } from "react-router";
import { AdminSessionActions } from "@/components/AdminSessionActions";
import { AdminSectionCommandDialog } from "@/components/AdminSectionCommandDialog";
import { useEventChannel } from "@/components/realtimeEventsContext";
import { fetchAdminStats, useAdminStats, useAdminSessions } from "@/hooks/queries/admin/stats";
import { useAdminPluginInstallations } from "@/hooks/queries/admin/plugins";
import { usePolicyCapability } from "@/hooks/queries/admin/policy";
import { useAdminUsers } from "@/hooks/queries/admin/users";
import {
  useAdminLibraries,
  useCancelLibraryScans,
  useScanAllLibraries,
  useScanLibrary,
} from "@/hooks/queries/admin/libraries";
import { useActiveScans } from "@/hooks/queries/admin/scans";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Activity,
  Film,
  Tv,
  Users,
  HardDrive,
  RefreshCw,
  Play,
  Pause,
  Library,
  ScanLine,
  Square,
} from "lucide-react";
import { Skeleton } from "@/components/ui/skeleton";
import type {
  AdminSession,
  AdminStats,
  Library as LibraryType,
  AdminUser,
  ScanRun,
  WatchProviderActivity,
} from "@/api/types";
import { useQueryClient } from "@tanstack/react-query";
import { adminKeys } from "@/hooks/queries/keys";
import { usePageActivity } from "@/hooks/usePageActivity";
import { cn } from "@/lib/utils";
import { buildAdminCommandNavSections } from "@/lib/adminNavigation";
import { compareActiveScans, formatActiveScanMode, formatActiveScanProgress } from "@/lib/scanRuns";
import { getSessionClientLabel } from "@/pages/adminActivityPresentation";

const REFRESH_SPINNER_MIN_VISIBLE_MS = 1_000;
const DASHBOARD_AUTO_REFRESH_MS = 60_000;
const RELATIVE_UPDATED_LABEL_TICK_MS = 30_000;

function formatFileCount(count: number | null | undefined) {
  if (count == null) {
    return "—";
  }
  return count === 1 ? "1 file" : `${count.toLocaleString()} files`;
}

function formatDashboardLibraryScanProgress(scan: ScanRun, activeScanCount: number) {
  const status = scan.status === "running" ? "Scanning" : "Queued";
  const progress = formatActiveScanProgress(scan);
  const detail =
    progress || (scan.status === "running" ? formatActiveScanMode(scan) : "Waiting for capacity");
  const extraScans = activeScanCount > 1 ? ` + ${activeScanCount - 1} more` : "";
  return `${status}: ${detail}${extraScans}`;
}

export default function AdminDashboard() {
  const queryClient = useQueryClient();
  const statsQuery = useAdminStats();
  const sessionsQuery = useAdminSessions();
  const librariesQuery = useAdminLibraries();
  const usersQuery = useAdminUsers();
  const { data: adminInstallations } = useAdminPluginInstallations();
  const policyCapability = usePolicyCapability();
  const scanAll = useScanAllLibraries();
  const pageActivity = usePageActivity();
  const manualRefreshStartedAtRef = useRef<number | null>(null);
  const wasDashboardPollingPausedRef = useRef(!pageActivity.canPollDashboard);
  const [isManualRefreshPending, setIsManualRefreshPending] = useState(false);
  const [lastDashboardUpdatedAt, setLastDashboardUpdatedAt] = useState<number | null>(null);
  const [relativeUpdatedNow, setRelativeUpdatedNow] = useState(() => Date.now());

  const sessions = sessionsQuery.data ?? [];
  const libraries = librariesQuery.data ?? [];
  const users = usersQuery.data ?? [];
  const { refetch: refetchSessions } = sessionsQuery;
  const { refetch: refetchLibraries } = librariesQuery;
  const { refetch: refetchUsers } = usersQuery;
  const hasDashboardData =
    statsQuery.data !== undefined &&
    sessionsQuery.data !== undefined &&
    librariesQuery.data !== undefined &&
    usersQuery.data !== undefined;
  const hasStaleDashboardData =
    statsQuery.isStale || sessionsQuery.isStale || librariesQuery.isStale || usersQuery.isStale;
  const dashboardDataUpdatedAt = Math.max(
    statsQuery.dataUpdatedAt,
    sessionsQuery.dataUpdatedAt,
    librariesQuery.dataUpdatedAt,
    usersQuery.dataUpdatedAt,
  );
  const lastUpdatedLabel = lastDashboardUpdatedAt
    ? formatRelativeUpdatedLabel(relativeUpdatedNow, lastDashboardUpdatedAt)
    : null;
  const adminSearchSections = useMemo(
    () =>
      buildAdminCommandNavSections(adminInstallations, {
        policyEditorAvailable: policyCapability.data?.editor_available === true,
      }),
    [adminInstallations, policyCapability.data?.editor_available],
  );

  useEffect(() => {
    if (!lastDashboardUpdatedAt) {
      return;
    }

    const interval = window.setInterval(() => {
      setRelativeUpdatedNow(Date.now());
    }, RELATIVE_UPDATED_LABEL_TICK_MS);

    return () => {
      window.clearInterval(interval);
    };
  }, [lastDashboardUpdatedAt]);

  useEffect(() => {
    if (hasDashboardData && dashboardDataUpdatedAt > 0) {
      setLastDashboardUpdatedAt(dashboardDataUpdatedAt);
    }
  }, [dashboardDataUpdatedAt, hasDashboardData]);

  const refreshDashboard = useCallback(
    async ({ manual }: { manual: boolean }) => {
      if (manual) {
        manualRefreshStartedAtRef.current = Date.now();
        setIsManualRefreshPending(true);
      }
      try {
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: adminKeys.stats(), refetchType: "none" }),
          queryClient.invalidateQueries({ queryKey: adminKeys.sessions(), refetchType: "none" }),
          queryClient.invalidateQueries({ queryKey: adminKeys.libraries(), refetchType: "none" }),
          queryClient.invalidateQueries({ queryKey: adminKeys.users(), refetchType: "none" }),
        ]);
        const nextStats = await fetchAdminStats({ refresh: true });
        queryClient.setQueryData(adminKeys.stats(), nextStats);
        await Promise.all([refetchSessions(), refetchLibraries(), refetchUsers()]);
        const refreshedAt = Date.now();
        setLastDashboardUpdatedAt(refreshedAt);
        setRelativeUpdatedNow(refreshedAt);
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
    [queryClient, refetchLibraries, refetchSessions, refetchUsers],
  );

  useEffect(() => {
    if (!pageActivity.canPollDashboard) {
      wasDashboardPollingPausedRef.current = true;
      return;
    }
    if (isManualRefreshPending) {
      return;
    }
    const resumedPolling = wasDashboardPollingPausedRef.current;
    wasDashboardPollingPausedRef.current = false;
    if (
      resumedPolling &&
      hasDashboardData &&
      (hasStaleDashboardData ||
        !lastDashboardUpdatedAt ||
        Date.now() - lastDashboardUpdatedAt >= DASHBOARD_AUTO_REFRESH_MS)
    ) {
      void refreshDashboard({ manual: true });
      return;
    }

    if (
      lastDashboardUpdatedAt &&
      Date.now() - lastDashboardUpdatedAt >= DASHBOARD_AUTO_REFRESH_MS
    ) {
      void refreshDashboard({ manual: false });
    }

    const interval = window.setInterval(() => {
      void refreshDashboard({ manual: false });
    }, DASHBOARD_AUTO_REFRESH_MS);

    return () => {
      window.clearInterval(interval);
    };
  }, [
    hasDashboardData,
    hasStaleDashboardData,
    isManualRefreshPending,
    lastDashboardUpdatedAt,
    pageActivity.canPollDashboard,
    refreshDashboard,
  ]);

  return (
    <div className="space-y-6 lg:space-y-8">
      <AdminSectionCommandDialog sections={adminSearchSections} />

      {/* Page header */}
      <div className="page-header">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3.25rem)]">Dashboard</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Live sessions, content health, and server activity in one view.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <div className="flex items-center gap-2">
            {lastUpdatedLabel && (
              <span className="text-muted-foreground text-xs whitespace-nowrap">
                Updated {lastUpdatedLabel}
              </span>
            )}
            <Button
              variant="outline"
              size="sm"
              className="min-w-[8.25rem] justify-center"
              onClick={() => {
                void refreshDashboard({ manual: true });
              }}
              disabled={isManualRefreshPending}
              aria-busy={isManualRefreshPending}
            >
              <RefreshCw
                className={`h-3.5 w-3.5 ${isManualRefreshPending ? "animate-spin" : ""}`}
              />
              {isManualRefreshPending ? "Refreshing..." : "Refresh"}
            </Button>
          </div>
          <Button
            variant="default"
            size="sm"
            className="cursor-pointer"
            onClick={() => {
              if (libraries.length > 0) {
                scanAll.mutate();
              }
            }}
            disabled={scanAll.isPending || libraries.length === 0}
          >
            <ScanLine className="h-3.5 w-3.5" />
            Scan All Libraries
          </Button>
        </div>
      </div>

      <StatsRow
        stats={statsQuery.data}
        sessionCount={sessions.length}
        isLoading={statsQuery.isLoading}
        error={statsQuery.error}
      />

      {statsQuery.data?.watch_provider_activity && (
        <TraktActivityCard activity={statsQuery.data.watch_provider_activity} />
      )}

      <NowPlayingSection
        sessions={sessions}
        isLoading={sessionsQuery.isLoading}
        error={sessionsQuery.error}
      />

      <div className="grid grid-cols-1 gap-3.5 xl:grid-cols-[1.4fr_1fr]">
        <LibrariesCard
          libraries={libraries}
          isLoading={librariesQuery.isLoading}
          error={librariesQuery.error}
        />
        <UsersCard users={users} isLoading={usersQuery.isLoading} error={usersQuery.error} />
      </div>

      <ActivityCard
        sessions={sessions}
        isLoading={sessionsQuery.isLoading}
        error={sessionsQuery.error}
      />
    </div>
  );
}

// --- Sub-components ---

function StatsRow({
  stats,
  sessionCount,
  isLoading,
  error,
}: {
  stats: AdminStats | undefined;
  sessionCount: number;
  isLoading: boolean;
  error: unknown;
}) {
  if (isLoading || !stats) {
    if (error) {
      return <SectionError message="Failed to load stats." />;
    }
    return (
      <div className="grid grid-cols-2 gap-3.5 sm:grid-cols-3 lg:grid-cols-5">
        {Array.from({ length: 5 }).map((_, i) => (
          <Skeleton key={i} className="h-24 rounded-2xl" />
        ))}
      </div>
    );
  }

  const storageGB = stats.total_storage_bytes / (1024 * 1024 * 1024);
  const storageTB = storageGB / 1024;
  const storageDisplay =
    storageTB >= 1 ? `${storageTB.toFixed(1)} TB` : `${storageGB.toFixed(0)} GB`;

  const statCards: { label: string; value: string; sub: string; icon: ReactNode }[] = [
    {
      label: "Active Streams",
      value: String(sessionCount),
      sub: sessionCount === 1 ? "1 session" : `${sessionCount} sessions`,
      icon: <Activity className="h-4 w-4" />,
    },
    {
      label: "Total Movies",
      value: stats.total_movies.toLocaleString(),
      sub: formatFileCount(stats.total_movie_files),
      icon: <Film className="h-4 w-4" />,
    },
    {
      label: "Total Shows",
      value: stats.total_shows.toLocaleString(),
      sub: formatFileCount(stats.total_show_files),
      icon: <Tv className="h-4 w-4" />,
    },
    {
      label: "Users",
      value: String(stats.total_users),
      sub: `${stats.total_users} registered`,
      icon: <Users className="h-4 w-4" />,
    },
    {
      label: "Storage",
      value: storageDisplay,
      sub: formatFileCount(stats.total_files),
      icon: <HardDrive className="h-4 w-4" />,
    },
  ];

  return (
    <div className="grid grid-cols-2 gap-3.5 sm:grid-cols-3 lg:grid-cols-5">
      {statCards.map((card) => (
        <div
          key={card.label}
          className="surface-panel rounded-2xl border-0 p-[18px] transition-colors duration-150"
        >
          <div className="mb-2 flex items-center justify-between">
            <div className="text-muted-foreground text-[11px] font-medium">{card.label}</div>
            <div className="text-muted-foreground">{card.icon}</div>
          </div>
          <div className="mb-0.5 text-[28px] leading-none font-extrabold tracking-tight">
            {card.value}
          </div>
          <div className="text-muted-foreground text-[11px]">{card.sub}</div>
        </div>
      ))}
    </div>
  );
}

function TraktActivityCard({ activity }: { activity: WatchProviderActivity }) {
  const hasActivity =
    activity.trakt_connected_profiles > 0 ||
    activity.sync_runs_24h > 0 ||
    activity.pending_exports > 0 ||
    activity.open_scrobbles > 0;

  if (!hasActivity) return null;

  const lastSync = activity.last_sync_completed_at
    ? getTimeAgo(activity.last_sync_completed_at)
    : "Never";

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-3">
        <CardTitle className="text-sm font-bold">Trakt Activity</CardTitle>
        <Link
          to="/admin/tasks/sync_watch_providers"
          className="text-muted-foreground hover:text-primary text-[11px] transition-colors"
        >
          Task details ›
        </Link>
      </CardHeader>
      <CardContent>
        <div className="grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
          <TraktMetric
            label="Connected"
            value={activity.trakt_connected_profiles.toLocaleString()}
            detail={`${activity.trakt_enabled_profiles.toLocaleString()} enabled`}
          />
          <TraktMetric
            label="Last sync"
            value={lastSync}
            detail={`${activity.sync_runs_24h.toLocaleString()} runs in 24h`}
          />
          <TraktMetric
            label="Imported"
            value={activity.imported_watched_24h.toLocaleString()}
            detail={`${activity.imported_progress_24h.toLocaleString()} progress updates`}
          />
          <TraktMetric
            label="Exported"
            value={activity.exported_watched_24h.toLocaleString()}
            detail={`${activity.pending_exports.toLocaleString()} pending`}
          />
        </div>
        <div className="border-border/60 mt-3 grid gap-2 border-t pt-3 text-xs sm:grid-cols-3">
          <div className="text-muted-foreground">
            Export enabled:{" "}
            <span className="text-foreground font-medium">
              {activity.trakt_export_enabled.toLocaleString()}
            </span>
          </div>
          <div className="text-muted-foreground">
            Scrobbling:{" "}
            <span className="text-foreground font-medium">
              {activity.trakt_scrobble_enabled.toLocaleString()}
            </span>
          </div>
          <div className="text-muted-foreground">
            Errors:{" "}
            <span
              className={
                activity.sync_errors_24h + activity.failed_exports > 0
                  ? "text-destructive font-medium"
                  : "text-foreground font-medium"
              }
            >
              {(activity.sync_errors_24h + activity.failed_exports).toLocaleString()}
            </span>
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

function TraktMetric({ label, value, detail }: { label: string; value: string; detail: string }) {
  return (
    <div className="border-border/60 rounded-lg border p-3">
      <div className="text-muted-foreground text-xs">{label}</div>
      <div className="mt-1 text-xl font-bold tracking-tight">{value}</div>
      <div className="text-muted-foreground mt-0.5 text-xs">{detail}</div>
    </div>
  );
}

function StreamCard({ session }: { session: AdminSession }) {
  const isEpisode =
    session.series_name && session.season_number != null && session.episode_number != null;
  const title = isEpisode
    ? session.episode_name || `S${session.season_number}E${session.episode_number}`
    : session.media_title || `File #${session.media_file_id}`;
  const username = session.username || `User #${session.user_id}`;
  const elapsed = getTimeAgo(session.started_at);
  const clientLabel = getSessionClientLabel(session);
  const methodColor =
    session.play_method === "direct"
      ? "bg-success/10 text-success border-success/15"
      : session.play_method === "remux"
        ? "bg-info/10 text-info border-info/15"
        : "bg-warning/10 text-warning border-warning/15";

  return (
    <div className="surface-panel flex gap-3.5 rounded-2xl border-0 p-3.5 transition-colors duration-150">
      {/* Poster */}
      <div
        className="bg-surface border-border relative flex w-[70px] flex-shrink-0 items-center justify-center overflow-hidden rounded-lg border"
        style={{ aspectRatio: "2/3" }}
      >
        {session.poster_url ? (
          <img
            src={session.poster_url}
            alt={session.media_title}
            className={`h-full w-full object-cover transition-opacity ${session.is_paused ? "opacity-45" : ""}`}
          />
        ) : (
          <Play className={`text-primary/40 h-5 w-5 ${session.is_paused ? "opacity-45" : ""}`} />
        )}
        {session.is_paused ? (
          <div className="absolute inset-0 flex items-center justify-center bg-black/35">
            <div className="border-border/40 bg-background/90 text-foreground inline-flex items-center gap-1 rounded-full border px-2 py-1 text-[10px] font-semibold shadow-sm backdrop-blur">
              <Pause className="h-3 w-3" />
              Paused
            </div>
          </div>
        ) : null}
      </div>

      {/* Info */}
      <div className="flex min-w-0 flex-1 flex-col">
        {isEpisode ? (
          <>
            {session.content_id ? (
              <Link
                to={`/item/${session.content_id}`}
                className="hover:text-primary truncate text-sm font-bold transition-colors"
              >
                {title}
              </Link>
            ) : (
              <div className="truncate text-sm font-bold">{title}</div>
            )}
            <div className="text-muted-foreground mb-1.5 text-xs">
              S{session.season_number} · E{session.episode_number}
              {session.series_name ? ` — ${session.series_name}` : ""}
            </div>
          </>
        ) : (
          <>
            {session.content_id ? (
              <Link
                to={`/item/${session.content_id}`}
                className="hover:text-primary truncate text-sm font-bold transition-colors"
              >
                {title}
              </Link>
            ) : (
              <div className="truncate text-sm font-bold">{title}</div>
            )}
            {session.media_type && (
              <div className="text-muted-foreground mb-1.5 text-xs">
                {session.media_type === "movie" ? "Movie" : "Series"}
              </div>
            )}
          </>
        )}

        {/* Tags */}
        <div className="mb-1.5 flex flex-wrap gap-1">
          <span
            className={`inline-flex rounded border px-1.5 py-0.5 text-[9px] font-semibold ${methodColor}`}
          >
            {session.play_method || "unknown"}
          </span>
          {clientLabel ? (
            <span
              title={session.client_user_agent || clientLabel}
              className="border-border/60 bg-muted/30 text-muted-foreground inline-flex max-w-[9rem] rounded border px-1.5 py-0.5 text-[9px] font-semibold"
            >
              <span className="truncate">{clientLabel}</span>
            </span>
          ) : null}
          {session.reporting_node && (
            <span className="border-primary/10 bg-primary/5 text-primary inline-flex rounded border px-1.5 py-0.5 text-[9px] font-semibold">
              {session.node_display_name || session.reporting_node}
            </span>
          )}
          {(session.profile_name || session.profile_id) && (
            <SessionProfilePill label={session.profile_name || session.profile_id} />
          )}
        </div>

        {/* User */}
        <div className="mt-auto flex items-center gap-1.5">
          <div
            className="text-primary-foreground flex h-[22px] w-[22px] items-center justify-center rounded-full text-[9px] font-bold"
            style={{ background: `var(--primary)` }}
          >
            {username.charAt(0).toUpperCase()}
          </div>
          <span className="text-xs font-medium">{username}</span>
          <span className="text-muted-foreground ml-auto text-[10px]">{elapsed}</span>
        </div>
      </div>
    </div>
  );
}

function SessionProfilePill({ label }: { label: string }) {
  return (
    <span className="border-primary/30 bg-primary/15 text-primary inline-flex max-w-full shrink-0 items-center rounded border px-1.5 py-0.5 align-middle text-[9px] leading-[1.1] font-semibold whitespace-nowrap">
      {label}
    </span>
  );
}

function NowPlayingSection({
  sessions,
  isLoading,
  error,
}: {
  sessions: AdminSession[];
  isLoading: boolean;
  error: unknown;
}) {
  if (error) return null;

  if (isLoading) {
    return (
      <div>
        <div className="mb-3 flex items-center justify-between">
          <div className="text-base font-bold">Now Playing</div>
        </div>
        <div className="grid grid-cols-1 gap-3.5 lg:grid-cols-2">
          {Array.from({ length: 2 }).map((_, i) => (
            <Skeleton key={i} className="h-[120px] rounded-2xl" />
          ))}
        </div>
      </div>
    );
  }

  if (sessions.length === 0) return null;

  return (
    <div>
      <div className="mb-3 flex items-center justify-between">
        <div className="text-base font-bold">Now Playing</div>
        <Link
          to="/admin/activity"
          className="text-muted-foreground hover:text-primary text-[11px] transition-colors"
        >
          View all {sessions.length} streams ›
        </Link>
      </div>
      <div className="grid grid-cols-1 gap-3.5 lg:grid-cols-2">
        {sessions.slice(0, 4).map((session) => (
          <StreamCard key={session.session_id} session={session} />
        ))}
      </div>
      {sessions.length > 4 && (
        <Link
          to="/admin/activity"
          className="text-muted-foreground hover:text-primary mt-2 block text-center text-[12px] transition-colors"
        >
          +{sessions.length - 4} more active streams
        </Link>
      )}
    </div>
  );
}

function LibrariesCard({
  libraries,
  isLoading,
  error,
}: {
  libraries: LibraryType[];
  isLoading: boolean;
  error: unknown;
}) {
  useEventChannel("scans");
  const scanLibrary = useScanLibrary();
  const cancelScans = useCancelLibraryScans();
  const { data: activeScans = [] } = useActiveScans();

  const activeScansByLibraryId = useMemo(() => {
    const scansByLibraryID = new Map<number, ScanRun[]>();
    for (const scan of activeScans) {
      if (scan.status !== "accepted" && scan.status !== "running") {
        continue;
      }
      const scans = scansByLibraryID.get(scan.library_id) ?? [];
      scans.push(scan);
      scansByLibraryID.set(scan.library_id, scans);
    }
    for (const scans of scansByLibraryID.values()) {
      scans.sort(compareActiveScans);
    }
    return scansByLibraryID;
  }, [activeScans]);

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-3">
        <CardTitle className="text-sm font-bold">Libraries</CardTitle>
        <Link
          to="/admin/libraries"
          className="text-muted-foreground hover:text-primary text-[11px] transition-colors"
        >
          Manage ›
        </Link>
      </CardHeader>
      <CardContent className="space-y-2">
        {isLoading ? (
          <LibrarySkeletonRows />
        ) : error ? (
          <SectionError message="Failed to load libraries." />
        ) : libraries.length === 0 ? (
          <div className="text-muted-foreground py-4 text-center text-sm">
            No libraries configured.
          </div>
        ) : (
          libraries.map((lib) => {
            const activeLibraryScans = activeScansByLibraryId.get(lib.id) ?? [];
            const primaryActiveScan = activeLibraryScans[0];
            const hasActiveScan = activeLibraryScans.length > 0;
            const isScanStarting = scanLibrary.isPending && scanLibrary.variables === lib.id;
            const isCancellingScan = cancelScans.isPending && cancelScans.variables === lib.id;
            const scanProgressLabel = primaryActiveScan
              ? formatDashboardLibraryScanProgress(primaryActiveScan, activeLibraryScans.length)
              : isScanStarting
                ? "Starting scan..."
                : "";

            return (
              <div
                key={lib.id}
                className="bg-surface border-border hover:bg-surface-hover flex items-center gap-3 rounded-md border p-3 transition-colors duration-150"
              >
                {lib.poster_url ? (
                  <img
                    src={lib.poster_url}
                    alt={lib.name}
                    className="border-border h-8 w-14 flex-shrink-0 rounded border object-cover"
                  />
                ) : (
                  <div className="bg-primary/5 border-primary/10 flex h-10 w-10 flex-shrink-0 items-center justify-center rounded-lg border">
                    <Library className="text-primary h-4 w-4" />
                  </div>
                )}
                <div className="min-w-0 flex-1">
                  <div className="text-sm font-bold">{lib.name}</div>
                  <div className="text-muted-foreground flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-[11px]">
                    <span>
                      {lib.type} · {lib.paths.length} {lib.paths.length === 1 ? "path" : "paths"}
                    </span>
                    {scanProgressLabel ? (
                      <>
                        <span className="text-border/70">·</span>
                        <span className="max-w-[22rem] truncate text-amber-300 tabular-nums">
                          {scanProgressLabel}
                        </span>
                      </>
                    ) : null}
                  </div>
                </div>
                <div className="flex flex-shrink-0 items-center gap-2">
                  <Button
                    variant={hasActiveScan ? "destructive" : "ghost"}
                    size="icon"
                    className="h-7 w-7 cursor-pointer"
                    onClick={(e) => {
                      e.stopPropagation();
                      if (hasActiveScan) {
                        cancelScans.mutate(lib.id);
                        return;
                      }
                      scanLibrary.mutate(lib.id);
                    }}
                    disabled={hasActiveScan ? isCancellingScan : isScanStarting}
                    title={hasActiveScan ? "Stop Library Scans" : "Scan Library"}
                    aria-label={hasActiveScan ? `Stop scans for ${lib.name}` : `Scan ${lib.name}`}
                  >
                    {hasActiveScan ? (
                      <Square className="h-3 w-3 fill-current" />
                    ) : (
                      <ScanLine className={cn("h-3 w-3", isScanStarting && "animate-pulse")} />
                    )}
                  </Button>
                  <div
                    className={cn(
                      "h-2 w-2 rounded-full",
                      hasActiveScan || isScanStarting
                        ? "bg-amber-400 shadow-[0_0_6px_rgba(251,191,36,0.65)]"
                        : lib.enabled
                          ? "bg-green-500"
                          : "bg-muted-foreground/30",
                      hasActiveScan && "animate-pulse",
                    )}
                    title={
                      hasActiveScan
                        ? "Scan in progress"
                        : lib.enabled
                          ? "Library enabled"
                          : "Library disabled"
                    }
                  />
                </div>
              </div>
            );
          })
        )}
      </CardContent>
    </Card>
  );
}

function UsersCard({
  users,
  isLoading,
  error,
}: {
  users: AdminUser[];
  isLoading: boolean;
  error: unknown;
}) {
  const navigate = useNavigate();

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-3">
        <CardTitle className="text-sm font-bold">Users</CardTitle>
        <Link
          to="/admin/users"
          className="text-muted-foreground hover:text-primary text-[11px] transition-colors"
        >
          Manage ›
        </Link>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <UserSkeletonRows />
        ) : error ? (
          <SectionError message="Failed to load users." />
        ) : users.length === 0 ? (
          <div className="text-muted-foreground py-4 text-center text-sm">No users.</div>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>User</TableHead>
                <TableHead>Role</TableHead>
                <TableHead>Status</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {users.slice(0, 8).map((u) => (
                <TableRow
                  key={u.id}
                  className="hover:bg-accent/50 cursor-pointer"
                  onClick={() => navigate(`/admin/users/${u.id}`)}
                >
                  <TableCell>
                    <div className="flex items-center gap-2.5">
                      <div
                        className="text-primary-foreground flex h-7 w-7 flex-shrink-0 items-center justify-center rounded-full text-[10px] font-bold"
                        style={{ background: `var(--primary)` }}
                      >
                        {u.username.charAt(0).toUpperCase()}
                      </div>
                      <div>
                        <div className="text-[13px] font-semibold">{u.username}</div>
                        <div className="text-muted-foreground text-[10px]">{u.email}</div>
                      </div>
                    </div>
                  </TableCell>
                  <TableCell>
                    <Badge variant={u.role === "admin" ? "default" : "secondary"}>{u.role}</Badge>
                  </TableCell>
                  <TableCell>
                    <Badge variant={u.enabled ? "outline" : "destructive"}>
                      {u.enabled ? "Active" : "Disabled"}
                    </Badge>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

function ActivityCard({
  sessions,
  isLoading,
  error,
}: {
  sessions: AdminSession[];
  isLoading: boolean;
  error: unknown;
}) {
  if (!isLoading && !error && sessions.length === 0) return null;

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-3">
        <CardTitle className="text-sm font-bold">Recent Activity</CardTitle>
        <Link
          to="/admin/activity"
          className="text-muted-foreground hover:text-primary text-[11px] transition-colors"
        >
          View all ›
        </Link>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <ActivitySkeletonRows />
        ) : error ? (
          <SectionError message="Failed to load activity." />
        ) : (
          <div className="space-y-0">
            {sessions.slice(0, 10).map((s) => {
              const isEp = s.series_name && s.season_number != null && s.episode_number != null;
              const title = isEp
                ? s.episode_name || `S${s.season_number}E${s.episode_number}`
                : s.media_title || `File #${s.media_file_id}`;
              const username = s.username || `User #${s.user_id}`;
              const profileDisplay = s.profile_name || s.profile_id || "";
              const clientLabel = getSessionClientLabel(s);
              const meta = [getTimeAgo(s.started_at), clientLabel].filter(Boolean).join(" · ");
              return (
                <div
                  key={s.session_id}
                  className="border-border/30 flex items-start gap-3 border-b py-2.5"
                >
                  <div className="text-primary bg-primary/5 border-primary/10 flex h-[30px] w-[30px] flex-shrink-0 items-center justify-center rounded-lg border">
                    <Play className="h-3.5 w-3.5" />
                  </div>
                  <div className="min-w-0 flex-1">
                    <div className="text-muted-foreground text-xs leading-relaxed">
                      <span className="text-foreground font-semibold">{username}</span>
                      {profileDisplay ? (
                        <>
                          {" "}
                          <SessionProfilePill label={profileDisplay} />
                        </>
                      ) : null}
                      {" started watching "}
                      <Link
                        to={`/admin/history?user_id=${s.user_id}${s.profile_id ? `&profile_id=${encodeURIComponent(s.profile_id)}` : ""}`}
                        className="text-foreground hover:text-primary font-semibold transition-colors"
                      >
                        {title}
                      </Link>
                    </div>
                    <div className="text-muted-foreground mt-0.5 text-[10px]">{meta}</div>
                  </div>
                  <div className="flex-shrink-0">
                    <AdminSessionActions session={s} compact />
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// --- Helpers ---

function getTimeAgo(dateStr: string): string {
  const now = Date.now();
  const then = new Date(dateStr).getTime();
  const diff = Math.max(0, now - then);
  const minutes = Math.floor(diff / 60000);
  if (minutes < 1) return "Just now";
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}

function formatRelativeUpdatedLabel(now: number, updatedAt: number) {
  const elapsedMinutes = Math.floor(Math.max(0, now - updatedAt) / 60_000);
  if (elapsedMinutes < 1) {
    return "less than 1 minute ago";
  }
  if (elapsedMinutes === 1) {
    return "1 minute ago";
  }
  return `${elapsedMinutes.toLocaleString()} minutes ago`;
}

function delay(ms: number) {
  return new Promise<void>((resolve) => {
    window.setTimeout(resolve, ms);
  });
}

function SectionError({ message }: { message: string }) {
  return <div className="text-destructive py-4 text-center text-sm">{message}</div>;
}

function LibrarySkeletonRows() {
  return (
    <>
      {Array.from({ length: 3 }).map((_, i) => (
        <Skeleton key={i} className="h-[60px] rounded-md" />
      ))}
    </>
  );
}

function UserSkeletonRows() {
  return (
    <div className="space-y-2">
      {Array.from({ length: 4 }).map((_, i) => (
        <Skeleton key={i} className="h-10 rounded-md" />
      ))}
    </div>
  );
}

function ActivitySkeletonRows() {
  return (
    <div className="space-y-0">
      {Array.from({ length: 4 }).map((_, i) => (
        <div key={i} className="border-border/30 flex items-start gap-3 border-b py-2.5">
          <Skeleton className="h-[30px] w-[30px] rounded-lg" />
          <div className="min-w-0 flex-1 space-y-1.5">
            <Skeleton className="h-3 w-3/4 rounded" />
            <Skeleton className="h-2 w-1/4 rounded" />
          </div>
        </div>
      ))}
    </div>
  );
}
