import { Link } from "react-router";
import { Pause, Play } from "lucide-react";
import { JellyfinSessionPill } from "@/components/JellyfinSessionPill";
import { PlaybackRouteBadges } from "@/components/PlaybackRouteBadges";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminSessions } from "@/hooks/queries/admin/stats";
import {
  activityMethodMeta,
  classifyActivityMethod,
  getSessionClientLabel,
} from "@/pages/adminActivityPresentation";
import type { AdminSession } from "@/api/types";
import { formatRelativeTime } from "@/lib/date";
import { SectionError } from "../feedback";
import { useReportCollapsed } from "../widgetChrome";
import { SessionProfilePill } from "./SessionProfilePill";

export function NowPlayingWidget() {
  const sessionsQuery = useAdminSessions();
  const sessions = sessionsQuery.data ?? [];

  // Only a successful, genuinely empty load earns the strip. A skeleton or an
  // error keeps its full height: shrinking the widget to announce a failure
  // would hide the failure.
  const isIdle = !sessionsQuery.isLoading && !sessionsQuery.error && sessions.length === 0;
  useReportCollapsed(isIdle);

  if (isIdle) {
    return (
      <div className="flex h-full min-w-0 items-center gap-3">
        <div className="shrink-0 text-base font-bold">Now Playing</div>
        <div className="text-muted-foreground min-w-0 flex-1 truncate text-sm">
          Nothing playing right now
        </div>
        <Link
          to="/admin/activity"
          className="text-muted-foreground hover:text-primary shrink-0 text-[11px] transition-colors"
        >
          View activity ›
        </Link>
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col">
      <div className="mb-3 flex shrink-0 items-center justify-between">
        <div className="text-base font-bold">Now Playing</div>
        {sessions.length > 0 && (
          <Link
            to="/admin/activity"
            className="text-muted-foreground hover:text-primary text-[11px] transition-colors"
          >
            View all {sessions.length} streams ›
          </Link>
        )}
      </div>
      {/* The stream cards scroll inside the widget: a short widget shows two of
          them rather than spilling out of its row. */}
      <div className="min-h-0 flex-1 overflow-y-auto">
        {sessionsQuery.isLoading ? (
          <div className="grid grid-cols-1 gap-3.5 lg:grid-cols-2">
            {Array.from({ length: 2 }).map((_, i) => (
              <Skeleton key={i} className="h-[120px] rounded-2xl" />
            ))}
          </div>
        ) : sessionsQuery.error ? (
          <SectionError message="Failed to load streams." />
        ) : (
          /* The empty case never reaches here — it returned the collapsed strip
             above — so this branch always has at least one stream to show. */
          <>
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
          </>
        )}
      </div>
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
  const elapsed = formatRelativeTime(session.started_at, {
    rounding: "floor",
    justNowLabel: "Just now",
  });
  const clientLabel = getSessionClientLabel(session);
  const method = classifyActivityMethod(session);
  const methodColor = activityMethodMeta(method).badgeClass;

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
            {method}
          </span>
          <JellyfinSessionPill session={session} />
          {clientLabel ? (
            <span
              title={session.client_user_agent || clientLabel}
              className="border-border/60 bg-muted/30 text-muted-foreground inline-flex max-w-[9rem] rounded border px-1.5 py-0.5 text-[9px] font-semibold"
            >
              <span className="truncate">{clientLabel}</span>
            </span>
          ) : null}
          <PlaybackRouteBadges session={session} />
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
