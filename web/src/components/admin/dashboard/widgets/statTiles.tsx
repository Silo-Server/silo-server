import type { ReactNode } from "react";
import {
  Activity,
  Film,
  Gauge,
  HardDrive,
  ShieldCheck,
  Tv,
  UserCheck,
  Users,
  Zap,
} from "lucide-react";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminSessions, useAdminStats } from "@/hooks/queries/admin/stats";
import {
  useAdminPlaybackActivity,
  useAdminTimeseries,
} from "@/hooks/queries/admin/dashboardInsights";
import { classifyActivityMethod } from "@/pages/adminActivityPresentation";
import { formatFileCount, formatMbps } from "../format";
import { latestFreshPoint } from "./timeseriesSeries";
import { formatLocale } from "@/lib/appLocale";
import { useT } from "@/i18n";
import { formatDecimal } from "@/lib/numberFormat";

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
  const t = useT();
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
        <div className="text-destructive text-sm">
          {t("web.admin.dashboard.widgets.statTile.unavailable")}
        </div>
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
  const t = useT();
  const sessionsQuery = useAdminSessions();
  const sessionCount = sessionsQuery.data?.length ?? 0;
  return (
    <StatTile
      label={t("web.admin.dashboard.widgets.activeStreams.label")}
      value={String(sessionCount)}
      sub={t("web.admin.dashboard.widgets.activeStreams.sessions", {
        count: sessionCount,
        count_text: String(sessionCount),
      })}
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
  const t = useT();
  const timeseriesQuery = useAdminTimeseries(1);
  const latest = latestFreshPoint(timeseriesQuery.data, EGRESS_SAMPLE_MAX_AGE_MS);
  return (
    <StatTile
      label={t("web.admin.dashboard.widgets.egressNow.label")}
      value={latest ? formatMbps(latest.egress_kbps / 1_000) : "—"}
      sub={
        latest
          ? t("web.admin.dashboard.widgets.egressNow.source")
          : t("web.admin.dashboard.widgets.egressNow.waiting")
      }
      icon={<Gauge className="h-4 w-4" />}
      isLoading={timeseriesQuery.isLoading}
      error={timeseriesQuery.error}
    />
  );
}

export function TranscodeShareStatWidget() {
  const t = useT();
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
      label={t("web.admin.dashboard.widgets.transcodeShare.label")}
      value={share === null ? "—" : `${share}%`}
      sub={
        sessions.length > 0
          ? t("web.admin.dashboard.widgets.transcodeShare.detail", {
              count: sessions.length,
              transcoding: transcoding.toLocaleString(formatLocale()),
              count_text: sessions.length.toLocaleString(formatLocale()),
            })
          : t("web.admin.dashboard.widgets.transcodeShare.none")
      }
      icon={<Zap className="h-4 w-4" />}
      isLoading={sessionsQuery.isLoading}
      error={sessionsQuery.error}
    />
  );
}

export function ProfilesActiveStatWidget() {
  const t = useT();
  const activityQuery = useAdminPlaybackActivity(24);
  const profiles = activityQuery.data?.profiles_active_24h;
  return (
    <StatTile
      // A rolling 24h window, not "today": the server has no user timezone, so
      // the label says 24h rather than implying a calendar day.
      label={t("web.admin.dashboard.widgets.profilesActive.label")}
      value={profiles === undefined ? "—" : profiles.toLocaleString(formatLocale())}
      sub={t("web.admin.dashboard.widgets.profilesActive.detail")}
      icon={<UserCheck className="h-4 w-4" />}
      isLoading={activityQuery.isLoading}
      error={activityQuery.error}
    />
  );
}

export function MoviesStatWidget() {
  const t = useT();
  const statsQuery = useAdminStats();
  const stats = statsQuery.data;
  return (
    <StatTile
      label={t("web.admin.dashboard.widgets.movies.label")}
      value={stats ? stats.total_movies.toLocaleString(formatLocale()) : "—"}
      sub={formatFileCount(stats?.total_movie_files)}
      icon={<Film className="h-4 w-4" />}
      isLoading={statsQuery.isLoading || (!stats && !statsQuery.error)}
      error={statsQuery.error}
    />
  );
}

export function ShowsStatWidget() {
  const t = useT();
  const statsQuery = useAdminStats();
  const stats = statsQuery.data;
  return (
    <StatTile
      label={t("web.admin.dashboard.widgets.shows.label")}
      value={stats ? stats.total_shows.toLocaleString(formatLocale()) : "—"}
      sub={formatFileCount(stats?.total_show_files)}
      icon={<Tv className="h-4 w-4" />}
      isLoading={statsQuery.isLoading || (!stats && !statsQuery.error)}
      error={statsQuery.error}
    />
  );
}

/**
 * How many movies and series carry an advisory age. A profile's advisory-age
 * limit only hides titles that have one, and the provider that supplies them is
 * rate limited per day, so this is how an admin sees what the limit acts on.
 */
export function AdvisoryCoverageStatWidget() {
  const t = useT();
  const statsQuery = useAdminStats();
  const stats = statsQuery.data;
  const titles = stats ? stats.total_movies + stats.total_shows : 0;
  const covered = stats?.advisory_titles ?? 0;
  return (
    <StatTile
      label={t("web.admin.dashboard.widgets.advisoryCoverage.label")}
      value={stats ? (titles > 0 ? `${Math.round((covered / titles) * 100)}%` : "—") : "—"}
      sub={t("web.admin.dashboard.widgets.advisoryCoverage.detail", {
        count: titles,
        covered: covered.toLocaleString(formatLocale()),
        count_text: titles.toLocaleString(formatLocale()),
      })}
      icon={<ShieldCheck className="h-4 w-4" />}
      isLoading={statsQuery.isLoading || (!stats && !statsQuery.error)}
      error={statsQuery.error}
    />
  );
}

export function UsersStatWidget() {
  const t = useT();
  const statsQuery = useAdminStats();
  const stats = statsQuery.data;
  return (
    <StatTile
      label={t("web.admin.dashboard.widgets.usersStat.label")}
      value={stats ? String(stats.total_users) : "—"}
      sub={t("web.admin.dashboard.widgets.usersStat.registered", {
        count: stats?.total_users ?? 0,
        count_text: String(stats?.total_users ?? 0),
      })}
      icon={<Users className="h-4 w-4" />}
      isLoading={statsQuery.isLoading || (!stats && !statsQuery.error)}
      error={statsQuery.error}
    />
  );
}

export function StorageStatWidget() {
  const t = useT();
  const statsQuery = useAdminStats();
  const stats = statsQuery.data;
  let storageDisplay = "—";
  if (stats) {
    const storageGB = stats.total_storage_bytes / (1024 * 1024 * 1024);
    const storageTB = storageGB / 1024;
    storageDisplay =
      storageTB >= 1 ? `${formatDecimal(storageTB, 1)} TB` : `${formatDecimal(storageGB, 0)} GB`;
  }
  return (
    <StatTile
      label={t("web.admin.dashboard.widgets.storage.label")}
      value={storageDisplay}
      sub={formatFileCount(stats?.total_files)}
      icon={<HardDrive className="h-4 w-4" />}
      isLoading={statsQuery.isLoading || (!stats && !statsQuery.error)}
      error={statsQuery.error}
    />
  );
}
