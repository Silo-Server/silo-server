import { Link } from "react-router";
import { Loader, Pause, Play, Radio } from "lucide-react";

import type { AdminSession } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { JellyfinSessionPill } from "@/components/JellyfinSessionPill";
import { Skeleton } from "@/components/ui/skeleton";
import { useHouseholdSessions } from "@/hooks/queries/profiles";
import {
  activityMethodMeta,
  classifyActivityMethod,
  formatSessionBitrate,
  getSessionClientLabel,
  getPlaybackSessionSubtitle,
  getPlaybackSessionTitle,
} from "@/pages/adminActivityPresentation";

function formatElapsed(startedAt: string): string {
  const started = new Date(startedAt).getTime();
  if (Number.isNaN(started)) {
    return "";
  }
  const minutes = Math.max(0, Math.floor((Date.now() - started) / 60_000));
  if (minutes < 60) {
    return `${minutes}m`;
  }
  const hours = Math.floor(minutes / 60);
  const remainder = minutes % 60;
  return remainder > 0 ? `${hours}h ${remainder}m` : `${hours}h`;
}

function streamMeta(session: AdminSession): string {
  return [
    formatElapsed(session.started_at),
    getSessionClientLabel(session),
    session.client_ip,
    session.node_display_name,
  ]
    .filter(Boolean)
    .join(" · ");
}

function StreamRow({ session }: { session: AdminSession }) {
  const title = getPlaybackSessionTitle(session);
  const subtitle = getPlaybackSessionSubtitle(session);
  const profileLabel = session.profile_name?.trim() || "Profile";
  const methodMeta = activityMethodMeta(classifyActivityMethod(session));
  const bitrate = formatSessionBitrate(session.stream_bitrate_kbps);
  const watchHref =
    session.content_id && session.media_file_id
      ? `/watch/${session.content_id}?file=${session.media_file_id}`
      : null;

  return (
    <div className="border-border flex gap-3 rounded-md border px-3 py-3 sm:px-4">
      <div className="bg-muted h-14 w-10 shrink-0 overflow-hidden rounded">
        {session.poster_url ? (
          <img src={session.poster_url} alt="" className="h-full w-full object-cover" />
        ) : null}
      </div>

      <div className="min-w-0 flex-1 space-y-1.5">
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant="outline">{profileLabel}</Badge>
          <Badge variant="outline" className="gap-1">
            {session.is_paused ? <Pause className="h-3 w-3" /> : <Play className="h-3 w-3" />}
            {session.is_paused ? "Paused" : "Playing"}
          </Badge>
          <span className={`inline-flex h-2 w-2 rounded-full ${methodMeta.swatchClass}`} />
          <span className="text-muted-foreground text-xs">
            {methodMeta.label}
            {bitrate ? ` · ${bitrate}` : ""}
          </span>
          <JellyfinSessionPill session={session} />
        </div>

        <div className="min-w-0">
          {watchHref ? (
            <Link to={watchHref} className="truncate text-sm font-semibold hover:underline">
              {title}
            </Link>
          ) : (
            <p className="truncate text-sm font-semibold">{title}</p>
          )}
          {subtitle ? <p className="text-muted-foreground truncate text-xs">{subtitle}</p> : null}
        </div>

        <p className="text-muted-foreground text-xs">{streamMeta(session)}</p>
      </div>
    </div>
  );
}

export function HouseholdStreamsPanel() {
  const { data: sessions = [], isLoading, isFetching } = useHouseholdSessions();

  return (
    <section className="surface-panel space-y-3 rounded-md border px-4 py-4 shadow-none sm:px-5">
      <div className="flex items-start justify-between gap-3">
        <div className="space-y-1">
          <div className="flex items-center gap-2">
            <h3 className="text-sm font-semibold">Active streams</h3>
            {sessions.length > 0 ? (
              <span className="live-badge flex items-center gap-1">
                <Radio className="h-3 w-3" />
                {sessions.length} live
              </span>
            ) : null}
          </div>
          <p className="text-muted-foreground text-sm">
            Playback happening on any profile in this account.
          </p>
        </div>
        {isFetching && !isLoading ? (
          <Loader className="text-muted-foreground h-4 w-4 animate-spin" />
        ) : null}
      </div>

      {isLoading ? (
        <div className="space-y-2">
          <Skeleton className="h-20 w-full rounded-md" />
          <Skeleton className="h-20 w-full rounded-md" />
        </div>
      ) : sessions.length === 0 ? (
        <p className="text-muted-foreground text-sm">No one is streaming right now.</p>
      ) : (
        <div className="space-y-2">
          {sessions.map((session) => (
            <StreamRow key={session.session_id} session={session} />
          ))}
        </div>
      )}
    </section>
  );
}
