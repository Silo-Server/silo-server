import type { ReactNode } from "react";
import { Activity, Film, Gauge, HardDrive, Tv, UserCheck, Users, Zap } from "lucide-react";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminSessions, useAdminStats } from "@/hooks/queries/admin/stats";
import {
  useAdminPlaybackActivity,
  useAdminTimeseries,
} from "@/hooks/queries/admin/dashboardInsights";
import { classifyActivityMethod } from "@/pages/adminActivityPresentation";
import { formatFileCount, formatMbps } from "../format";
import { latestFreshPoint } from "./timeseriesSeries";

function StatTile({
  label,
  value,
  sub,
  icon,
  isLoading,
  error,
}: {
  label: string;
  value: string;
  sub: string;
  icon: ReactNode;
  isLoading: boolean;
  error: unknown;
}) {
  if (isLoading) {
    return <Skeleton className="h-full min-h-24 rounded-2xl" />;
  }

  return (
    // A stat tile is one grid row tall, so its content is centered in whatever
    // height the row gives it and the padding is trimmed to the ~96px the
    // loading skeleton has always reserved.
    <div className="surface-panel flex h-full flex-col justify-center overflow-hidden rounded-2xl border-0 p-4 transition-colors duration-150">
      <div className="mb-1.5 flex items-center justify-between">
        <div className="text-muted-foreground text-[11px] leading-none font-medium">{label}</div>
        <div className="text-muted-foreground">{icon}</div>
      </div>
      {error ? (
        <div className="text-destructive text-sm">Unavailable</div>
      ) : (
        <>
          <div className="mb-0.5 text-[28px] leading-none font-extrabold tracking-tight">
            {value}
          </div>
          <div className="text-muted-foreground text-[11px]">{sub}</div>
        </>
      )}
    </div>
  );
}

export function ActiveStreamsStatWidget() {
  const sessionsQuery = useAdminSessions();
  const sessionCount = sessionsQuery.data?.length ?? 0;
  return (
    <StatTile
      label="Active Streams"
      value={String(sessionCount)}
      sub={sessionCount === 1 ? "1 session" : `${sessionCount} sessions`}
      icon={<Activity className="h-4 w-4" />}
      isLoading={sessionsQuery.isLoading}
      error={sessionsQuery.error}
    />
  );
}

// The sampler writes one row a minute, so a sample older than two minutes means
// the sampler is behind (or stopped) — show nothing rather than a stale rate
// presented as the current one.
const EGRESS_SAMPLE_MAX_AGE_MS = 2 * 60_000;

export function EgressNowStatWidget() {
  const timeseriesQuery = useAdminTimeseries(1);
  const latest = latestFreshPoint(timeseriesQuery.data, EGRESS_SAMPLE_MAX_AGE_MS);
  return (
    <StatTile
      label="Egress now"
      value={latest ? formatMbps(latest.egress_kbps / 1_000) : "—"}
      sub={latest ? "node + server egress" : "waiting for a sample"}
      icon={<Gauge className="h-4 w-4" />}
      isLoading={timeseriesQuery.isLoading}
      error={timeseriesQuery.error}
    />
  );
}

export function TranscodeShareStatWidget() {
  const sessionsQuery = useAdminSessions();
  const sessions = sessionsQuery.data ?? [];
  // Same reduction the activity page uses: the server-computed
  // effective_play_method when present, otherwise the per-stream decisions.
  // Audio-only transcodes stay out of the count — this tile is about the
  // sessions burning video encode capacity.
  const transcoding = sessions.filter(
    (session) => classifyActivityMethod(session) === "transcode",
  ).length;
  const share = sessions.length > 0 ? Math.round((transcoding / sessions.length) * 100) : null;
  return (
    <StatTile
      label="Transcode share"
      value={share === null ? "—" : `${share}%`}
      sub={
        sessions.length > 0
          ? `${transcoding.toLocaleString()} of ${sessions.length.toLocaleString()} streams`
          : "no active streams"
      }
      icon={<Zap className="h-4 w-4" />}
      isLoading={sessionsQuery.isLoading}
      error={sessionsQuery.error}
    />
  );
}

export function ProfilesActiveStatWidget() {
  const activityQuery = useAdminPlaybackActivity(24);
  const profiles = activityQuery.data?.profiles_active_24h;
  return (
    <StatTile
      // A rolling 24h window, not "today": the server has no user timezone, so
      // the label says 24h rather than implying a calendar day.
      label="Profiles · 24h"
      value={profiles === undefined ? "—" : profiles.toLocaleString()}
      sub="watched on this server"
      icon={<UserCheck className="h-4 w-4" />}
      isLoading={activityQuery.isLoading}
      error={activityQuery.error}
    />
  );
}

export function MoviesStatWidget() {
  const statsQuery = useAdminStats();
  const stats = statsQuery.data;
  return (
    <StatTile
      label="Total Movies"
      value={stats ? stats.total_movies.toLocaleString() : "—"}
      sub={formatFileCount(stats?.total_movie_files)}
      icon={<Film className="h-4 w-4" />}
      isLoading={statsQuery.isLoading || (!stats && !statsQuery.error)}
      error={statsQuery.error}
    />
  );
}

export function ShowsStatWidget() {
  const statsQuery = useAdminStats();
  const stats = statsQuery.data;
  return (
    <StatTile
      label="Total Shows"
      value={stats ? stats.total_shows.toLocaleString() : "—"}
      sub={formatFileCount(stats?.total_show_files)}
      icon={<Tv className="h-4 w-4" />}
      isLoading={statsQuery.isLoading || (!stats && !statsQuery.error)}
      error={statsQuery.error}
    />
  );
}

export function UsersStatWidget() {
  const statsQuery = useAdminStats();
  const stats = statsQuery.data;
  return (
    <StatTile
      label="Users"
      value={stats ? String(stats.total_users) : "—"}
      sub={`${stats?.total_users ?? 0} registered`}
      icon={<Users className="h-4 w-4" />}
      isLoading={statsQuery.isLoading || (!stats && !statsQuery.error)}
      error={statsQuery.error}
    />
  );
}

export function StorageStatWidget() {
  const statsQuery = useAdminStats();
  const stats = statsQuery.data;
  let storageDisplay = "—";
  if (stats) {
    const storageGB = stats.total_storage_bytes / (1024 * 1024 * 1024);
    const storageTB = storageGB / 1024;
    storageDisplay = storageTB >= 1 ? `${storageTB.toFixed(1)} TB` : `${storageGB.toFixed(0)} GB`;
  }
  return (
    <StatTile
      label="Storage"
      value={storageDisplay}
      sub={formatFileCount(stats?.total_files)}
      icon={<HardDrive className="h-4 w-4" />}
      isLoading={statsQuery.isLoading || (!stats && !statsQuery.error)}
      error={statsQuery.error}
    />
  );
}
